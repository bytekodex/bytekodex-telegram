package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

// A filename arrives from a stranger, so it must not be able to point outside the scratch
// directory. This is the one thing in the package worth testing without a container runtime.
func TestSourceNamesCannotEscapeTheScratchDirectory(t *testing.T) {
	scratch := t.TempDir()

	names, err := writeSources(scratch, []session.File{
		{Name: "../../etc/passwd", Content: "x"},
		{Name: "/absolute/Main.java", Content: "y"},
		{Name: "sub/dir/Ok.java", Content: "z"},
	})
	if err != nil {
		t.Fatalf("writeSources: %v", err)
	}

	for _, name := range names {
		if strings.ContainsAny(name, "/\\") {
			t.Errorf("name %q still carries a path", name)
		}
		if _, err := os.Stat(filepath.Join(scratch, name)); err != nil {
			t.Errorf("file %q was not written: %v", name, err)
		}
	}

	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Errorf("wrote %d entries for %d files", len(entries), len(names))
	}
}

func TestArgsLockTheContainerDown(t *testing.T) {
	c := &Container{Limits: DefaultLimits(), DepsVolume: "/var/lib/bytekodex/deps"}
	args := strings.Join(c.args("/tmp/scratch", toolchain.Release{Image: "img"}, []string{"javac"}), " ")

	for _, required := range []string{
		"--network none",
		"--read-only",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--pids-limit 256",
		"--user 1000:1000",
		"/var/lib/bytekodex/deps:/deps:ro",
	} {
		if !strings.Contains(args, required) {
			t.Errorf("missing %q in: %s", required, args)
		}
	}
}

func TestATargetTheCompilerCannotEmitIsRefusedBeforeRunning(t *testing.T) {
	chain, ok := toolchain.For(detect.Java)
	if !ok {
		t.Fatal("no java toolchain")
	}
	release, ok := chain.Release("java8")
	if !ok {
		t.Fatal("no java8 release")
	}

	// Runtime is deliberately a command that does not exist: reaching it would be the failure.
	c := &Container{Runtime: "definitely-not-a-real-runtime", Limits: DefaultLimits()}
	_, err := c.Compile(context.Background(), chain, release, "25", []session.File{
		{Name: "Main.java", Content: "class Main {}"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot target") {
		t.Errorf("err = %v, want a refusal before the container starts", err)
	}
}

func TestClasspathIsAddedForKotlinAndOmittedForJava(t *testing.T) {
	kotlin, _ := toolchain.For(detect.Kotlin)
	if got := kotlin.ClasspathArg("/deps"); !strings.Contains(got, "coroutines") {
		t.Errorf("kotlin classpath = %q, want coroutines on it", got)
	}

	java, _ := toolchain.For(detect.Java)
	if got := java.ClasspathArg("/deps"); got != "" {
		t.Errorf("java classpath = %q, want empty rather than an empty -cp", got)
	}
}

func TestCollectStopsAtTheOutputLimit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"C.class", "A.class", "B.class", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("xxxx"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	artifacts, err := collect(root, Limits{MaxOutputs: 2, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("got %d artifacts, want 2", len(artifacts))
	}
	if artifacts[0].Name != "A.class" || artifacts[1].Name != "B.class" {
		t.Errorf("order is not stable: %q, %q", artifacts[0].Name, artifacts[1].Name)
	}
}
