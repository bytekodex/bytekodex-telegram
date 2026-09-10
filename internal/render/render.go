// Package render binds the Rust platform over its C ABI.
//
// The image never touches the filesystem. Rust writes the PNG into a Go byte slice we hand it,
// and that same slice is what the Telegram client streams from, so the bytes are written once
// and read once. Buffers come from a pool and go back when the caller closes the result.
//
// The header under include/ is vendored rather than reached for across repositories, since the
// two projects release independently. New checks bk_abi_version, so a stale copy is caught
// loudly instead of corrupting a struct layout in silence.
package render

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: -lbytekodex
// #include <stdlib.h>
// #include "bytekodex.h"
import "C"

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"unsafe"
)

// Platform selects which bytecode dialect the input is.
type Platform uint32

const (
	JVM Platform = C.BK_PLATFORM_JVM
	CIL Platform = C.BK_PLATFORM_CIL
)

// InputKind distinguishes a compiled artifact from disassembly a user pasted in.
type InputKind uint32

const (
	Binary          InputKind = C.BK_INPUT_BINARY
	DisassemblyText InputKind = C.BK_INPUT_DISASSEMBLY_TEXT
	// Diagnostic is compiler output. It renders like a dump and needs no platform.
	Diagnostic InputKind = C.BK_INPUT_DIAGNOSTIC
)

// View selects which sections of a class appear in the output.
type View uint32

const (
	ViewMethods      View = C.BK_VIEW_METHODS
	ViewConstantPool View = C.BK_VIEW_CONSTANT_POOL
	ViewLocals       View = C.BK_VIEW_LOCALS
	ViewStackMap     View = C.BK_VIEW_STACK_MAP
	ViewLineNumbers  View = C.BK_VIEW_LINE_NUMBERS
	ViewAttributes   View = C.BK_VIEW_ATTRIBUTES
)

// ErrUnsupported reports a platform the library was not built with.
var ErrUnsupported = errors.New("render: unsupported bytecode platform")

// Error carries a status code from the library together with its message.
type Error struct {
	Status  int32
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("render: status %d", e.Status)
	}
	return fmt.Sprintf("render: %s (status %d)", e.Message, e.Status)
}

// Options describes one page request.
type Options struct {
	Platform  Platform
	Kind      InputKind
	View      View
	Page      uint32
	PageRows  uint32
	Margin    uint32
	Corner    uint32
	MaxDimSum uint32 // 0 uses the library default, which stays under Telegram's cap
}

// Stats are the counts the library gathered while decoding.
type Stats struct {
	PagesTotal uint32
	Width      uint32
	Height     uint32
	Opcodes    uint64
	Methods    uint32
	Fields     uint32
}

// Image is a rendered PNG. Read it, then Close it to return the buffer to the pool. Reading
// after Close yields nothing; that is a bug in the caller, not a use-after-free.
type Image struct {
	Stats  Stats
	buffer *[]byte
	reader *bytes.Reader
	pool   *sync.Pool
}

func (i *Image) Read(p []byte) (int, error) {
	if i.reader == nil {
		return 0, io.EOF
	}
	return i.reader.Read(p)
}

// Len is the PNG size in bytes, which Telegram wants to know before the upload starts.
func (i *Image) Len() int {
	if i.reader == nil {
		return 0
	}
	return int(i.reader.Size())
}

func (i *Image) Close() error {
	if i.buffer == nil {
		return nil
	}
	i.pool.Put(i.buffer)
	i.buffer, i.reader = nil, nil
	return nil
}

// Renderer owns a glyph cache, which fills in lazily and so is not safe to share. One is kept
// per worker and leased out.
type Renderer struct {
	mu      sync.Mutex
	handles chan *handle
	buffers sync.Pool
}

type handle struct {
	ptr     *C.bk_renderer
	cleanup runtime.Cleanup
}

// initialBufferSize is a page of code at the default font size, measured rather than guessed:
// a full method listing lands between 60 and 200 KiB.
const initialBufferSize = 256 << 10

// New builds workers renderers over one font. The font must be a single TrueType face; a .ttc
// collection is several, and will be refused.
func New(font []byte, fontSize float32, workers int) (*Renderer, error) {
	if got := uint32(C.bk_abi_version()); got != C.BK_ABI_VERSION {
		return nil, fmt.Errorf("render: library speaks ABI %d, this build expects %d", got, C.BK_ABI_VERSION)
	}
	if len(font) == 0 {
		return nil, errors.New("render: empty font")
	}
	if workers < 1 {
		workers = 1
	}

	r := &Renderer{
		handles: make(chan *handle, workers),
		buffers: sync.Pool{New: func() any {
			buffer := make([]byte, initialBufferSize)
			return &buffer
		}},
	}

	for range workers {
		var status C.int32_t
		ptr := C.bk_renderer_new(slice(font), C.float(fontSize), &status)
		runtime.KeepAlive(font)
		if ptr == nil {
			r.Close()
			return nil, statusError(int32(status))
		}
		h := &handle{ptr: ptr}
		// A forgotten Close must not leak native memory. Close stops the cleanup first, so the
		// handle is freed exactly once either way.
		h.cleanup = runtime.AddCleanup(h, func(p *C.bk_renderer) { C.bk_renderer_free(p) }, ptr)
		r.handles <- h
	}
	return r, nil
}

