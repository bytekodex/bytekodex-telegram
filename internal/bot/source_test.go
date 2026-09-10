package bot

import "testing"

// The image is as wide as its longest line, so trailing whitespace costs real pixels and real
// render time for something nobody can see.
func TestTrailingWhitespaceIsStrippedFromEveryLine(t *testing.T) {
	got := normalizeSource("class A {   \n    int x;\t\t\n}  ")
	want := "class A {\n    int x;\n}"
	if got != want {
		t.Errorf("normalizeSource = %q, want %q", got, want)
	}
}

// Windows clients send CRLF, and a stray carriage return would be measured and drawn as a glyph.
func TestCarriageReturnsBecomeNewlines(t *testing.T) {
	for _, input := range []string{"a\r\nb", "a\rb"} {
		if got := normalizeSource(input); got != "a\nb" {
			t.Errorf("normalizeSource(%q) = %q, want %q", input, got, "a\nb")
		}
	}
}

func TestBlankLinesAtEitherEndAreDropped(t *testing.T) {
	got := normalizeSource("\n\n  \nclass A {}\n\n   \n")
	if got != "class A {}" {
		t.Errorf("normalizeSource = %q, want %q", got, "class A {}")
	}
}

// Indentation is the one kind of whitespace that carries meaning, in Groovy and in readability.
func TestLeadingIndentationSurvives(t *testing.T) {
	got := normalizeSource("class A {\n        int deeplyIndented;\n}")
	if want := "class A {\n        int deeplyIndented;\n}"; got != want {
		t.Errorf("normalizeSource = %q, want %q", got, want)
	}
}

// Non-breaking spaces arrive from people pasting out of web pages and documentation, where they
// are invisible and yet widen every page.
func TestNonBreakingSpacesAtLineEndsAreStripped(t *testing.T) {
	if got := normalizeSource("int x;\u00a0\u00a0"); got != "int x;" {
		t.Errorf("normalizeSource = %q, want %q", got, "int x;")
	}
}

func TestAMarkdownFenceIsRemovedBeforeNormalizing(t *testing.T) {
	got := normalizeSource(stripFence("```java\nclass A {}   \n```"))
	if got != "class A {}" {
		t.Errorf("got %q, want %q", got, "class A {}")
	}
}
