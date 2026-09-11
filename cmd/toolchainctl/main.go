// Command toolchainctl resolves the toolchain manifest into a lock, and turns that lock into a
// plan of image builds.
//
// It runs when a human adds a version, never while serving a request. The lock it writes is
// committed, so a build is reproducible and an archive that was quietly republished under the
// same name shows up as a checksum mismatch instead of a mystery.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

const usage = `usage: toolchainctl <command> [flags]

commands:
  resolve   read the manifest, look every download up, write the lock
  plan      print the docker build commands the lock implies
  show      summarize the lock as a table
  probe     ask the built images what their compilers actually support
  majors    print one locked JDK major per line, newest first
  deps      print version, name, url and sha256 for every Kotlin classpath jar, tab-separated

flags:
  -dir string        directory holding manifest.json and lock.json (default "toolchains")
  -quiet             do not report progress while resolving
  -platform string   plan: restrict to one architecture (amd64 or arm64), skipping versions that
                      have nothing resolved for it
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := flags.String("dir", "toolchains", "directory holding manifest.json and lock.json")
	quiet := flags.Bool("quiet", false, "do not report progress while resolving")
	platform := flags.String("platform", "", "restrict plan to one Docker architecture (amd64 or arm64); empty means every architecture the lock has")
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
		err = plan(lockPath, *platform)
	case "show":
		err = show(lockPath)
	case "majors":
		err = printMajors(lockPath)
	case "deps":
		err = printDeps(lockPath)
	case "probe":
		err = probe(lockPath)
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
//
// platform, when set, restricts every image to one architecture instead of the multi-arch build a
// bare laptop would want. A production server only ever runs one architecture, and cross-building
// the other one there means paying for QEMU emulation on every image, for an artifact nothing will
// ever pull. A version with nothing resolved for the requested architecture — JDK 7 has no aarch64
// build anywhere — is skipped rather than failing the whole plan.
func plan(lockPath, platform string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}

	// JDK images first: every compiler image is built on top of one.
	for _, major := range lock.Majors() {
		entries := lock.Architectures(major)
		if platform != "" {
			entries = filterArch(entries, platform)
			if len(entries) == 0 {
				continue
			}
		}
		representative := entries[0]

		// One image per version, carrying every architecture it was resolved for. The Dockerfile
		// picks by TARGETARCH, so the same command works on an x64 server and an arm64 laptop, and
		// with buildx it produces one multi-architecture image.
		platforms := make([]string, 0, len(entries))
		for _, entry := range entries {
			platforms = append(platforms, "linux/"+dockerArch(entry.Architecture))
		}

		fmt.Printf("docker build -f toolchains/Dockerfile.jdk -t %s \\\n", representative.Image())
		fmt.Printf("  --platform %s \\\n", strings.Join(platforms, ","))
		for _, entry := range entries {
			suffix := strings.ToUpper(dockerArch(entry.Architecture))
			fmt.Printf("  --build-arg URL_%s=%s \\\n", suffix, entry.URL)
			fmt.Printf("  --build-arg SHA256_%s=%s \\\n", suffix, entry.SHA256)
		}
		fmt.Printf("  toolchains\n")

		// The system classes come out of the JDK image, so they are planned next to it.
		fmt.Printf("docker build -f toolchains/Dockerfile.sysclasses -t %s \\\n", sysclassImage(major))
		if platform != "" {
			fmt.Printf("  --platform linux/%s \\\n", dockerArch(platform))
		}
		fmt.Printf("  --build-arg BASE=%s \\\n", representative.Image())
		fmt.Printf("  toolchains\n")
	}

	for _, entry := range lock.Kotlin {
		printToolBuild("kotlin", entry, lock, platform)
	}
	for _, entry := range lock.Groovy {
		printToolBuild("groovy", entry, lock, platform)
	}
	return nil
}

// filterArch keeps only the entries whose architecture matches, under whichever spelling was used
// to resolve them.
func filterArch(entries []toolchain.LockedJDK, platform string) []toolchain.LockedJDK {
	var out []toolchain.LockedJDK
	for _, entry := range entries {
		if dockerArch(entry.Architecture) == dockerArch(platform) {
			out = append(out, entry)
		}
	}
	return out
}

// dockerArch translates foojay's architecture names into Docker's.
func dockerArch(architecture string) string {
	switch architecture {
	case "x64", "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return architecture
	}
}

// printMajors is deliberately plain text, one integer per line: it exists so a deploy script can
// loop over `docker pull ghcr.io/bytekodex/sysclasses:$major` without a JSON parser on the host.
func printMajors(lockPath string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}
	for _, major := range lock.Majors() {
		fmt.Println(major)
	}
	return nil
}

// printDeps is tab-separated on purpose, same reasoning as printMajors: a shell loop, not a JSON
// parser, is what toolchains/sync-deps.sh wants to read this with.
func printDeps(lockPath string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}
	for _, kotlin := range lock.Kotlin {
		for _, dep := range kotlin.Deps {
			fmt.Printf("%s\t%s\t%s\t%s\n", kotlin.Version, dep.Name, dep.URL, dep.SHA256)
		}
	}
	return nil
}

func sysclassImage(major int) string {
	return fmt.Sprintf("%s/sysclasses:%d", toolchain.ImagePrefix, major)
}

func printToolBuild(language string, tool toolchain.LockedTool, lock *toolchain.Lock, platform string) {
	base := ""
	if representative, ok := lock.Representative(tool.JDK); ok {
		base = representative.Image()
	}

	fmt.Printf("docker build -f toolchains/Dockerfile.%s -t %s \\\n", language, tool.Image(language))
	if platform != "" {
		fmt.Printf("  --platform linux/%s \\\n", dockerArch(platform))
	}
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
	for _, major := range lock.Majors() {
		entries := lock.Architectures(major)
		jdk := entries[0]

		flag := "--release"
		if !jdk.ReleaseFlag {
			flag = "-source/-target"
		}
		arches := make([]string, 0, len(entries))
		for _, entry := range entries {
			arches = append(arches, entry.Architecture)
		}
		fmt.Printf("  %-3d %-16s %-18s %-3s %-14s floor %-2d %s\n",
			major, jdk.Distribution, jdk.JavaVersion, jdk.ReleaseStatus, flag, jdk.ReleaseFloor,
			strings.Join(arches, "+"))
	}

	for name, entries := range map[string][]toolchain.LockedTool{"Kotlin": lock.Kotlin, "Groovy": lock.Groovy} {
		fmt.Printf("\n%s\n", name)
		for _, entry := range entries {
			fmt.Printf("  %-8s on JDK %-3d up to bytecode %s\n", entry.Version, entry.JDK, entry.JVMTargetMax)
		}
	}
	return nil
}

// probe asks the compilers themselves what they support, rather than trusting what the manifest
// claims. The manifest has to state a release floor before any image exists, and a wrong floor
// means a button that fails only when someone presses it. Images that were never built are
// skipped, so this is useful with two of them and with all of them.
func probe(lockPath string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}

	runtime := "docker"
	if found := os.Getenv("BYTEKODEX_RUNTIME"); found != "" {
		runtime = found
	}

	disagreements := 0
	skipped := 0
	for _, major := range lock.Majors() {
		entry, _ := lock.Representative(major)

		floor, err := probeFloor(runtime, entry.Image())
		switch {
		case errors.Is(err, errNoImage):
			skipped++
			continue
		case err != nil:
			return fmt.Errorf("jdk %d: %w", major, err)
		}

		mark := "ok"
		if floor != entry.ReleaseFloor {
			mark = fmt.Sprintf("manifest says %d", entry.ReleaseFloor)
			disagreements++
		}
		fmt.Printf("  jdk %-3d floor %-3d %s\n", major, floor, mark)
	}

	if skipped > 0 {
		fmt.Printf("%d version(s) skipped, no image built\n", skipped)
	}
	if disagreements > 0 {
		return fmt.Errorf("%d version(s) disagree with the manifest", disagreements)
	}
	return nil
}

// errNoImage separates "not built yet", which is normal, from a real failure.
var errNoImage = errors.New("image not present")

// probeFloor reads the oldest release javac will target. javac 8 and earlier have no --release at
// all and no list to read, so their floor is their own version.
func probeFloor(runtime, image string) (int, error) {
	if err := exec.Command(runtime, "image", "inspect", image).Run(); err != nil {
		return 0, errNoImage
	}

	output, err := exec.Command(runtime, "run", "--rm", "--network", "none", image,
		"javac", "--help").CombinedOutput()
	if err != nil {
		// javac 7 and 8 exit non-zero on --help and do not know the flag either way.
		if version, versionErr := probeVersion(runtime, image); versionErr == nil {
			return version, nil
		}
		return 0, fmt.Errorf("%s: %w", runtime, err)
	}

	releases := supportedReleases(string(output))
	if len(releases) == 0 {
		return probeVersion(runtime, image)
	}
	return slices.Min(releases), nil
}

// probeVersion reads the compiler's own version, which is the floor when it cannot target older.
func probeVersion(runtime, image string) (int, error) {
	output, err := exec.Command(runtime, "run", "--rm", "--network", "none", image,
		"javac", "-version").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("javac -version: %w", err)
	}

	// "javac 1.8.0_504" and "javac 25.0.4" both have to come out as a major version.
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return 0, fmt.Errorf("cannot read a version from %q", output)
	}
	number := strings.TrimPrefix(fields[len(fields)-1], "1.")
	if cut := strings.IndexAny(number, "._-+"); cut > 0 {
		number = number[:cut]
	}
	return strconv.Atoi(number)
}

// supportedReleases pulls the version list out of javac's help text. Two shapes exist, and reading
// only one of them silently loses the first version in the list:
//
//	Compile for a specific release. Supported releases: 7, 8, 9, 10, 11, 12
//
//	Supported releases:
//	    8, 9, 10, ... 25
//
// and javac 9, the first version to have --release at all, calls them targets instead:
//
//	Compile for a specific VM version. Supported targets: 6, 7, 8, 9
var releaseListHeadings = []string{"Supported releases:", "Supported targets:"}

func supportedReleases(help string) []int {
	lines := strings.Split(help, "\n")
	for i, line := range lines {
		at, heading := -1, ""
		for _, candidate := range releaseListHeadings {
			if found := strings.Index(line, candidate); found >= 0 {
				at, heading = found, candidate
				break
			}
		}
		if at < 0 {
			continue
		}

		// The heading is not always at the start of the line, and the list is on the next line
		// when it does not fit on this one.
		text := strings.TrimSpace(line[at+len(heading):])
		if text == "" && i+1 < len(lines) {
			text = strings.TrimSpace(lines[i+1])
		}

		var releases []int
		for _, field := range strings.Split(text, ",") {
			if version, err := strconv.Atoi(strings.TrimSpace(field)); err == nil {
				releases = append(releases, version)
			}
		}
		return releases
	}
	return nil
}
