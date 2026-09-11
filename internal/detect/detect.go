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
// purpose: a signal is evidence, not proof. A negative weight marks a construct the language
// cannot contain; RE2 has no lookahead, so "matches X but is not Kotlin" is expressed as a
// penalty rather than inside the pattern.
//
// All patterns run on code after stripNonCode, so they never see comment or string contents.
type signal struct {
	pattern *regexp.Regexp
	weight  int
}

// perSignalCap bounds how many matches of one pattern count. Repetition is evidence (three data
// classes say more than one), but a common weak construct must not outvote a rare strong one.
// Setting it to 1 restores presence-only scoring.
const perSignalCap = 3

var signals = map[Language][]signal{
	Kotlin: {
		// Modifiers, generics and extension receivers: `override fun`, `fun <T> List<T>.second(`, `fun String?.orEmpty(`.
		{regexp.MustCompile(`\bfun\s+(<[^>]+>\s*)?([\w.]+(<[^>]*>)?\??\.)?\w+\s*\(`), 4},
		// Test names in backticks: fun `returns empty list`().
		{regexp.MustCompile("\\bfun\\s+`[^`]+`\\s*\\("), 5},
		{regexp.MustCompile(`\bsuspend\s+fun\b`), 5},
		// fun interface 1.4, value class 1.5, data object 1.9.
		{regexp.MustCompile(`\b(data\s+(class|object)|value\s+class|enum\s+class|annotation\s+class|fun\s+interface|companion\s+object)\b`), 5},
		{regexp.MustCompile(`\b(lateinit\s+var|by\s+lazy)\b`), 5},
		{regexp.MustCompile(`\b(expect|actual)\s+(fun|class|val|object|interface)\b`), 5},
		{regexp.MustCompile(`@file:\w+`), 5},
		{regexp.MustCompile(`(?m)^\s*import\s+kotlinx?\.`), 5},
		// object Foo : Bar(). Scala 3's braceless `object Foo:` has nothing after the colon.
		{regexp.MustCompile(`\bobject\s+\w+\s*:[ \t]*\w`), 4},
		// Nullable type: `: String?`.
		{regexp.MustCompile(`:\s*[A-Z][\w.]*(<[^>]*>)?\?[\s=,)]`), 4},
		// Postfix x!!, unlike a prefix double negation.
		{regexp.MustCompile(`[\w)\]]!!`), 4},
		{regexp.MustCompile(`\bwhen\s*(\([^)]*\))?\s*\{`), 3},
		{regexp.MustCompile(`(?m)^\s*is\s+[\w.<>]+\s*->`), 3},
		{regexp.MustCompile(`\?\.(let|also|run|apply|takeIf)\s*\{|\.(also|apply)\s*\{`), 3},
		{regexp.MustCompile(`\b(listOf|mutableListOf|mapOf|mutableMapOf|setOf|arrayOf|emptyList|buildList)\s*[<(]`), 3},
		{regexp.MustCompile(`\binternal\s+(fun|class|val|var|object|interface)\b`), 4},
		{regexp.MustCompile(`\binit\s*\{|\bconstructor\s*\(`), 2},
		// Shared with Scala, so it only separates the pair from Java and Groovy.
		{regexp.MustCompile(`\bval\s+\w+\s*[:=]|\bvar\s+\w+\s*:`), 2},
		// Gradle Kotlin DSL. Groovy DSL may also use parentheses, hence the low weight.
		{regexp.MustCompile(`(?m)^\s*(implementation|api|kapt|ksp|testImplementation)\s*\(\s*"`), 2},
		{regexp.MustCompile(`\bnew\s+[\w.]+\s*[(<\[]`), -4},
		{regexp.MustCompile(`\bdef\s+\w|\bpublic\s+static\s`), -4},
	},
	Java: {
		{regexp.MustCompile(`(?m)^\s*package\s+[\w.]+\s*;`), 4},
		// The semicolon, not the java. prefix: every JVM language imports java.*.
		{regexp.MustCompile(`(?m)^\s*import\s+(static\s+)?[\w.]+(\.\*)?\s*;`), 4},
		// A method with a body. `[^):]` keeps out Kotlin and Scala parameters, which read `name: Type`.
		{regexp.MustCompile(`\b(public|private|protected)\s+((static|final|abstract|synchronized|native)\s+)*(<[^>]+>\s+)?[\w.]+(<[^>]*>)?(\[\])*\s+\w+\s*\([^):]*\)\s*(throws\s+[\w., ]+)?\{`), 3},
		{regexp.MustCompile(`\bstatic\s+void\s+main\s*\(`), 5},
		// Compact source files and instance main methods.
		{regexp.MustCompile(`(?m)^\s*void\s+main\s*\(\s*\)\s*\{`), 5},
		{regexp.MustCompile(`\b(System\.(out|err)|IO)\.print(ln|f)?\s*\(`), 3},
		// Diamond, Java 7.
		{regexp.MustCompile(`\bnew\s+[\w.]+\s*<>\s*\(`), 4},
		{regexp.MustCompile(`\bnew\s+[\w.]+(<[^>]*>)?\s*[(\[{]`), 2},
		{regexp.MustCompile(`@Override\b`), 3},
		{regexp.MustCompile(`\bthrows\s+[\w.]+`), 3},
		{regexp.MustCompile(`\bimplements\s+[\w.]+`), 2},
		// Records, Java 16.
		{regexp.MustCompile(`\brecord\s+\w+\s*(<[^>]*>)?\s*\([^)]*\)\s*(implements\s+[\w.<>, ]+)?\{`), 5},
		// Java 17. `sealed` alone also exists in Kotlin and Scala.
		{regexp.MustCompile(`\bnon-sealed\b|\bpermits\s+[\w.]+`), 5},
		// Switch arrows, Java 14; patterns and guards, Java 21.
		{regexp.MustCompile(`\bcase\s+[^:\n]+->`), 3},
		{regexp.MustCompile(`\byield\s+[^;\n]+;`), 3},
		// Local var, Java 10. The semicolon keeps Kotlin's `var x =` out.
		{regexp.MustCompile(`\bvar\s+\w+\s*=[^;\n]*;`), 3},
		// Pattern matching for instanceof, Java 16.
		{regexp.MustCompile(`\binstanceof\s+[\w.]+(<[^>]*>)?\s+\w+\s*[)&|;]`), 4},
		{regexp.MustCompile(`\b(final\s+)?(int|long|double|float|boolean|char|byte|short|String)(\[\])?\s+\w+\s*[=;]`), 2},
		// `List<Order> paid =`: type before name.
		{regexp.MustCompile(`(?m)^\s*(final\s+)?[A-Z]\w*<[\w<>, ?.]+>\s+\w+\s*=`), 3},
		// `(o -> ...)`. Kotlin lambdas live in braces.
		{regexp.MustCompile(`\(\s*\w+\s*->`), 2},
		{regexp.MustCompile(`\bCollectors\.|\.stream\(\)`), 2},
		{regexp.MustCompile(`\bfun\s+\w|\b(val|def)\s+\w+\s*[:=(]|=>`), -4},
		{regexp.MustCompile(`(?m)^\s*(case\s+class|object)\s+\w`), -3},
		// import or package without a semicolon.
		{regexp.MustCompile(`(?m)^\s*(import|package)\s+[\w.]+(\.\*)?[ \t]*$`), -3},
	},
	Groovy: {
		{regexp.MustCompile(`(?m)^\s*import\s+groovy\.`), 5},
		// Not @ToString, @EqualsAndHashCode or @Immutable: Lombok and Compose have those too.
		{regexp.MustCompile(`@(Grab|Grapes|GrabResolver|CompileStatic|CompileDynamic|TypeChecked|Field|Canonical|TupleConstructor|Memoized)\b`), 5},
		// A def method with a brace body and no Scala-style `x: T` parameters.
		{regexp.MustCompile(`\bdef\s+\w+\s*\([^):]*\)\s*\{`), 4},
		// Spock feature methods: def "adds two numbers"().
		{regexp.MustCompile(`\bdef\s+['"]`), 5},
		{regexp.MustCompile(`(?m)^\s*(given|when|then|expect|where|and|setup|cleanup):\s*$`), 4},
		{regexp.MustCompile(`(?m)^\s*def\s+\w+\s*=`), 2},
		// A call without parentheses.
		{regexp.MustCompile(`\bprintln\s+['"$\w]`), 4},
		// Empty map literal.
		{regexp.MustCompile(`\[\s*:\s*\]`), 5},
		{regexp.MustCompile(`=\s*\[\s*['"\w]+\s*:`), 4},
		{regexp.MustCompile(`\.(each|eachWithIndex|findAll|inject|collectEntries|eachLine|withCloseable|findResult)\s*[({]`), 4},
		// =~ and ==~.
		{regexp.MustCompile(`=~`), 4},
		{regexp.MustCompile(`~/[^/\n]+/`), 4},
		{regexp.MustCompile(`'''`), 4},
		// Spread: people*.name.
		{regexp.MustCompile(`\w\*\.[A-Za-z_]`), 4},
		// Groovy 3.
		{regexp.MustCompile(`!instanceof\b|\w\s*\?=\s`), 5},
		{regexp.MustCompile(`<=>`), 3},
		// Gradle Groovy DSL.
		{regexp.MustCompile(`(?m)^\s*(implementation|api|compileOnly|runtimeOnly|testImplementation|classpath)\s+['"]`), 5},
		{regexp.MustCompile(`\bapply\s+plugin\s*:`), 5},
		// Jenkinsfile.
		{regexp.MustCompile(`(?m)^\s*pipeline\s*\{|\bstage\s*\(\s*['"]`), 4},
		{regexp.MustCompile(`\bfun\s+\w|\bval\s+\w+\s*[:=]|=>|\blazy\s+val\b`), -4},
	},
	Scala: {
		{regexp.MustCompile(`(?m)^\s*import\s+scala\.`), 5},
		// import a._ and import a.{B, C}.
		{regexp.MustCompile(`(?m)^\s*import\s+[\w.]+\.(_|\{)`), 5},
		// def f(x: A)(using C): T =
		{regexp.MustCompile(`\bdef\s+\w+(\[[^\]]*\])?(\s*\([^)]*\))*\s*:\s*[\w.\[\], ?]+\s*=`), 5},
		{regexp.MustCompile(`\bdef\s+\w+(\[[^\]]*\])?\s*\(\s*(using\s+|implicit\s+)?\w+\s*:`), 4},
		{regexp.MustCompile(`\b(case\s+(class|object)|sealed\s+trait|lazy\s+val)\b`), 5},
		{regexp.MustCompile(`\bobject\s+\w+\s+extends\b|\bextends\s+App\b|@main\s+def\b`), 5},
		// Scala 3 braceless definitions.
		{regexp.MustCompile(`(?m)^\s*(object|class|trait|enum)\s+\w+[^\n{]*:[ \t]*$`), 4},
		{regexp.MustCompile(`\bgiven\s+\w|\busing\s+\w+\s*:|\(\s*using\s|\bimplicit\s+(val|def|class|object)\b|\(\s*implicit\s`), 4},
		// Scala 3.
		{regexp.MustCompile(`\bextension\s*(\[[^\]]*\]\s*)?\(`), 5},
		{regexp.MustCompile(`\b(opaque\s+type|derives)\b`), 5},
		{regexp.MustCompile(`(?m)\bmatch\s*\{|\bmatch[ \t]*$`), 4},
		{regexp.MustCompile(`(?m)^\s*case\s+[^\n]*=>`), 4},
		// For comprehension.
		{regexp.MustCompile(`\w+\s+<-\s+\w`), 4},
		// Type arguments in square brackets: `: List[Int]`.
		{regexp.MustCompile(`:\s*[A-Z]\w*\[[A-Z?_]`), 4},
		// Scala 3 end markers and if-then.
		{regexp.MustCompile(`(?m)^\s*end\s+\w+[ \t]*$`), 3},
		{regexp.MustCompile(`\bif\s+[^\n{]+\s+then\b`), 4},
		// Groovy 2.3+ has traits too.
		{regexp.MustCompile(`\btrait\s+\w+`), 2},
		{regexp.MustCompile(`=>\s`), 2},
		// sbt.
		{regexp.MustCompile(`\blibraryDependencies\s*\+\+?=|\s%%\s`), 5},
		{regexp.MustCompile(`\bfun\s+\w|\bnew\s+[\w.]+\s*<>`), -4},
	},
}

