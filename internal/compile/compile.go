// Package compile turns source a stranger sent into class files.
//
// Compiling untrusted source is running untrusted code. Groovy executes global AST
// transformations and @Grab at compile time, javac runs annotation processors it finds on the
// classpath, and the Kotlin compiler loads plugins. So the compiler runs in a container with no
// network, a read-only root, a dropped capability set and a wall clock, and the only thing that
// comes back out is the bytes of whatever class files appeared.
package compile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

// Artifact is one compiled class.
type Artifact struct {
	// Name is the path inside the output directory, so nested packages stay legible.
	Name  string
	Bytes []byte
}

// Result is everything a compilation produced.
type Result struct {
	Artifacts []Artifact
	// Output is the compiler's own stdout and stderr, shown when nothing came out.
	Output string
	Took   time.Duration
}

// Failed reports a compilation that ran but produced nothing.
type Failed struct {
	Output string
}

func (f *Failed) Error() string {
	if f.Output == "" {
		return "compile: the compiler produced neither classes nor a message"
	}
	return "compile: " + firstLines(f.Output, 20)
}

// ErrTimeout is a compilation that ran out of time rather than failing on its own terms.
var ErrTimeout = errors.New("compile: timed out")

// Limits bound what one compilation may consume. They are generous enough for real code and
// tight enough that a deliberate compile bomb is only an annoyance.
type Limits struct {
	Timeout    time.Duration
	Memory     string // passed to the runtime, e.g. "512m"
	CPUs       string // e.g. "1.0"
	MaxOutputs int
	MaxBytes   int
}

// DefaultLimits are what the bot runs with.
func DefaultLimits() Limits {
	return Limits{
		Timeout:    45 * time.Second,
		Memory:     "768m",
		CPUs:       "1.5",
		MaxOutputs: 64,
		MaxBytes:   8 << 20,
	}
}

// Compiler is the seam a test replaces. Nothing above this package knows about containers.
type Compiler interface {
	Compile(ctx context.Context, chain toolchain.Toolchain, release toolchain.Release, target string, files []session.File) (Result, error)
}

// Container runs each compilation in a throwaway container.
type Container struct {
	// Runtime is the command used, "docker" or "podman".
	Runtime string
	Limits  Limits
	// DepsVolume holds the jars in Toolchain.Classpath, mounted read-only. Kotlin needs
	// coroutines on the classpath or most interesting snippets do not compile at all.
	DepsVolume string
}

const (
	workDir  = "/work"
	depsDir  = "/deps"
	outputIn = "out"
)

// Compile writes the sources to a scratch directory, mounts it, and reads back what appeared.
// The scratch directory is the container's only writable surface and is removed either way.
func (c *Container) Compile(
	ctx context.Context,
	chain toolchain.Toolchain,
	release toolchain.Release,
	target string,
	files []session.File,
) (Result, error) {
	if len(files) == 0 {
		return Result{}, errors.New("compile: nothing to compile")
	}
	if !release.Supports(target) {
		return Result{}, fmt.Errorf("compile: %s cannot target bytecode %s", release.Label, target)
	}

	scratch, err := os.MkdirTemp("", "bytekodex-*")
	if err != nil {
		return Result{}, fmt.Errorf("compile: scratch directory: %w", err)
	}
	defer os.RemoveAll(scratch)

	names, err := writeSources(scratch, files)
	if err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(filepath.Join(scratch, outputIn), 0o777); err != nil {
		return Result{}, fmt.Errorf("compile: output directory: %w", err)
	}

	limits := c.Limits
	if limits.Timeout == 0 {
		limits = DefaultLimits()
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()

	command := chain.Command(release, target, names)
	// Only name the dependency directory when it is actually mounted. Pointing a classpath at a
	// directory that is not there is not fatal, but every compile then carries a warning about it,
	// and that warning would be shown to whoever sent the snippet.
	if c.DepsVolume != "" {
		if classpath := chain.ClasspathArg(depsDir); classpath != "" {
			command = append(command[:1:1], append([]string{"-cp", classpath}, command[1:]...)...)
		}
	}

	started := time.Now()
	var combined bytes.Buffer
	cmd := exec.CommandContext(ctx, c.runtime(), c.args(scratch, release, command)...)
	cmd.Stdout, cmd.Stderr = &combined, &combined
	runErr := cmd.Run()
	took := time.Since(started)

	if ctx.Err() != nil {
		return Result{}, ErrTimeout
	}

	artifacts, err := collect(filepath.Join(scratch, outputIn), limits)
	if err != nil {
		return Result{}, err
	}
	// A non-zero exit with classes on disk still happens — a warning treated as an error by one
	// compiler, a partial build by another — so what came out matters more than the exit code.
	if len(artifacts) == 0 {
		if runErr != nil || combined.Len() > 0 {
			return Result{}, &Failed{Output: combined.String()}
		}
		return Result{}, &Failed{}
	}

	return Result{Artifacts: artifacts, Output: combined.String(), Took: took}, nil
}

func (c *Container) runtime() string {
	if c.Runtime != "" {
		return c.Runtime
	}
	return "docker"
}

// args builds the container invocation. Each flag here is load-bearing: without the network
// gone, @Grab downloads and runs code; without a read-only root and a private tmpfs, a compiler
// plugin can write anywhere it likes; without the pid cap, a fork bomb takes the host down.
func (c *Container) args(scratch string, release toolchain.Release, command []string) []string {
	limits := c.Limits
	if limits.Memory == "" {
		limits = DefaultLimits()
	}

	args := []string{
		"run", "--rm",
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "256",
		"--memory", limits.Memory,
		"--cpus", limits.CPUs,
		"--user", "1000:1000",
		"--workdir", workDir,
		"--volume", scratch + ":" + workDir + ":rw",
	}
	if c.DepsVolume != "" {
		args = append(args, "--volume", c.DepsVolume+":"+depsDir+":ro")
	}
	args = append(args, release.Image)
	return append(args, command...)
}

// writeSources lays the files out and returns the names to pass the compiler. A user-supplied
// name is not trusted: only its base is kept, so nothing can escape the scratch directory.
func writeSources(scratch string, files []session.File) ([]string, error) {
	names := make([]string, 0, len(files))
	for i, file := range files {
		name := filepath.Base(file.Name)
		if name == "." || name == string(filepath.Separator) || strings.HasPrefix(name, "..") {
			name = fmt.Sprintf("Snippet%d.txt", i)
		}
		if err := os.WriteFile(filepath.Join(scratch, name), []byte(file.Content), 0o644); err != nil {
			return nil, fmt.Errorf("compile: writing %s: %w", name, err)
		}
		names = append(names, name)
	}
	return names, nil
}

// collect reads the class files back, in a stable order so that the same source always produces
// the same pages.
func collect(root string, limits Limits) ([]Artifact, error) {
	var artifacts []Artifact
	total := 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".class") {
			return err
		}
		if len(artifacts) >= limits.MaxOutputs {
			return filepath.SkipAll
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("compile: reading %s: %w", path, err)
		}
		if total += len(bytes); total > limits.MaxBytes {
			return filepath.SkipAll
		}
		name, _ := filepath.Rel(root, path)
		artifacts = append(artifacts, Artifact{Name: name, Bytes: bytes})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	return artifacts, nil
}

func firstLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n")
}
