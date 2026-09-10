// Command toolchainctl resolves the toolchain manifest into a lock, and turns that lock into a
// plan of image builds.
//
// It runs when a human adds a version, never while serving a request. The lock it writes is
// committed, so a build is reproducible and an archive that was quietly republished under the
// same name shows up as a checksum mismatch instead of a mystery.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

const usage = `usage: toolchainctl <command> [flags]

commands:
  resolve   read the manifest, look every download up, write the lock
  plan      print the docker build commands the lock implies
  show      summarize the lock as a table

flags:
  -dir string   directory holding manifest.json and lock.json (default "toolchains")
  -quiet        do not report progress while resolving
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := flags.String("dir", "toolchains", "directory holding manifest.json and lock.json")
	quiet := flags.Bool("quiet", false, "do not report progress while resolving")
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	manifestPath := filepath.Join(*dir, "manifest.json")
	lockPath := filepath.Join(*dir, "lock.json")

	var err error
	switch os.Args[1] {
	case "resolve":
		err = resolve(manifestPath, lockPath, *quiet)
	case "plan":
		err = plan(lockPath)
	case "show":
		err = show(lockPath)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "toolchainctl:", err)
		os.Exit(1)
	}
}

func resolve(manifestPath, lockPath string, quiet bool) error {
	manifest, err := toolchain.LoadManifest(manifestPath)
	if err != nil {
		return err
	}

	// Resolving hashes archives that publish no checksum, which takes minutes over a slow link,
	// so interrupting has to actually stop it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	resolver := toolchain.NewResolver()
	if !quiet {
		resolver.Log = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
		}
	}

	lock, err := resolver.Resolve(ctx, manifest)
	if err != nil {
		return err
	}
	if err := lock.Save(lockPath); err != nil {
		return err
	}

	fmt.Printf("%s: %d JDK, %d Kotlin, %d Groovy\n", lockPath, len(lock.JDK), len(lock.Kotlin), len(lock.Groovy))
	return nil
}

// plan prints build commands rather than running them, so the same output serves a human piping
// it to a shell and a CI job consuming it line by line. Nothing here shells out to docker.
func plan(lockPath string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}

	// JDK images first: every compiler image is built on top of one.
	for _, jdk := range lock.SortedJDK() {
		fmt.Printf("docker build -f toolchains/Dockerfile.jdk -t %s \\\n", jdk.Image())
		fmt.Printf("  --build-arg URL=%s \\\n", jdk.URL)
		fmt.Printf("  --build-arg SHA256=%s \\\n", jdk.SHA256)
		fmt.Printf("  toolchains\n")
	}

	for _, entry := range lock.Kotlin {
		printToolBuild("kotlin", entry, lock)
	}
	for _, entry := range lock.Groovy {
		printToolBuild("groovy", entry, lock)
	}
	return nil
}

func printToolBuild(language string, tool toolchain.LockedTool, lock *toolchain.Lock) {
	base := ""
	for _, jdk := range lock.JDK {
		if jdk.Major == tool.JDK {
			base = jdk.Image()
		}
	}

	fmt.Printf("docker build -f toolchains/Dockerfile.%s -t %s \\\n", language, tool.Image(language))
	fmt.Printf("  --build-arg BASE=%s \\\n", base)
	fmt.Printf("  --build-arg URL=%s \\\n", tool.URL)
	fmt.Printf("  --build-arg SHA256=%s \\\n", tool.SHA256)
	fmt.Printf("  toolchains\n")
}

func show(lockPath string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}

	fmt.Printf("resolved %s\n\n", lock.GeneratedAt.Format("2006-01-02 15:04 MST"))

	fmt.Println("JDK")
	for _, jdk := range lock.SortedJDK() {
		flag := "--release"
		if !jdk.ReleaseFlag {
			flag = "-source/-target"
		}
		fmt.Printf("  %-3d %-16s %-18s %-3s %s, floor %d\n",
			jdk.Major, jdk.Distribution, jdk.JavaVersion, jdk.ReleaseStatus, flag, jdk.ReleaseFloor)
	}

	for name, entries := range map[string][]toolchain.LockedTool{"Kotlin": lock.Kotlin, "Groovy": lock.Groovy} {
		fmt.Printf("\n%s\n", name)
		for _, entry := range entries {
			fmt.Printf("  %-8s on JDK %-3d up to bytecode %s\n", entry.Version, entry.JDK, entry.JVMTargetMax)
		}
	}
	return nil
}
