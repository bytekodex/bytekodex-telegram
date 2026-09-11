package bot

import "html"

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
