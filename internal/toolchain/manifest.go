package toolchain

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Manifest is the hand-written half: which versions we offer, and the few facts about them that
// no API knows. Everything else is resolved.
type Manifest struct {
	JDK    JDKSection      `json:"jdk"`
	Kotlin VersionSection  `json:"kotlin"`
	Groovy VersionSection  `json:"groovy"`
	Note   json.RawMessage `json:"comment,omitempty"`
}

// JDKSection describes which builds to look for. The libc filter is not optional: foojay happily
// returns musl builds for linux/x64, and those do not run on a glibc base image.
type JDKSection struct {
	OperatingSystem string `json:"operating_system"`
	// Architectures are resolved independently, because a JDK tarball is per-architecture and the
	// same image has to build on an x64 server and an arm64 laptop.
	Architectures []string     `json:"architectures"`
	Libc          string       `json:"libc"`
	ArchiveType   string       `json:"archive_type"`
	Distributions []string     `json:"distributions"`
	Versions      []JDKRequest `json:"versions"`
}

// JDKRequest is one JDK we want.
type JDKRequest struct {
	Major int `json:"major"`
	// ReleaseFlag is false for JDK 7 and 8, where javac predates --release and needs
	// -source/-target instead. Defaults to true.
	ReleaseFlag *bool `json:"release_flag,omitempty"`
	// ReleaseFloor is the oldest bytecode level this javac still emits. JEP 182 retires old
	// values over time, so this is per version rather than a constant.
	ReleaseFloor int `json:"release_floor"`
	// AllowEarlyAccess permits an EA build. JDK 27 and 28 have no GA builds at all yet.
	AllowEarlyAccess bool `json:"allow_early_access,omitempty"`
	// Preview enables --enable-preview. javac only accepts it when the target equals the
	// compiler's own version, so it applies to the newest JDK and nothing else.
	Preview bool `json:"preview,omitempty"`
}

// UsesReleaseFlag reports whether javac in this JDK understands --release.
func (r JDKRequest) UsesReleaseFlag() bool { return r.ReleaseFlag == nil || *r.ReleaseFlag }

// VersionSection is the shape shared by Kotlin and Groovy: a plain list of versions.
type VersionSection struct {
	Versions []VersionRequest `json:"versions"`
}

// VersionRequest is one compiler release.
type VersionRequest struct {
	Version string `json:"version"`
	// JVMTargetMax is the newest bytecode level this compiler can emit.
	JVMTargetMax string `json:"jvm_target_max"`
	// JDK is the runtime the compiler itself runs on.
	JDK int `json:"jdk"`
	// Default marks the release preselected in the keyboard.
	Default bool `json:"default,omitempty"`
}

// Line is the minor version line, "2.4" for "2.4.20". Two patches of one line are never offered
// at once, so the line is what identifies a release to a user.
func (r VersionRequest) Line() string {
	parts := strings.Split(r.Version, ".")
	if len(parts) < 2 {
		return r.Version
	}
	return parts[0] + "." + parts[1]
}

// Lock is the generated half: exact URLs, checksums and the versions they turned out to be.
// It is committed, so a build is reproducible and a silently republished archive is caught.
type Lock struct {
	GeneratedAt time.Time    `json:"generated_at"`
	JDK         []LockedJDK  `json:"jdk"`
	Kotlin      []LockedTool `json:"kotlin"`
	Groovy      []LockedTool `json:"groovy"`
}

// LockedJDK is a resolved JDK download, for one version on one architecture.
type LockedJDK struct {
	Major         int    `json:"major"`
	Architecture  string `json:"architecture"`
	Distribution  string `json:"distribution"`
	JavaVersion   string `json:"java_version"`
	ReleaseStatus string `json:"release_status"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	Filename      string `json:"filename"`
	Size          int64  `json:"size"`

	ReleaseFloor int  `json:"release_floor"`
	ReleaseFlag  bool `json:"release_flag"`
	Preview      bool `json:"preview,omitempty"`
}

// LockedTool is a resolved Kotlin or Groovy download.
type LockedTool struct {
	Version      string `json:"version"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	JVMTargetMax string `json:"jvm_target_max"`
	JDK          int    `json:"jdk"`
	Default      bool   `json:"default,omitempty"`
}

// Image is the tag a locked entry is built as. Kept here so the resolver, the Docker plan and
// the runtime catalog cannot disagree about it.
const ImagePrefix = "ghcr.io/bytekodex"

func (l LockedJDK) Image() string { return fmt.Sprintf("%s/jdk:%d", ImagePrefix, l.Major) }
func (l LockedTool) Image(language string) string {
	return fmt.Sprintf("%s/%s:%s", ImagePrefix, language, l.Version)
}

// LoadManifest reads the hand-written manifest.
func LoadManifest(path string) (*Manifest, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("toolchain: reading the manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(blob, &manifest); err != nil {
		return nil, fmt.Errorf("toolchain: parsing the manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *Manifest) validate() error {
	if len(m.JDK.Versions) == 0 {
		return fmt.Errorf("toolchain: the manifest offers no JDK")
	}
	if m.JDK.Libc == "" || len(m.JDK.Architectures) == 0 || m.JDK.OperatingSystem == "" {
		return fmt.Errorf("toolchain: the JDK section needs operating_system, architectures and libc")
	}

	majors := make(map[int]bool, len(m.JDK.Versions))
	for _, jdk := range m.JDK.Versions {
		if majors[jdk.Major] {
			return fmt.Errorf("toolchain: JDK %d is listed twice", jdk.Major)
		}
		majors[jdk.Major] = true
	}

	// A compiler pinned to a JDK we do not build would produce an image that cannot be built,
	// and the failure would surface as a confusing Docker error much later.
	for language, section := range map[string]VersionSection{"kotlin": m.Kotlin, "groovy": m.Groovy} {
		lines := make(map[string]string, len(section.Versions))
		for _, tool := range section.Versions {
			if !majors[tool.JDK] {
				return fmt.Errorf("toolchain: %s %s wants JDK %d, which the manifest does not list",
					language, tool.Version, tool.JDK)
			}
			if previous, clash := lines[tool.Line()]; clash {
				return fmt.Errorf("toolchain: %s has two releases on line %s: %s and %s",
					language, tool.Line(), previous, tool.Version)
			}
			lines[tool.Line()] = tool.Version
		}
	}
	return nil
}

// LoadLock reads the generated lock.
func LoadLock(path string) (*Lock, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("toolchain: reading the lock: %w", err)
	}
	var lock Lock
	if err := json.Unmarshal(blob, &lock); err != nil {
		return nil, fmt.Errorf("toolchain: parsing the lock: %w", err)
	}
	return &lock, nil
}

// Save writes the lock, indented, because it is reviewed in diffs like any other source.
func (l *Lock) Save(path string) error {
	blob, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(blob, '\n'), 0o644)
}

// targets expands a floor and a ceiling into the bytecode levels a compiler offers, newest
// first. Java numbering has a seam at 9: before it the levels were spelled 1.6 and 1.8.
func targets(floor int, ceiling string) []string {
	top, err := parseLevel(ceiling)
	if err != nil || top < floor {
		return []string{ceiling}
	}

	var out []string
	for level := top; level >= floor; level-- {
		if level >= 9 {
			out = append(out, strconv.Itoa(level))
			continue
		}
		out = append(out, "1."+strconv.Itoa(level))
	}
	return out
}

// parseLevel reads both spellings of a bytecode level: "1.8" and "8" are the same thing.
func parseLevel(level string) (int, error) {
	return strconv.Atoi(strings.TrimPrefix(level, "1."))
}
