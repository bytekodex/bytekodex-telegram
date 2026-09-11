// Package toolchain describes what the bot can compile with, and how.
//
// Nothing here is hardcoded. The offered versions come from toolchains/manifest.json, their exact
// downloads from toolchains/lock.json, and this file only turns the lock into something a keyboard
// and a compiler invocation can use. Adding Kotlin 2.5 is an edit to the manifest, not to Go.
//
// Every entry names a container image rather than a local binary. Compiling a stranger's source is
// running their code — Groovy global AST transformations and javac annotation processors both
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
	// Preview enables the compiler's preview features. For javac this is only legal when the
	// target equals the compiler's own version, which is why it lives per release.
	Preview bool
	// EarlyAccess marks a build that is not a final release, so the caption can say so.
	EarlyAccess bool
	// Major is the JDK this release is, for Java, or runs on, for the others. The system class
	// store is keyed on it.
	Major int
	// Classpath lists the dependencies added to every compilation of this release, relative to
	// the deps mount root. It is per-release, not per-language: Kotlin's class metadata is
	// forward-readable only, so no one set of coroutines/stdlib jars could serve every Kotlin
	// version at once. Without it, `suspend fun` fails on the first line that uses it.
	Classpath []string

	// usesReleaseFlag distinguishes javac 9 and up, which takes --release, from 7 and 8, which
	// need -source and -target.
	usesReleaseFlag bool
}

// Toolchain is a language together with its selectable versions.
type Toolchain struct {
	Language detect.Language
	Releases []Release
	// Command builds the compiler invocation for a release and a set of source filenames.
	Command func(release Release, target string, sources []string) []string
}

// Catalog is everything the bot can compile. It is built from a lock and passed around
// explicitly, so there is no global to get out of step with the file on disk.
type Catalog struct {
	chains []Toolchain
}

// FromLock turns a resolved lock into a catalog.
func FromLock(lock *Lock) *Catalog {
	return &Catalog{chains: []Toolchain{
		javaToolchain(lock),
		kotlinToolchain(lock),
		groovyToolchain(lock),
	}}
}

// For returns the toolchain for a language.
func (c *Catalog) For(language detect.Language) (Toolchain, bool) {
	if c == nil {
		return Toolchain{}, false
	}
	i := slices.IndexFunc(c.chains, func(t Toolchain) bool { return t.Language == language })
	if i < 0 || len(c.chains[i].Releases) == 0 {
		return Toolchain{}, false
	}
	return c.chains[i], true
}

// Languages lists what the bot supports, in the order the keyboard shows them. A language whose
// lock section is empty is left out rather than shown as a dead end.
func (c *Catalog) Languages() []detect.Language {
	if c == nil {
		return nil
	}
	out := make([]detect.Language, 0, len(c.chains))
	for _, chain := range c.chains {
		if len(chain.Releases) > 0 {
			out = append(out, chain.Language)
		}
	}
	return out
}

/* ---------- per-language assembly ---------- */

func javaToolchain(lock *Lock) Toolchain {
	majors := lock.Majors()
	releases := make([]Release, 0, len(majors))
	for i, major := range majors {
		// One release per version, not per architecture: which tarball a machine downloads is a
		// build-time detail and has nothing to do with what a user picks.
		jdk, ok := lock.Representative(major)
		if !ok {
			continue
		}
		label := fmt.Sprintf("JDK %d", jdk.Major)
		if jdk.ReleaseStatus == "ea" {
			label += " (early access)"
		}
		releases = append(releases, Release{
			ID:              fmt.Sprintf("java%d", jdk.Major),
			Label:           label,
			Image:           jdk.Image(),
			Targets:         targets(jdk.ReleaseFloor, fmt.Sprint(jdk.Major)),
			Default:         i == 0,
			Preview:         jdk.Preview,
			EarlyAccess:     jdk.ReleaseStatus == "ea",
			Major:           jdk.Major,
			usesReleaseFlag: jdk.ReleaseFlag,
		})
	}

	return Toolchain{
		Language: detect.Java,
		Releases: releases,
		Command: func(release Release, target string, sources []string) []string {
			// -proc:none matters: an annotation processor on the classpath would otherwise run
			// arbitrary code during compilation. -g keeps the local variable table, which is half
			// of what makes the output worth looking at.
			command := []string{"javac", "-g", "-proc:none", "-nowarn"}

			// javac only accepts --enable-preview when the target is its own version, so preview
			// and an older target are mutually exclusive rather than merely unusual.
			preview := release.Preview && strings.TrimPrefix(release.ID, "java") == target
			switch {
			case preview:
				command = append(command, "--release", target, "--enable-preview")
			case release.usesReleaseFlag:
				command = append(command, "--release", target)
			default:
				// javac 7 and 8 predate --release. -source and -target alone do not pin the API,
				// but on those JDKs the API is the right one anyway.
				command = append(command, "-source", target, "-target", target)
			}

			return append(command, append([]string{"-d", "out"}, sources...)...)
		},
	}
}

func kotlinToolchain(lock *Lock) Toolchain {
	releases := make([]Release, 0, len(lock.Kotlin))
	for _, kotlin := range lock.Kotlin {
		classpath := make([]string, len(kotlin.Deps))
		for i, dep := range kotlin.Deps {
			// One subdirectory per Kotlin version: two releases can be locked to the same
			// coroutines version (see manifest.json), but never to the same stdlib, so the path
			// has to be release-specific even where the jar's own content is shared.
			classpath[i] = kotlin.Version + "/" + dep.Name
		}
		releases = append(releases, Release{
			ID:        "kotlin" + kotlin.Version,
			Label:     "Kotlin " + kotlin.Version,
			Image:     kotlin.Image("kotlin"),
			Targets:   targets(8, kotlin.JVMTargetMax),
			Default:   kotlin.Default,
			Major:     kotlin.JDK,
			Classpath: classpath,
		})
	}
	slices.Reverse(releases)

	return Toolchain{
		Language: detect.Kotlin,
		Releases: releases,
		Command: func(_ Release, target string, sources []string) []string {
			// -Xno-optimize keeps the bytecode close to what the source says, which is the point
			// of looking at it. Progressive mode is deliberately off: it changes semantics, and a
			// user who wants it can ask for it through the extra flags.
			return append([]string{
				"kotlinc", "-nowarn", "-Xno-optimize",
				"-jvm-target", target,
				"-d", "out",
			}, sources...)
		},
	}
}

func groovyToolchain(lock *Lock) Toolchain {
	releases := make([]Release, 0, len(lock.Groovy))
	for _, groovy := range lock.Groovy {
		releases = append(releases, Release{
			ID:      "groovy" + groovy.Version,
			Label:   "Groovy " + groovy.Version,
			Image:   groovy.Image("groovy"),
			Targets: targets(8, groovy.JVMTargetMax),
			Default: groovy.Default,
			Major:   groovy.JDK,
		})
	}
	slices.Reverse(releases)

	return Toolchain{
		Language: detect.Groovy,
		Releases: releases,
		Command: func(_ Release, target string, sources []string) []string {
			return append([]string{
				"groovyc", "--compile-static=false",
				"-j", "-target", target,
				"-d", "out",
			}, sources...)
		},
	}
}

/* ---------- lookups ---------- */

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
func (r Release) ClasspathArg(root string) string {
	if len(r.Classpath) == 0 {
		return ""
	}
	entries := make([]string, len(r.Classpath))
	for i, jar := range r.Classpath {
		entries[i] = root + "/" + jar
	}
	return strings.Join(entries, ":")
}