var (
	// A shebang names the runtime outright. Checked before stripping, like the directive below.
	shebang = regexp.MustCompile(`\A#!.*\b(groovy|kotlinc?|scala|java)\b`)
	// scala-cli directives live in comments, so stripping would erase them.
	scalaCLIDirective = regexp.MustCompile(`(?m)^//>\s*using\s`)
)

// Source guesses from code alone. It never returns an error: an unrecognized snippet is a Guess
// with Unknown and Confident false, which the keyboard shows as "pick a language".
func Source(code string) Guess {
	if m := shebang.FindStringSubmatch(code); m != nil {
		switch m[1] {
		case "groovy":
			return Guess{Language: Groovy, Confident: true}
		case "kotlin", "kotlinc":
			return Guess{Language: Kotlin, Confident: true}
		case "scala":
			return Guess{Language: Scala, Confident: true}
		case "java":
			return Guess{Language: Java, Confident: true}
		}
	}
	if scalaCLIDirective.MatchString(code) {
		return Guess{Language: Scala, Confident: true}
	}

	code = stripNonCode(code)

	scores := make(map[Language]int, len(signals))
	for language, patterns := range signals {
		for _, s := range patterns {
			scores[language] += s.weight * len(s.pattern.FindAllStringIndex(code, perSignalCap))
		}
	}
	scoreSemicolons(code, scores)

	best, second, winner := 0, 0, Unknown
	// Iterating a map is unordered, so ties are broken by a fixed preference rather than by luck.
	// Java precedes Groovy: Groovy accepts almost any Java, so without a Groovy-only marker the
	// code is more likely Java.
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

// scoreSemicolons looks at the file as a whole, which no single pattern can: Java requires
// semicolons, Kotlin and Scala almost never have them, Groovy may go either way. Snippets under
// five lines say too little to count.
func scoreSemicolons(code string, scores map[Language]int) {
	lines, terminated := 0, 0
	for _, line := range strings.Split(code, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "{" || trimmed == "}" {
			continue
		}
		lines++
		if strings.HasSuffix(trimmed, ";") {
			terminated++
		}
	}
	if lines < 5 {
		return
	}
	switch share := float64(terminated) / float64(lines); {
	case share > 0.4:
		scores[Java] += 4
		scores[Kotlin] -= 4
		scores[Scala] -= 4
	case share < 0.05:
		scores[Java] -= 4
	}
}

// stripNonCode blanks out comments and the contents of string literals so that prose,
// commented-out code and string data cannot outvote the code itself. Blanking rather than
// removing keeps line breaks, which the (?m)^ patterns and the semicolon count depend on, and
// keeps the quotes themselves, which `println "..."`, `”'` and Spock's `def "..."` rely on.
//
// Strings have to be tracked for comments to be found correctly: a "/*" inside a literal, such as
// @WebFilter("/*"), would otherwise open a comment that swallows the rest of the file.
func stripNonCode(code string) string {
	out := []byte(code)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}

	n := len(code)
	for i := 0; i < n; {
		switch {
		case strings.HasPrefix(code[i:], "//"):
			end := indexFrom(code, i, "\n")
			blank(i, end)
			i = end
		case strings.HasPrefix(code[i:], "/*"):
			// Kotlin and Scala allow nested block comments.
			depth, j := 1, i+2
			for j < n && depth > 0 {
				switch {
				case strings.HasPrefix(code[j:], "/*"):
					depth++
					j += 2
				case strings.HasPrefix(code[j:], "*/"):
					depth--
					j += 2
				default:
					j++
				}
			}
			blank(i, j)
			i = j
		case strings.HasPrefix(code[i:], `"""`), strings.HasPrefix(code[i:], "'''"):
			quote := code[i : i+3]
			end := indexFrom(code, i+3, quote)
			blank(i+3, end)
			i = end + 3
			if i > n {
				i = n
			}
		case code[i] == '"' || code[i] == '\'':
			quote, j := code[i], i+1
			for j < n && code[j] != quote && code[j] != '\n' {
				if code[j] == '\\' {
					j++
				}
				j++
			}
			if j < n && code[j] == quote {
				blank(i+1, j)
				i = j + 1
			} else {
				// No closing quote on the line: a Scala 2 symbol like 'foo, not a string.
				i++
			}
		default:
			i++
		}
	}
	return string(out)
}

// indexFrom is strings.Index starting at from, returning len(s) when sub is absent.
func indexFrom(s string, from int, sub string) int {
	if k := strings.Index(s[from:], sub); k >= 0 {
		return from + k
	}
	return len(s)
}
