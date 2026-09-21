package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/compile"
	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

func TestCodeAndBoldEscapeWhatTheyWrap(t *testing.T) {
	if got := code("List<String> & co"); got != "<code>List&lt;String&gt; &amp; co</code>" {
		t.Errorf("code() = %q", got)
	}
	if got := bold("a < b"); got != "<b>a &lt; b</b>" {
		t.Errorf("bold() = %q", got)
	}
}

// The magic bytes are CAFEBABE, then two bytes of minor and two of major — see JVMS 4.1. 65535 is
// not an arbitrary large number: it is the JVM's own preview marker.
func TestClassfileVersionReadsMajorMinorFromTheHeader(t *testing.T) {
	header := []byte{0xCA, 0xFE, 0xBA, 0xBE, 0xFF, 0xFF, 0x00, 0x48} // minor 65535, major 72
	if got := classfileVersion(header); got != "72.65535" {
		t.Errorf("classfileVersion() = %q, want %q", got, "72.65535")
	}
	if got := classfileVersion([]byte{1, 2, 3}); got != "" {
		t.Errorf("classfileVersion(short) = %q, want empty", got)
	}
}

func TestMediaCaptionOmitsThePositionForASingleImage(t *testing.T) {
	one := mediaCaption("SnippetKt.class", "Kotlin 2.4.20 · bytecode 25", 1, 1)
	if strings.Contains(one, "(1/1)") {
		t.Errorf("a single image should not carry a position: %q", one)
	}
	if !strings.Contains(one, "<code>SnippetKt.class · Kotlin 2.4.20 · bytecode 25</code>") {
		t.Errorf("caption = %q, want the name and descriptor in one code span", one)
	}

	many := mediaCaption("SnippetKt.class", "Kotlin 2.4.20 · bytecode 25", 1, 3)
	if !strings.Contains(many, "<b>(1/3)</b>") {
		t.Errorf("caption = %q, want a bold position out of three", many)
	}
}

func TestCompileSummaryNamesTheRightSecondFieldPerLanguage(t *testing.T) {
	result := compile.Result{
		Artifacts: []compile.Artifact{{Name: "A.class", Bytes: []byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 52}}},
		Took:      1216 * time.Millisecond,
	}

	java := compileSummary(detect.Java, toolchain.Release{Label: "JDK 28", Major: 28}, "28", result, 1)
	if !strings.Contains(java, "<b>JDK</b>: <code>28</code>") {
		t.Errorf("java summary = %q, want a JDK field", java)
	}
	if !strings.Contains(java, "<code>52.0</code>") {
		t.Errorf("java summary = %q, want the classfile version", java)
	}

	kotlin := compileSummary(detect.Kotlin, toolchain.Release{Label: "Kotlin 2.4.20"}, "25", result, 1)
	if !strings.Contains(kotlin, "<b>Version</b>: <code>2.4.20</code>") {
		t.Errorf("kotlin summary = %q, want a bare Version field", kotlin)
	}
	if strings.Contains(kotlin, "JDK") {
		t.Errorf("kotlin summary should not claim a JDK field: %q", kotlin)
	}
}

func TestCompileSummaryNotesSkippedArtifacts(t *testing.T) {
	result := compile.Result{
		Artifacts: []compile.Artifact{{Name: "A.class"}, {Name: "B.class"}},
		Took:      time.Second,
	}
	got := compileSummary(detect.Java, toolchain.Release{Major: 28}, "28", result, 1)
	if !strings.Contains(got, "<b>Not shown</b>: <code>1</code>") {
		t.Errorf("summary = %q, want one artifact reported as not shown", got)
	}
}
