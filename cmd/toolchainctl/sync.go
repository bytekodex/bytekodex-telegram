package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
)

// botUID is the bot image's own user. The scratch directory has to belong to it, because the
// compilers it starts run as the same user and write their classes there.
const botUID = 1000

// syncHost readies the machine the bot runs on with everything the bot reads besides its own image:
// every compiler image the lock offers, each JDK's system classes, the Kotlin classpath jars, and a
// scratch directory. It runs before the bot on every deploy, so each step skips what is already
// there: an image tag never changes once its checksum is locked, and a jar is checked by its hash.
//
// The layout under root is the one the bot is pointed at:
//
//	root/sysclasses/<major>/java   BYTEKODEX_SYSCLASSES
//	root/deps/<kotlin>/<jar>       BYTEKODEX_DEPS
//	root/scratch                   TMPDIR
func syncHost(lockPath, root string) error {
	lock, err := toolchain.LoadLock(lockPath)
	if err != nil {
		return err
	}
	runtime := "docker"
	if found := os.Getenv("BYTEKODEX_RUNTIME"); found != "" {
		runtime = found
	}

	if err := pullImages(runtime, toolchain.FromLock(lock)); err != nil {
		return err
	}
	if err := syncSysclasses(runtime, lock, filepath.Join(root, "sysclasses")); err != nil {
		return err
	}
	if err := syncDeps(lock, filepath.Join(root, "deps")); err != nil {
		return err
	}

	scratch := filepath.Join(root, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	// Only possible as root, which is how the deploy runs this. Run by hand as anyone else, the
	// directory is that user's, which is what they want anyway.
	if os.Geteuid() == 0 {
		return os.Chown(scratch, botUID, botUID)
	}
	return nil
}

// pullImages pulls the image of every release the keyboard offers, so the first compilation with
// a rarely used version doesn't spend its deadline downloading one.
func pullImages(runtime string, catalog *toolchain.Catalog) error {
	seen := map[string]bool{}
	pulled := 0
	for _, language := range catalog.Languages() {
		chain, _ := catalog.For(language)
		for _, release := range chain.Releases {
			if seen[release.Image] {
				continue
			}
			seen[release.Image] = true
			if exec.Command(runtime, "image", "inspect", release.Image).Run() == nil {
				continue
			}
			fmt.Printf("pull  %s\n", release.Image)
			if output, err := exec.Command(runtime, "pull", "-q", release.Image).CombinedOutput(); err != nil {
				return fmt.Errorf("pulling %s: %w: %s", release.Image, err, output)
			}
			pulled++
		}
	}
	fmt.Printf("images: %d pulled, %d already here\n", pulled, len(seen)-pulled)
	return nil
}

// syncSysclasses turns each `sysclasses:<major>` image into a directory of .class files. Reading a
// file out of an image takes a container, even one that never runs anything: CMD is unset in
// Dockerfile.sysclasses on purpose.
func syncSysclasses(runtime string, lock *toolchain.Lock, dest string) error {
	extracted := 0
	for _, major := range lock.Majors() {
		dir := filepath.Join(dest, strconv.Itoa(major))
		if _, err := os.Stat(filepath.Join(dir, "java")); err == nil {
			continue
		}
		image := sysclassImage(major)
		fmt.Printf("sync  %s\n", image)
		if output, err := exec.Command(runtime, "pull", "-q", image).CombinedOutput(); err != nil {
			return fmt.Errorf("pulling %s: %w: %s", image, err, output)
		}
		created, err := exec.Command(runtime, "create", image, "/java").Output()
		if err != nil {
			return fmt.Errorf("creating a container from %s: %w", image, err)
		}
		container := strings.TrimSpace(string(created))
		// Copied next to the final place and renamed into it, so a deploy that dies halfway leaves
		// no directory that the check above would take for a finished one.
		partial := dir + ".partial"
		_ = os.RemoveAll(partial)
		if err := os.MkdirAll(partial, 0o755); err != nil {
			return err
		}
		output, err := exec.Command(runtime, "cp", "-q", container+":/java", filepath.Join(partial, "java")).CombinedOutput()
		_ = exec.Command(runtime, "rm", "-f", container).Run()
		if err != nil {
			return fmt.Errorf("copying the classes out of %s: %w: %s", image, err, output)
		}
		_ = os.RemoveAll(dir)
		if err := os.Rename(partial, dir); err != nil {
			return err
		}
		extracted++
	}
	fmt.Printf("sysclasses: %d extracted, %d already here\n", extracted, len(lock.Majors())-extracted)
	return nil
}

// syncDeps downloads the classpath jars each Kotlin release needs and checks them against the
// lock. A jar already on disk with the right hash is left alone.
func syncDeps(lock *toolchain.Lock, dest string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	fetched, total := 0, 0
	for _, kotlin := range lock.Kotlin {
		for _, dep := range kotlin.Deps {
			total++
			out := filepath.Join(dest, kotlin.Version, dep.Name)
			if sum, err := fileSHA256(out); err == nil && sum == dep.SHA256 {
				continue
			}
			fmt.Printf("fetch %s/%s\n", kotlin.Version, dep.Name)
			if err := download(client, dep.URL, dep.SHA256, out); err != nil {
				return fmt.Errorf("%s/%s: %w", kotlin.Version, dep.Name, err)
			}
			fetched++
		}
	}
	fmt.Printf("deps: %d fetched, %d already here\n", fetched, total-fetched)
	return nil
}

func download(client *http.Client, url, want, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, response.Status)
	}

	partial := out + ".tmp"
	file, err := os.Create(partial)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(file, hash), response.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(partial)
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		_ = os.Remove(partial)
		return fmt.Errorf("checksum mismatch: got %s, the lock says %s", got, want)
	}
	return os.Rename(partial, out)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
