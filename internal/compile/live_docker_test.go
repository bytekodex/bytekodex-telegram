package compile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

// These run the real thing against a real container runtime, which is the only way to find out
// whether a compiler accepts the flags the catalog builds for it. They need images built by
// `toolchainctl plan`, so they are skipped unless BYTEKODEX_LIVE_DOCKER is set.
func liveOrSkip(t *testing.T) *Container {
	t.Helper()
	if os.Getenv("BYTEKODEX_LIVE_DOCKER") == "" {
		t.Skip("set BYTEKODEX_LIVE_DOCKER=1 and build the images to run this")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker on PATH")
	}
	limits := DefaultLimits()
	limits.Timeout = 90 * time.Second
	return &Container{Runtime: "docker", Limits: limits}
}

func liveCatalog(t *testing.T) *toolchain.Catalog {
	t.Helper()
	lock, err := toolchain.LoadLock("../../toolchains/lock.json")
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	return toolchain.FromLock(lock)
}

func TestLiveJavaCompiles(t *testing.T) {
	container := liveOrSkip(t)
	chain, _ := liveCatalog(t).For(detect.Java)

	release, ok := chain.Release("java25")
	if !ok {
		t.Fatal("no java25 in the catalog")
	}
	source := session.File{
		Name: "Demo.java",
		Content: "import java.util.List;\n\npublic class Demo {\n" +
			"    int total(List<String> words) { return words.stream().mapToInt(String::length).sum(); }\n}\n",
	}

	result, err := container.Compile(context.Background(), chain, release, "25", []session.File{source})
	if err != nil {
		t.Fatalf("Compile: %v\n%s", err, result.Output)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "Demo.class" {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
	if got := result.Artifacts[0].Bytes[:4]; got[0] != 0xCA || got[1] != 0xFE {
		t.Errorf("no class file magic: % x", got)
	}
	t.Logf("Demo.class: %d bytes, output %q", len(result.Artifacts[0].Bytes), result.Output)
}

// The interesting part of an old JDK is that javac had different flags. Java 7 predates --release
// and, unlike every later version, will not create the output directory for itself.
func TestLiveJavaSevenCompiles(t *testing.T) {
	container := liveOrSkip(t)
	chain, _ := liveCatalog(t).For(detect.Java)

	release, ok := chain.Release("java7")
	if !ok {
		t.Fatal("no java7 in the catalog")
	}
	source := session.File{
		Name: "Old.java",
		Content: "import java.util.List;\n\npublic class Old {\n" +
			"    int total(List<String> words) {\n        int sum = 0;\n" +
			"        for (String word : words) { sum += word.length(); }\n        return sum;\n    }\n}\n",
	}

	result, err := container.Compile(context.Background(), chain, release, "1.7", []session.File{source})
	if err != nil {
		t.Fatalf("Compile: %v\n%s", err, result.Output)
	}
	// Major version 51 is Java 7. Getting this wrong is the whole risk of the -source/-target path.
	major := int(result.Artifacts[0].Bytes[6])<<8 | int(result.Artifacts[0].Bytes[7])
	if major != 51 {
		t.Errorf("class file major version %d, want 51", major)
	}
}

func TestLiveKotlinCompiles(t *testing.T) {
	container := liveOrSkip(t)
	chain, _ := liveCatalog(t).For(detect.Kotlin)

	release := chain.DefaultRelease()
	source := session.File{
		Name:    "Snippet.kt",
		Content: "class Snippet {\n    fun total(words: List<String>): Int = words.sumOf { it.length }\n}\n",
	}

	result, err := container.Compile(context.Background(), chain, release, release.DefaultTarget(),
		[]session.File{source})
	if err != nil {
		t.Fatalf("Compile: %v\n%s", err, result.Output)
	}
	if len(result.Artifacts) == 0 {
		t.Fatalf("no classes, output:\n%s", result.Output)
	}
	// kotlinc used to leak a jansi failure into stderr on a noexec tmpfs. Nothing should be there.
	if strings.TrimSpace(result.Output) != "" {
		t.Errorf("compiler said something unexpected:\n%s", result.Output)
	}
	t.Logf("%s: %d bytes", result.Artifacts[0].Name, len(result.Artifacts[0].Bytes))
}

// A compile error has to come back as a message rather than as a crash, because that message is
// what gets rendered for the user.
func TestLiveCompileErrorIsReported(t *testing.T) {
	container := liveOrSkip(t)
	chain, _ := liveCatalog(t).For(detect.Java)
	release, _ := chain.Release("java25")

	source := session.File{Name: "Broken.java", Content: "public class Broken { int x = \"no\"; }\n"}

	_, err := container.Compile(context.Background(), chain, release, "25", []session.File{source})

	var failed *Failed
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want Failed", err)
	}
	if !strings.Contains(failed.Output, "incompatible types") {
		t.Errorf("output does not explain itself:\n%s", failed.Output)
	}
	t.Logf("javac said:\n%s", failed.Output)
}
