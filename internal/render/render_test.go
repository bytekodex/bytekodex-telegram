package render

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Disassembly text is used as the input here so the test needs no compiled fixture. The binary
// path is covered on the Rust side, against a few hundred real class files.
const disassembly = `  public toString()Ljava/lang/String;
    Code:
         0:    aload_0
         1:    getfield #2
         4:    invokevirtual #3
         7:    areturn
`

func font(t *testing.T) []byte {
	t.Helper()
	for _, path := range []string{
		"/System/Library/Fonts/SFNSMono.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
		"/usr/share/fonts/TTF/DejaVuSansMono.ttf",
		"C:/Windows/Fonts/consola.ttf",
	} {
		if bytes, err := os.ReadFile(path); err == nil {
			return bytes
		}
	}
	t.Skip("no monospace TrueType face installed")
	return nil
}

func options() Options {
	return Options{Platform: JVM, Kind: DisassemblyText, View: ViewMethods, Corner: 12}
}

func TestRenderProducesAPNGWithoutTouchingDisk(t *testing.T) {
	renderer, err := New(font(t), 24, 2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer renderer.Close()

	image, err := renderer.Render([]byte(disassembly), options())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	defer image.Close()

	if image.Len() == 0 {
		t.Fatal("rendered nothing")
	}
	header := make([]byte, 8)
	if _, err := image.Read(header); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(header, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("not a PNG: %q", header)
	}
	if image.Stats.Opcodes != 4 {
		t.Errorf("Opcodes = %d, want 4", image.Stats.Opcodes)
	}
	if image.Stats.Width == 0 || image.Stats.Height == 0 {
		t.Errorf("empty geometry: %dx%d", image.Stats.Width, image.Stats.Height)
	}
}

// The whole point of the pooled buffer is that a second render reuses the first one's memory,
// so closing and rendering again must give the same bytes rather than a freed slice.
func TestBufferIsReusedNotCorrupted(t *testing.T) {
	renderer, err := New(font(t), 24, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer renderer.Close()

	first, err := renderer.Render([]byte(disassembly), options())
	if err != nil {
		t.Fatalf("first Render: %v", err)
	}
	before := make([]byte, first.Len())
	if _, err := first.Read(before); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	first.Close()

	second, err := renderer.Render([]byte(disassembly), options())
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}
	defer second.Close()
	after := make([]byte, second.Len())
	if _, err := second.Read(after); err != nil {
		t.Fatalf("second Read: %v", err)
	}

	if !bytes.Equal(before, after) {
		t.Error("identical input rendered differently after the buffer was recycled")
	}
}

func TestPagesReportsCountsWithoutRendering(t *testing.T) {
	renderer, err := New(font(t), 24, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer renderer.Close()

	stats, err := renderer.Pages([]byte(disassembly), options())
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if stats.PagesTotal != 1 {
		t.Errorf("PagesTotal = %d, want 1", stats.PagesTotal)
	}
	if stats.Opcodes != 4 {
		t.Errorf("Opcodes = %d, want 4", stats.Opcodes)
	}
}

func TestUnknownPlatformIsRejected(t *testing.T) {
	renderer, err := New(font(t), 24, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer renderer.Close()

	bad := options()
	bad.Platform = 99
	if _, err := renderer.Render([]byte(disassembly), bad); err == nil {
		t.Fatal("expected an error for an unknown platform")
	}
}

// A rendered page has to fit inside Telegram's photo cap, so the limit must actually bite.
func TestTooLargeAPageIsRefused(t *testing.T) {
	renderer, err := New(font(t), 24, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer renderer.Close()

	tight := options()
	tight.MaxDimSum = 16
	_, err = renderer.Render([]byte(disassembly), tight)
	if err == nil {
		t.Fatal("expected the dimension limit to be enforced")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("unhelpful error: %v", err)
	}
}
