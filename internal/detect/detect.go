// Package detect guesses which JVM language a snippet is written in.
//
// The guess is a starting point for the keyboard, never a decision: the user sees what was
// detected and can override it without being asked a blocking question first. So the scoring
// only has to be right often enough to save a tap, and being wrong costs one.
package detect

import (
	"path"
	"regexp"
	"strings"
)

// Language is one of the languages the bot can compile.
type Language string

const (
	Java    Language = "java"
	Kotlin  Language = "kotlin"
	Groovy  Language = "groovy"
	Scala   Language = "scala"
	Unknown Language = ""
)

// Display is the name shown to a user.
func (l Language) Display() string {
	switch l {
	case Java:
		return "Java"
	case Kotlin:
		return "Kotlin"
	case Groovy:
		return "Groovy"
	case Scala:
		return "Scala"
	default:
		return "unknown"
	}
}

// Extension is the source suffix the compiler expects.
func (l Language) Extension() string {
	switch l {
	case Java:
		return ".java"
	case Kotlin:
		return ".kt"
	case Groovy:
		return ".groovy"
	case Scala:
		return ".scala"
	default:
		return ".txt"
	}
}

// Guess is a language with the confidence behind it.
type Guess struct {
	Language Language
	// Confident is true when one language won by a clear margin. When it is false the bot still
	// preselects Language, but says it is unsure.
	Confident bool
}

// ByExtension maps a filename to a language. A user who attached a file has already told us what
// it is, so this beats any amount of scoring.
func ByExtension(filename string) Language {
	switch strings.ToLower(path.Ext(filename)) {
	case ".java":
		return Java
	case ".kt", ".kts":
		return Kotlin
	case ".groovy", ".gvy", ".gradle":
		return Groovy
	case ".scala", ".sc":
		return Scala
	default:
		return Unknown
	}
}

// signal is a pattern that argues for a language, with a weight. Weights are small integers on
// purpose: a signal is evidence, not proof, and no single one should decide the outcome.
type signal struct {
	pattern *regexp.Regexp
	weight  int
}

var signals = map[Language][]signal{
	Kotlin: {
		{regexp.MustCompile(`(?m)^\s*fun\s+\w+\s*\(`), 4},
		{regexp.MustCompile(`\bval\s+\w+\s*[:=]`), 3},
		{regexp.MustCompile(`\bvar\s+\w+\s*:`), 3},
		{regexp.MustCompile(`\bsuspend\s+fun\b`), 5},
		{regexp.MustCompile(`\bdata\s+class\b`), 5},
		{regexp.MustCompile(`\bcompanion\s+object\b`), 5},
		{regexp.MustCompile(`\bwhen\s*[({]`), 2},
		{regexp.MustCompile(`\?:|!!`), 2},
		{regexp.MustCompile(`(?m)^import\s+kotlinx?\.`), 4},
	},
	Java: {
		{regexp.MustCompile(`\b(public|private|protected)\s+(static\s+)?(final\s+)?\w+[\w<>\[\], ]*\s+\w+\s*\(`), 3},
		{regexp.MustCompile(`\bpublic\s+static\s+void\s+main\s*\(`), 5},
		{regexp.MustCompile(`\bSystem\.out\.print`), 3},
		{regexp.MustCompile(`\bnew\s+\w+\s*\(`), 2},
		{regexp.MustCompile(`(?m)^import\s+java(x)?\.`), 3},
		{regexp.MustCompile(`\bextends\s+\w+|\bimplements\s+\w+`), 2},
		{regexp.MustCompile(`\b(record|sealed|permits)\s`), 3},
	},
	Groovy: {
		{regexp.MustCompile(`\bdef\s+\w+`), 4},
		{regexp.MustCompile(`\bprintln\s+['"$]`), 4},
		{regexp.MustCompile(`@(Grab|CompileStatic|TypeChecked)\b`), 5},
		{regexp.MustCompile(`\bit\s*\.\w+|\{\s*it\s*->`), 3},
		{regexp.MustCompile(`\$\{[^}]+\}`), 1},
		{regexp.MustCompile(`\b\w+\s*=\s*\[[^\]]*:`), 3},
	},
	Scala: {
		{regexp.MustCompile(`(?m)^\s*def\s+\w+\s*[(\[:]`), 4},
		{regexp.MustCompile(`\bobject\s+\w+\s*(extends|\{)`), 4},
		{regexp.MustCompile(`\bcase\s+class\b`), 5},
		{regexp.MustCompile(`\bimplicit\b|\bgiven\b`), 3},
		{regexp.MustCompile(`=>\s`), 1},
		{regexp.MustCompile(`(?m)^import\s+scala\.`), 4},
	},
}

// Java's method-declaration signal also matches Kotlin and Scala bodies often enough to matter,
// so a keyword only those languages have cancels it out.
var notJava = regexp.MustCompile(`(?m)^\s*(fun|def|val|case class|object)\b`)

// Source guesses from code alone. It never returns an error: an unrecognized snippet is a Guess
// with Unknown and Confident false, which the keyboard shows as "pick a language".
func Source(code string) Guess {
	code = stripComments(code)

	scores := make(map[Language]int, len(signals))
	for language, patterns := range signals {
		for _, s := range patterns {
			if s.pattern.MatchString(code) {
				scores[language] += s.weight
			}
		}
	}
	if notJava.MatchString(code) {
		scores[Java] -= 3
	}

	best, second, winner := 0, 0, Unknown
	// Iterating a map is unordered, so ties are broken by a fixed preference rather than by luck.
	for _, language := range []Language{Kotlin, Java, Groovy, Scala} {
		switch score := scores[language]; {
		case score > best:
			best, second, winner = score, best, language
		case score > second:
			second = score
		}
	}

	if best < 3 {
		return Guess{Language: Unknown}
	}
	return Guess{Language: winner, Confident: best-second >= 3}
}

// stripComments removes line and block comments so that prose and commented-out code in another
// language cannot outvote the code itself. String literals are left alone; misreading one is
// cheaper than tracking escapes.
func stripComments(code string) string {
	var out strings.Builder
	out.Grow(len(code))

	for i := 0; i < len(code); {
		switch {
		case strings.HasPrefix(code[i:], "//"):
			end := strings.IndexByte(code[i:], '\n')
			if end < 0 {
				return out.String()
			}
			i += end
		case strings.HasPrefix(code[i:], "/*"):
			end := strings.Index(code[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
		default:
			out.WriteByte(code[i])
			i++
		}
	}
	return out.String()
}
