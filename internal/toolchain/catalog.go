// Package toolchain describes what the bot can compile with, and how.
//
// Every entry names a container image rather than a local binary. Compiling a stranger's source
// is running their code — Groovy global AST transformations and javac annotation processors both
// execute at compile time — so there is no version of this that is safe on the host.
package toolchain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
)

// Release is one selectable compiler version.
type Release struct {
	// ID is stable and appears in callback data, so it must never be reworded.
	ID string
	// Label is what the user sees.
	Label string
	// Image is the container the compiler runs in.
	Image string
	// Targets are the bytecode levels this compiler can emit, newest first. The first is default.
	Targets []string
	// Default marks the release preselected in the keyboard.
	Default bool
}

// Toolchain is a language together with its selectable versions.
type Toolchain struct {
	Language detect.Language
	Releases []Release
	// Classpath lists the dependencies added to every compilation. Kotlin without coroutines on
	// the classpath fails on the first `suspend fun`, which is most of what people want to look at.
	Classpath []string
	// Command builds the compiler invocation for a release and a set of source filenames.
	Command func(release Release, target string, sources []string) []string
}

// All is the catalog. Versions are pinned rather than floating, so a rebuilt image cannot quietly
// change what a user sees.
var All = []Toolchain{
	{
		Language: detect.Java,
		Releases: []Release{
			{ID: "java25", Label: "JDK 25 (LTS)", Image: "eclipse-temurin:25-jdk", Targets: []string{"25", "21", "17"}, Default: true},
			{ID: "java21", Label: "JDK 21 (LTS)", Image: "eclipse-temurin:21-jdk", Targets: []string{"21", "17", "11"}},
			{ID: "java17", Label: "JDK 17 (LTS)", Image: "eclipse-temurin:17-jdk", Targets: []string{"17", "11", "8"}},
			{ID: "java11", Label: "JDK 11 (LTS)", Image: "eclipse-temurin:11-jdk", Targets: []string{"11", "8"}},
			{ID: "java8", Label: "JDK 8", Image: "eclipse-temurin:8-jdk", Targets: []string{"8"}},
		},
		Command: func(_ Release, target string, sources []string) []string {
			// -proc:none matters: an annotation processor on the classpath would otherwise run
			// arbitrary code during compilation. -g keeps the local variable table, which is half
			// of what makes the output worth looking at.
			return append([]string{
				"javac", "-g", "-proc:none", "-nowarn",
				"--release", target,
				"-d", "out",
			}, sources...)
		},
	},
	{
		Language: detect.Kotlin,
		Classpath: []string{
			"kotlinx-coroutines-core-jvm.jar",
			"kotlin-stdlib.jar",
		},
		Releases: []Release{
			{ID: "kotlin2.2", Label: "Kotlin 2.2", Image: "ghcr.io/bytekodex/kotlin:2.2", Targets: []string{"25", "21", "17"}, Default: true},
			{ID: "kotlin2.1", Label: "Kotlin 2.1", Image: "ghcr.io/bytekodex/kotlin:2.1", Targets: []string{"21", "17", "11"}},
			{ID: "kotlin2.0", Label: "Kotlin 2.0", Image: "ghcr.io/bytekodex/kotlin:2.0", Targets: []string{"21", "17", "8"}},
			{ID: "kotlin1.9", Label: "Kotlin 1.9", Image: "ghcr.io/bytekodex/kotlin:1.9", Targets: []string{"17", "11", "8"}},
		},
		Command: func(_ Release, target string, sources []string) []string {
			return append([]string{
				"kotlinc", "-nowarn", "-Xno-optimize",
				"-jvm-target", target,
				"-d", "out",
			}, sources...)
		},
	},
	{
		Language: detect.Groovy,
		Releases: []Release{
			{ID: "groovy4", Label: "Groovy 4.0", Image: "groovy:4.0-jdk21", Targets: []string{"21", "17", "11"}, Default: true},
			{ID: "groovy3", Label: "Groovy 3.0", Image: "groovy:3.0-jdk17", Targets: []string{"17", "11", "8"}},
		},
		Command: func(_ Release, target string, sources []string) []string {
			return append([]string{
				"groovyc", "--compile-static=false",
				"-j", "-target", target,
				"-d", "out",
			}, sources...)
		},
	},
	{
		Language: detect.Scala,
		Releases: []Release{
			{ID: "scala3", Label: "Scala 3.5", Image: "ghcr.io/bytekodex/scala:3.5", Targets: []string{"21", "17"}, Default: true},
			{ID: "scala2.13", Label: "Scala 2.13", Image: "ghcr.io/bytekodex/scala:2.13", Targets: []string{"17", "11", "8"}},
		},
		Command: func(_ Release, target string, sources []string) []string {
			return append([]string{"scalac", "-release", target, "-d", "out"}, sources...)
		},
	},
}

// For returns the toolchain for a language.
func For(language detect.Language) (Toolchain, bool) {
	i := slices.IndexFunc(All, func(t Toolchain) bool { return t.Language == language })
	if i < 0 {
		return Toolchain{}, false
	}
	return All[i], true
}

// Languages lists what the bot supports, in the order the keyboard shows them.
func Languages() []detect.Language {
	out := make([]detect.Language, 0, len(All))
	for _, t := range All {
		out = append(out, t.Language)
	}
	return out
}

// Release finds a release by its stable ID.
func (t Toolchain) Release(id string) (Release, bool) {
	i := slices.IndexFunc(t.Releases, func(r Release) bool { return r.ID == id })
	if i < 0 {
		return Release{}, false
	}
	return t.Releases[i], true
}

// DefaultRelease is what a user who never opens the version menu gets.
func (t Toolchain) DefaultRelease() Release {
	if i := slices.IndexFunc(t.Releases, func(r Release) bool { return r.Default }); i >= 0 {
		return t.Releases[i]
	}
	if len(t.Releases) > 0 {
		return t.Releases[0]
	}
	return Release{}
}

// Supports reports whether a release can emit a bytecode level.
func (r Release) Supports(target string) bool { return slices.Contains(r.Targets, target) }

// DefaultTarget is the newest level a release emits.
func (r Release) DefaultTarget() string {
	if len(r.Targets) == 0 {
		return ""
	}
	return r.Targets[0]
}

// Describe is the one-line summary shown above the keyboard.
func Describe(language detect.Language, release Release, target string) string {
	return fmt.Sprintf("%s · %s · bytecode %s", language.Display(), release.Label, target)
}

// ClasspathArg joins the dependency list the way the JVM expects. Empty means no -cp at all,
// which is not the same as an empty -cp.
func (t Toolchain) ClasspathArg(root string) string {
	if len(t.Classpath) == 0 {
		return ""
	}
	entries := make([]string, len(t.Classpath))
	for i, jar := range t.Classpath {
		entries[i] = root + "/" + jar
	}
	return strings.Join(entries, ":")
}
