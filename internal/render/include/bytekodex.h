/*
 * Bytekodex platform — C ABI.
 *
 * Written by hand rather than generated, so the header is the specification and a change to it
 * is a deliberate, reviewable act. `bk_abi_version` exists to catch the case where it drifts
 * from the Rust side anyway.
 *
 * Ownership rules, in full:
 *   - Every pointer passed in is borrowed for the duration of the call and never retained.
 *   - The library never returns memory the caller must free, except the renderer handle.
 *   - The output buffer belongs to the caller. If it is too small, `required` says how big it
 *     must be and nothing was written; grow it and call again.
 *
 * Threading: a `bk_renderer` is not thread-safe, because its glyph cache fills in lazily. Keep
 * one per worker, or guard it. Everything else is stateless apart from the thread-local error
 * message read by `bk_last_error`.
 */

#ifndef BYTEKODEX_H
#define BYTEKODEX_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define BK_ABI_VERSION 1u

/* Status codes. Negative values are errors and are never renumbered. */
#define BK_OK                        0
#define BK_ERR_ABI_MISMATCH         (-1)
#define BK_ERR_INVALID_ARGUMENT     (-2)
#define BK_ERR_UNSUPPORTED_PLATFORM (-3)
#define BK_ERR_UNSUPPORTED_INPUT    (-4)
#define BK_ERR_MALFORMED            (-5)
#define BK_ERR_UNSUPPORTED_VERSION  (-6)
#define BK_ERR_BUFFER_TOO_SMALL     (-7)
#define BK_ERR_IMAGE_TOO_LARGE      (-8)
#define BK_ERR_ENCODE               (-9)
#define BK_ERR_FONT                 (-10)
#define BK_ERR_PAGE_OUT_OF_RANGE    (-11)
#define BK_ERR_INTERNAL             (-99)

#define BK_PLATFORM_JVM 1u
#define BK_PLATFORM_CIL 2u /* reserved, not implemented */

#define BK_INPUT_BINARY            1u /* raw .class bytes */
#define BK_INPUT_DISASSEMBLY_TEXT  2u /* javap output pasted by a user */
/*
 * Compiler output. Rendered like everything else, because an error is the second most common
 * thing a user reads and plain text throws away which line and column it was about. Belongs to
 * no bytecode format, so `platform` is ignored for it.
 */
#define BK_INPUT_DIAGNOSTIC        3u

/* View flags. Zero means BK_VIEW_METHODS. */
#define BK_VIEW_METHODS       (1u << 0)
#define BK_VIEW_CONSTANT_POOL (1u << 1)
#define BK_VIEW_LOCALS        (1u << 2)
#define BK_VIEW_STACK_MAP     (1u << 3)
#define BK_VIEW_LINE_NUMBERS  (1u << 4)
#define BK_VIEW_ATTRIBUTES    (1u << 5)

/* A borrowed byte run. Not NUL-terminated: class files contain NUL bytes. */
typedef struct bk_slice {
  const uint8_t *ptr;
  size_t len;
} bk_slice;

typedef struct bk_request {
  uint32_t abi_version;       /* must be BK_ABI_VERSION */
  uint32_t platform;          /* BK_PLATFORM_* */
  uint32_t input_kind;        /* BK_INPUT_* */
  uint32_t view_flags;        /* BK_VIEW_* bitmask, 0 for the default */
  uint32_t page;              /* zero-based */
  uint32_t page_rows;         /* 0 for the default */
  float    font_size;         /* honored by bk_renderer_new, not per call */
  uint32_t margin;            /* 0 for the default */
  uint32_t corner_radius;     /* 0 for square corners */
  uint32_t max_dimension_sum; /* width + height ceiling; 0 for the default 16000 */
  bk_slice input;
} bk_request;

typedef struct bk_response {
  size_t   written;       /* PNG bytes written into the output buffer */
  size_t   required;      /* bytes the buffer needs; equals written on success */
  uint32_t pages_total;
  uint32_t width;
  uint32_t height;
  uint64_t opcodes_total; /* filled in even when rendering failed */
  uint32_t methods;
  uint32_t fields;
  int32_t  status;
} bk_response;

typedef struct bk_renderer bk_renderer;

uint32_t bk_abi_version(void);

/* Opcodes decoded since the library was loaded. For metrics. */
uint64_t bk_opcodes_decoded_total(void);

/*
 * Builds a renderer and rasterizes the ASCII range into its glyph cache. `font` must be a
 * single monospace TrueType face — a `.ttc` collection is not one. The bytes are not retained.
 * Returns NULL on failure and writes the status to `status` when it is non-NULL.
 */
bk_renderer *bk_renderer_new(bk_slice font, float font_size, int32_t *status);

void bk_renderer_free(bk_renderer *renderer);

/*
 * Renders one page into `out`, writing counts and geometry to `response`.
 *
 * Returns BK_OK, or BK_ERR_BUFFER_TOO_SMALL with `response->required` set, or another error.
 * Pass out = NULL and out_cap = 0 to get counts and the required size without rendering bytes.
 */
int32_t bk_render(bk_renderer *renderer, const bk_request *request, uint8_t *out, size_t out_cap,
                  bk_response *response);

/*
 * Copies the calling thread's last error into `buf` as NUL-terminated UTF-8 and returns the
 * size it needs, terminator included. Pass buf = NULL to query the size first.
 */
size_t bk_last_error(uint8_t *buf, size_t cap);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* BYTEKODEX_H */
