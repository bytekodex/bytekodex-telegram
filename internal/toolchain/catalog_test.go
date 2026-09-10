package toolchain

import (
	"slices"
	"strings"
	"testing"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
)

func sampleLock() *Lock {
	return &Lock{
		JDK: []LockedJDK{
			{Major: 8, ReleaseFloor: 8, ReleaseFlag: false, ReleaseStatus: "ga"},
			{Major: 25, ReleaseFloor: 8, ReleaseFlag: true, ReleaseStatus: "ga"},
			{Major: 28, ReleaseFloor: 8, ReleaseFlag: true, ReleaseStatus: "ea", Preview: true},
		},
		Kotlin: []LockedTool{
			{Version: "1.2.71", JVMTargetMax: "1.8", JDK: 8},
			{Version: "2.4.20", JVMTargetMax: "25", JDK: 25, Default: true},
		},
		Groovy: []LockedTool{
			{Version: "5.1.2", JVMTargetMax: "25", JDK: 25, Default: true},
		},
	}
}

// Levels below 9 were spelled 1.6 and 1.8, and a keyboard that offered "8" where the compiler
// wants "1.8" would fail at compile time rather than at build time.
func TestTargetsSpanTheNumberingSeam(t *testing.T) {
	cases := []struct {
		floor   int
		ceiling string
		want    []string
	}{
		{8, "1.8", []string{"1.8"}},
		{7, "1.8", []string{"1.8", "1.7"}},
		{8, "11", []string{"11", "10", "9", "1.8"}},
		{21, "23", []string{"23", "22", "21"}},
	}
	for _, c := range cases {
		if got := targets(c.floor, c.ceiling); !slices.Equal(got, c.want) {
			t.Errorf("targets(%d, %q) = %v, want %v", c.floor, c.ceiling, got, c.want)
		}
	}
}

func TestNewestJDKIsTheDefaultAndOrderIsNewestFirst(t *testing.T) {
	chain, ok := FromLock(sampleLock()).For(detect.Java)
	if !ok {
		t.Fatal("no java toolchain")
	}

	if got := chain.Releases[0].ID; got != "java28" {
		t.Errorf("first release is %q, want java28", got)
	}
	if got := chain.DefaultRelease().ID; got != "java28" {
		t.Errorf("default is %q, want java28", got)
	}
	if !strings.Contains(chain.Releases[0].Label, "early access") {
		t.Errorf("an EA build should say so: %q", chain.Releases[0].Label)
	}
}

func TestKotlinIsOfferedNewestFirstWithTheManifestDefault(t *testing.T) {
	chain, ok := FromLock(sampleLock()).For(detect.Kotlin)
	if !ok {
		t.Fatal("no kotlin toolchain")
	}
	if got := chain.Releases[0].Label; got != "Kotlin 2.4.20" {
		t.Errorf("first release is %q, want Kotlin 2.4.20", got)
	}
	if got := chain.DefaultRelease().ID; got != "kotlin2.4.20" {
		t.Errorf("default is %q", got)
	}
	// 1.2 tops out at 1.8, so it must not advertise anything newer.
	old, _ := chain.Release("kotlin1.2.71")
	if old.Supports("21") {
		t.Error("Kotlin 1.2 claims it can emit bytecode 21")
	}
}

// javac grew --release in JDK 9. On 7 and 8 the same request has to become -source/-target, and
// getting this wrong fails inside a container where the error is much harder to read.
func TestJavacFlagsMatchTheCompilerVintage(t *testing.T) {
	chain, _ := FromLock(sampleLock()).For(detect.Java)

	modern, _ := chain.Release("java25")
	command := strings.Join(chain.Command(modern, "17", []string{"A.java"}), " ")
	if !strings.Contains(command, "--release 17") {
		t.Errorf("JDK 25 should use --release: %s", command)
	}

	ancient, _ := chain.Release("java8")
	command = strings.Join(chain.Command(ancient, "8", []string{"A.java"}), " ")
	if strings.Contains(command, "--release") {
		t.Errorf("JDK 8 has no --release: %s", command)
	}
	if !strings.Contains(command, "-source 8 -target 8") {
		t.Errorf("JDK 8 should use -source/-target: %s", command)
	}
}

// javac accepts --enable-preview only when the target equals its own version, so an older target
// must silently drop preview rather than produce an invocation javac rejects.
func TestPreviewOnlyAppliesToTheCompilersOwnVersion(t *testing.T) {
	chain, _ := FromLock(sampleLock()).For(detect.Java)
	preview, _ := chain.Release("java28")

	own := strings.Join(chain.Command(preview, "28", []string{"A.java"}), " ")
	if !strings.Contains(own, "--enable-preview") {
		t.Errorf("targeting 28 on JDK 28 should enable preview: %s", own)
	}

	older := strings.Join(chain.Command(preview, "21", []string{"A.java"}), " ")
	if strings.Contains(older, "--enable-preview") {
		t.Errorf("targeting 21 cannot enable preview: %s", older)
	}
}

