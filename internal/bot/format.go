package bot

import (
	"encoding/binary"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/compile"
	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

// Every outgoing message is sent with ParseMode HTML (see edit and send below), so any text that
// is not deliberately wrapped in a tag still has to be escaped: a compiler diagnostic mentioning
// List<String>, or a filename with an ampersand in it, would otherwise be parsed as markup and
// vanish from what the user sees instead of appearing as the text it is.

// code renders text the way an identifier — a class name, a version, a filename — reads best:
// monospace, exactly as written.
func code(s string) string { return "<code>" + html.EscapeString(s) + "</code>" }

// bold calls out a count or a short label without pretending it is code.
func bold(s string) string { return "<b>" + html.EscapeString(s) + "</b>" }

// esc escapes plain text that sits next to a code or bold span in the same message, so a stray
// "<" from user input cannot be mistaken for the start of a tag.
func esc(s string) string { return html.EscapeString(s) }

// success and fail prefix the two kinds of message this bot ever ends on: something rendered, or
// it did not. Named success rather than ok because ok is already every other error check's local
// variable name in this package, and shadowing it here would be the kind of bug go vet catches
// only by accident.
const (
	success = "✅ "
	fail    = "❌ "
)

// mediaCaption is what appears under every rendered document — a compiled class or a system
// class read straight off the store. The gear is decoration, the identifier and its release are
// monospace because they are one, and the position is bold because it is the one number here a
// user might actually need to act on (which attachment is which, out of how many).
func mediaCaption(name, descriptor string, index, total int) string {
	text := name
	if descriptor != "" {
		text += " · " + descriptor
	}
	caption := "⚙️ " + code(text)
	if total > 1 {
		caption += " " + bold(fmt.Sprintf("(%d/%d)", index, total))
	}
	return caption
}

// classfileVersion reads the major.minor pair straight out of a class file's header (bytes 4-8,
// after the CAFEBABE magic) without needing anything from the renderer. Minor 65535 is the JVM's
// own preview marker — the one --enable-preview turns on and off — so this number is also the
// answer to "is this what unlocked ACC_IDENTITY on a value class". Returns "" for anything too
// short to have a header at all, which a caller treats as "do not show this field" rather than
// as an error: by the time this runs the class already rendered, so a short slice here would be
// this function's bug, not the input's.
func classfileVersion(class []byte) string {
	if len(class) < 8 {
		return ""
	}
	minor := binary.BigEndian.Uint16(class[4:6])
	major := binary.BigEndian.Uint16(class[6:8])
	return fmt.Sprintf("%d.%d", major, minor)
}

// field is one line of a bulleted summary: a bold label, a monospace value.
type field struct{ key, value string }

// bullets renders a field list the way every structured summary in this package wants it.
func bullets(fields []field) string {
	var out string
	for _, f := range fields {
		out += fmt.Sprintf("\n- %s: %s", bold(f.key), code(f.value))
	}
	return out
}

// compileSummary is the message a successful compile ends on: one line saying it happened, then
// a bullet per fact worth knowing about what came out. The second field is language-specific —
// Java calls out the JDK, everything else the compiler's own version — because "JDK 28" and
// "2.4.20" both answer the same question ("compiled with what") in the terms that language's own
// release naming already uses everywhere else in this bot.
func compileSummary(language detect.Language, release toolchain.Release, target string, result compile.Result, shown int) string {
	fields := []field{{"Language", language.Display()}}
	if language == detect.Java {
		fields = append(fields, field{"JDK", fmt.Sprint(release.Major)})
	} else {
		fields = append(fields, field{"Version", strings.TrimPrefix(release.Label, language.Display()+" ")})
	}
	fields = append(fields, field{"Target", target})
	if len(result.Artifacts) > 0 {
		if classfile := classfileVersion(result.Artifacts[0].Bytes); classfile != "" {
			fields = append(fields, field{"Classfile", classfile})
		}
	}
	fields = append(fields, field{"Class(es)", fmt.Sprint(len(result.Artifacts))})
	if skipped := len(result.Artifacts) - shown; skipped > 0 {
		fields = append(fields, field{"Not shown", fmt.Sprint(skipped)})
	}

	return fmt.Sprintf("%sBytecode decompiled in %s%s", success, bold(result.Took.Round(time.Millisecond).String()), bullets(fields))
}