// Close frees the native renderers. Calls in flight keep their lease, so Close waits for them.
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		select {
		case h := <-r.handles:
			h.cleanup.Stop()
			C.bk_renderer_free(h.ptr)
			h.ptr = nil
		default:
			return nil
		}
	}
}

// Render draws one page of input. The returned Image must be closed.
func (r *Renderer) Render(input []byte, options Options) (*Image, error) {
	if len(input) == 0 {
		return nil, errors.New("render: empty input")
	}

	h := <-r.handles
	defer func() { r.handles <- h }()
	if h.ptr == nil {
		return nil, errors.New("render: renderer is closed")
	}

	var pinner runtime.Pinner
	defer pinner.Unpin()

	request := C.bk_request{
		abi_version:       C.BK_ABI_VERSION,
		platform:          C.uint32_t(options.Platform),
		input_kind:        C.uint32_t(options.Kind),
		view_flags:        C.uint32_t(options.View),
		page:              C.uint32_t(options.Page),
		page_rows:         C.uint32_t(options.PageRows),
		margin:            C.uint32_t(options.Margin),
		corner_radius:     C.uint32_t(options.Corner),
		max_dimension_sum: C.uint32_t(options.MaxDimSum),
		input:             pinned(&pinner, input),
	}

	buffer := r.buffers.Get().(*[]byte)
	var response C.bk_response

	status := call(h.ptr, &request, *buffer, &response)
	// The library reports the exact size it needs, so one retry is always enough.
	if status == C.BK_ERR_BUFFER_TOO_SMALL {
		grown := make([]byte, int(response.required))
		*buffer = grown
		status = call(h.ptr, &request, *buffer, &response)
	}
	runtime.KeepAlive(input)

	if status != C.BK_OK {
		r.buffers.Put(buffer)
		return nil, statusError(int32(status))
	}

	written := int(response.written)
	return &Image{
		Stats: Stats{
			PagesTotal: uint32(response.pages_total),
			Width:      uint32(response.width),
			Height:     uint32(response.height),
			Opcodes:    uint64(response.opcodes_total),
			Methods:    uint32(response.methods),
			Fields:     uint32(response.fields),
		},
		buffer: buffer,
		reader: bytes.NewReader((*buffer)[:written]),
		pool:   &r.buffers,
	}, nil
}

// Pages reports how many pages the input needs without rendering any of them.
func (r *Renderer) Pages(input []byte, options Options) (Stats, error) {
	h := <-r.handles
	defer func() { r.handles <- h }()

	var pinner runtime.Pinner
	defer pinner.Unpin()

	request := C.bk_request{
		abi_version: C.BK_ABI_VERSION,
		platform:    C.uint32_t(options.Platform),
		input_kind:  C.uint32_t(options.Kind),
		view_flags:  C.uint32_t(options.View),
		page_rows:   C.uint32_t(options.PageRows),
		input:       pinned(&pinner, input),
	}

	var response C.bk_response
	status := C.bk_render(h.ptr, &request, nil, 0, &response)
	runtime.KeepAlive(input)
	if status != C.BK_OK && status != C.BK_ERR_BUFFER_TOO_SMALL {
		return Stats{}, statusError(int32(status))
	}
	return Stats{
		PagesTotal: uint32(response.pages_total),
		Opcodes:    uint64(response.opcodes_total),
		Methods:    uint32(response.methods),
		Fields:     uint32(response.fields),
	}, nil
}

// OpcodesDecoded is a process-wide counter, for metrics.
func OpcodesDecoded() uint64 { return uint64(C.bk_opcodes_decoded_total()) }

func call(ptr *C.bk_renderer, request *C.bk_request, out []byte, response *C.bk_response) C.int32_t {
	return C.bk_render(ptr, request, (*C.uint8_t)(unsafe.Pointer(&out[0])), C.size_t(len(out)), response)
}

// slice borrows Go memory for the duration of one call. Passing a pointer into a Go slice is
// allowed because the bytes hold no Go pointers, and the library never retains it.
func slice(b []byte) C.bk_slice {
	if len(b) == 0 {
		return C.bk_slice{}
	}
	return C.bk_slice{ptr: (*C.uint8_t)(unsafe.Pointer(&b[0])), len: C.size_t(len(b))}
}

// pinned is slice for a pointer that will travel inside a struct rather than as an argument.
// cgo refuses a Go pointer reachable through another Go pointer unless it is pinned, since the
// collector would otherwise be free to move it while C holds the address.
func pinned(pinner *runtime.Pinner, b []byte) C.bk_slice {
	if len(b) == 0 {
		return C.bk_slice{}
	}
	pinner.Pin(&b[0])
	return slice(b)
}

func statusError(status int32) error {
	if status == C.BK_ERR_UNSUPPORTED_PLATFORM {
		return ErrUnsupported
	}
	return &Error{Status: status, Message: lastError()}
}

// lastError reads the thread-local message. Go can move a goroutine between threads, but not
// during a cgo call, so the message we read is the one our call left behind.
func lastError() string {
	size := C.bk_last_error(nil, 0)
	if size <= 1 {
		return ""
	}
	buffer := make([]byte, int(size))
	written := C.bk_last_error((*C.uint8_t)(unsafe.Pointer(&buffer[0])), C.size_t(len(buffer)))
	if written == 0 {
		return ""
	}
	return string(bytes.TrimRight(buffer[:written], "\x00"))
}