func TestEveryJavacInvocationRefusesAnnotationProcessors(t *testing.T) {
	chain, _ := FromLock(sampleLock()).For(detect.Java)
	for _, release := range chain.Releases {
		command := chain.Command(release, release.DefaultTarget(), []string{"A.java"})
		if !slices.Contains(command, "-proc:none") {
			t.Errorf("%s would run annotation processors: %v", release.ID, command)
		}
	}
}

func TestLanguagesWithNoReleasesAreNotOffered(t *testing.T) {
	catalog := FromLock(&Lock{JDK: []LockedJDK{{Major: 25, ReleaseFloor: 8, ReleaseFlag: true}}})

	if got := catalog.Languages(); !slices.Equal(got, []detect.Language{detect.Java}) {
		t.Errorf("Languages() = %v, want just java", got)
	}
	if _, ok := catalog.For(detect.Kotlin); ok {
		t.Error("offered Kotlin with nothing locked")
	}
}

func TestManifestRejectsACompilerPinnedToAMissingJDK(t *testing.T) {
	manifest := &Manifest{
		JDK:    JDKSection{OperatingSystem: "linux", Architecture: "x64", Libc: "glibc", Versions: []JDKRequest{{Major: 25}}},
		Kotlin: VersionSection{Versions: []VersionRequest{{Version: "2.4.20", JDK: 17}}},
	}
	err := manifest.validate()
	if err == nil || !strings.Contains(err.Error(), "JDK 17") {
		t.Errorf("err = %v, want a complaint about JDK 17", err)
	}
}

func TestManifestRejectsTwoReleasesOnOneLine(t *testing.T) {
	manifest := &Manifest{
		JDK: JDKSection{OperatingSystem: "linux", Architecture: "x64", Libc: "glibc", Versions: []JDKRequest{{Major: 25}}},
		Kotlin: VersionSection{Versions: []VersionRequest{
			{Version: "2.4.10", JDK: 25},
			{Version: "2.4.20", JDK: 25},
		}},
	}
	err := manifest.validate()
	if err == nil || !strings.Contains(err.Error(), "line 2.4") {
		t.Errorf("err = %v, want a complaint about line 2.4", err)
	}
}

// The manifest that ships is the one users get, so it has to be loadable and consistent.
func TestShippedManifestIsValid(t *testing.T) {
	manifest, err := LoadManifest("../../toolchains/manifest.json")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Kotlin.Versions) < 10 {
		t.Errorf("only %d Kotlin versions offered", len(manifest.Kotlin.Versions))
	}
	// JDK 7 and 8 predate --release, and every later one has it.
	for _, jdk := range manifest.JDK.Versions {
		wantRelease := jdk.Major >= 9
		if jdk.UsesReleaseFlag() != wantRelease {
			t.Errorf("JDK %d: UsesReleaseFlag() = %v, want %v", jdk.Major, jdk.UsesReleaseFlag(), wantRelease)
		}
	}
}

// And the lock that ships has to describe every version the manifest asks for.
func TestShippedLockCoversTheManifest(t *testing.T) {
	manifest, err := LoadManifest("../../toolchains/manifest.json")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	lock, err := LoadLock("../../toolchains/lock.json")
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}

	if len(lock.JDK) != len(manifest.JDK.Versions) {
		t.Errorf("lock has %d JDKs, manifest asks for %d", len(lock.JDK), len(manifest.JDK.Versions))
	}
	for _, jdk := range lock.JDK {
		if len(jdk.SHA256) != 64 {
			t.Errorf("JDK %d has no usable checksum: %q", jdk.Major, jdk.SHA256)
		}
		if jdk.URL == "" {
			t.Errorf("JDK %d has no URL", jdk.Major)
		}
	}
	for _, tool := range append(slices.Clone(lock.Kotlin), lock.Groovy...) {
		if len(tool.SHA256) != 64 {
			t.Errorf("%s has no usable checksum: %q", tool.Version, tool.SHA256)
		}
	}

	// Both defaults have to exist, or the keyboard silently preselects the oldest compiler.
	catalog := FromLock(lock)
	for _, language := range catalog.Languages() {
		chain, _ := catalog.For(language)
		if chain.DefaultRelease().ID == "" {
			t.Errorf("%s has no default release", language)
		}
	}
}
