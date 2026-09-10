package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// Resolver turns a manifest into a lock. It is the only thing in the project that talks to the
// outside world, and it runs when a human asks it to — never as part of serving a request.
type Resolver struct {
	HTTP *http.Client
	// Log receives one line per resolved entry, so a slow run shows progress.
	Log func(format string, args ...any)
}

// NewResolver builds a resolver with timeouts that suit archive hosts. archive.apache.org in
// particular can take a minute to answer a directory listing.
func NewResolver() *Resolver {
	return &Resolver{
		HTTP: &http.Client{Timeout: 3 * time.Minute},
		Log:  func(string, ...any) {},
	}
}

// Resolve produces a lock for everything the manifest lists.
func (r *Resolver) Resolve(ctx context.Context, manifest *Manifest) (*Lock, error) {
	lock := &Lock{GeneratedAt: time.Now().UTC().Truncate(time.Second)}

	for _, request := range manifest.JDK.Versions {
		found := 0
		for _, architecture := range manifest.JDK.Architectures {
			resolved, err := r.resolveJDK(ctx, manifest.JDK, architecture, request)
			if err != nil {
				// One architecture missing is a fact about the world, not a failure: nobody ever
				// shipped an aarch64 build of JDK 7. Missing on every architecture is a failure.
				r.Log("jdk %d/%s: %v", request.Major, architecture, err)
				continue
			}
			r.Log("jdk %d/%s: %s %s (%s)", resolved.Major, architecture,
				resolved.Distribution, resolved.JavaVersion, resolved.ReleaseStatus)
			lock.JDK = append(lock.JDK, *resolved)
			found++
		}
		if found == 0 {
			return nil, fmt.Errorf("toolchain: no build of JDK %d on any requested architecture", request.Major)
		}
	}

	for _, request := range manifest.Kotlin.Versions {
		resolved, err := r.resolveKotlin(ctx, request)
		if err != nil {
			return nil, err
		}
		r.Log("kotlin %s", resolved.Version)
		lock.Kotlin = append(lock.Kotlin, *resolved)
	}

	for _, request := range manifest.Groovy.Versions {
		resolved, err := r.resolveGroovy(ctx, request)
		if err != nil {
			return nil, err
		}
		r.Log("groovy %s", resolved.Version)
		lock.Groovy = append(lock.Groovy, *resolved)
	}

	return lock, nil
}

/* ---------- JDK, via the foojay Disco API ---------- */

// Endpoints are variables rather than constants so that a test can stand a fake in front of
// them. Nothing at runtime changes them.
var (
	FoojayAPI = "https://api.foojay.io/disco/v3.0"
	// GitHubAPI answers digest lookups, and GitHubHost is the download host they apply to.
	GitHubAPI  = "https://api.github.com"
	GitHubHost = "github.com"
)

type foojayPackage struct {
	ID            string `json:"id"`
	Distribution  string `json:"distribution"`
	JavaVersion   string `json:"java_version"`
	ReleaseStatus string `json:"release_status"`
	LibCType      string `json:"lib_c_type"`
	Filename      string `json:"filename"`
	Size          int64  `json:"size"`
}

type foojayDetail struct {
	Filename          string `json:"filename"`
	DirectDownloadURI string `json:"direct_download_uri"`
	Checksum          string `json:"checksum"`
	ChecksumType      string `json:"checksum_type"`
}

// resolveJDK picks the first distribution in the manifest's preference order that has a build.
// Vendors differ per version — JDK 7 exists only as Zulu, and 27 and 28 only as early access —
// so the order is a preference, not a requirement.
func (r *Resolver) resolveJDK(ctx context.Context, section JDKSection, architecture string, request JDKRequest) (*LockedJDK, error) {
	statuses := []string{"ga"}
	if request.AllowEarlyAccess {
		// GA first even when EA is allowed: a version can go GA between two resolver runs.
		statuses = append(statuses, "ea")
	}

	for _, status := range statuses {
		for _, distribution := range section.Distributions {
			packages, err := r.foojayPackages(ctx, section, architecture, request.Major, distribution, status)
			if err != nil {
				return nil, err
			}
			for _, pkg := range packages {
				// foojay returns musl builds under linux/x64, and those will not start on a
				// glibc base image.
				if section.Libc != "" && pkg.LibCType != section.Libc {
					continue
				}
				detail, err := r.foojayDetail(ctx, pkg.ID)
				if err != nil {
					return nil, err
				}
				if detail.DirectDownloadURI == "" {
					continue
				}
				sum := detail.Checksum
				if detail.ChecksumType != "" && detail.ChecksumType != "sha256" {
					sum = ""
				}
				// An early-access build is republished under the same tag as new builds appear, so
				// foojay's copy of its checksum is only as fresh as the last time foojay looked.
				// For those the metadata is not evidence: either the host confirms the bytes, or
				// they get hashed. GA archives are immutable, so there foojay is fine.
				if pkg.ReleaseStatus == "ea" {
					sum = ""
				}

				// foojay's metadata can lag the bytes. Early-access builds are republished under
				// the same tag and filename as new builds appear, and when that happens foojay
				// still reports the previous checksum — observed on temurin 28-ea+14, where the
				// archive grew by 48 bytes and the recorded hash no longer matched anything.
				// So where the bytes actually live is asked directly, and foojay is the fallback.
				if authoritative, err := r.digestForURL(ctx, detail.DirectDownloadURI); err != nil {
					return nil, err
				} else if authoritative != "" {
					if sum != "" && authoritative != sum {
						r.Log("jdk %d: %s republished the archive, taking the host's checksum over foojay's",
							request.Major, distribution)
					}
					sum = authoritative
				}

				if sum == "" {
					r.Log("jdk %d: %s publishes no usable checksum, hashing the archive",
						request.Major, distribution)
					if sum, err = r.hashRemote(ctx, detail.DirectDownloadURI); err != nil {
						return nil, fmt.Errorf("toolchain: jdk %d: %w", request.Major, err)
					}
				}
				return &LockedJDK{
					Major:         request.Major,
					Architecture:  architecture,
					Distribution:  pkg.Distribution,
					JavaVersion:   pkg.JavaVersion,
					ReleaseStatus: pkg.ReleaseStatus,
					URL:           detail.DirectDownloadURI,
					SHA256:        sum,
					Filename:      detail.Filename,
					Size:          pkg.Size,
					ReleaseFloor:  request.ReleaseFloor,
					ReleaseFlag:   request.UsesReleaseFlag(),
					Preview:       request.Preview,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("no %s/%s/%s build from %s",
		section.OperatingSystem, architecture, section.Libc, strings.Join(section.Distributions, ", "))
}

func (r *Resolver) foojayPackages(ctx context.Context, section JDKSection, architecture string, major int, distribution, status string) ([]foojayPackage, error) {
	query := url.Values{}
	query.Set("version", fmt.Sprint(major))
	query.Set("package_type", "jdk")
	query.Set("operating_system", section.OperatingSystem)
	query.Set("architecture", architecture)
	query.Set("archive_type", section.ArchiveType)
	query.Set("distribution", distribution)
	query.Set("release_status", status)
	query.Set("latest", "available")
	query.Set("directly_downloadable", "true")

	var body struct {
		Result []foojayPackage `json:"result"`
	}
	if err := r.getJSON(ctx, FoojayAPI+"/packages?"+query.Encode(), &body); err != nil {
		return nil, err
	}
	return body.Result, nil
}

func (r *Resolver) foojayDetail(ctx context.Context, id string) (*foojayDetail, error) {
	var body struct {
		Result []foojayDetail `json:"result"`
	}
	if err := r.getJSON(ctx, FoojayAPI+"/ids/"+url.PathEscape(id), &body); err != nil {
		return nil, err
	}
	if len(body.Result) == 0 {
		return nil, fmt.Errorf("toolchain: foojay has no detail for package %s", id)
	}
	return &body.Result[0], nil
}

/* ---------- Kotlin, via GitHub releases ---------- */

// resolveKotlin locates kotlin-compiler-<version>.zip.
//
// Checksums are looked for in increasing order of cost. Kotlin 2.2 and later carry a digest in
// the GitHub API, which is free. 1.9 through 2.1 publish a .sha256 sidecar, which is one small
// request. Everything older has neither, so the archive is downloaded once and hashed here —
// about 420 MB across the seven oldest lines, paid once because the lock is committed.
func (r *Resolver) resolveKotlin(ctx context.Context, request VersionRequest) (*LockedTool, error) {
	base := fmt.Sprintf("https://github.com/JetBrains/kotlin/releases/download/v%s/", request.Version)
	return r.resolveKotlinFrom(ctx, request, base)
}

// resolveKotlinFrom is resolveKotlin with the release base spelled out, so a test can point it at
// a server it controls.
func (r *Resolver) resolveKotlinFrom(ctx context.Context, request VersionRequest, base string) (*LockedTool, error) {
	archive := fmt.Sprintf("kotlin-compiler-%s.zip", request.Version)

	sum, err := r.digestForURL(ctx, base+archive)
	if err != nil {
		return nil, err
	}
	if sum == "" {
		if sum, err = r.fetchChecksum(ctx, base+archive+".sha256"); err != nil {
			return nil, err
		}
	}
	if sum == "" {
		r.Log("kotlin %s publishes no checksum, hashing the archive", request.Version)
		if sum, err = r.hashRemote(ctx, base+archive); err != nil {
			return nil, fmt.Errorf("toolchain: kotlin %s: %w", request.Version, err)
		}
	}

	return &LockedTool{
		Version:      request.Version,
		URL:          base + archive,
		SHA256:       sum,
		JVMTargetMax: request.JVMTargetMax,
		JDK:          request.JDK,
		Default:      request.Default,
	}, nil
}

// digestForURL returns the authoritative sha256 for a download when the host is one that
// publishes it. Today that means GitHub releases, which covers Temurin, Zulu's mirrors, Kotlin
// and most of the rest; anything else returns an empty string and the caller falls back.
func (r *Resolver) digestForURL(ctx context.Context, address string) (string, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host != GitHubHost {
		return "", nil
	}

	// /{owner}/{repo}/releases/download/{tag}/{asset}
	parts := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", nil
	}
	tag, err := url.PathUnescape(parts[4])
	if err != nil {
		return "", nil
	}
	asset, err := url.PathUnescape(parts[5])
	if err != nil {
		return "", nil
	}
	return r.githubDigest(ctx, parts[0]+"/"+parts[1], tag, asset)
}

// githubDigest reads the asset digest GitHub records for recent uploads. It returns an empty
// string, not an error, when the release or the digest is absent: older assets simply predate the
// field, and the caller has cheaper-to-worse fallbacks for that.
func (r *Resolver) githubDigest(ctx context.Context, repository, tag, asset string) (string, error) {
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}

	address := fmt.Sprintf("%s/repos/%s/releases/tags/%s", GitHubAPI, repository, url.PathEscape(tag))
	if err := r.getJSON(ctx, address, &release); err != nil {
		// An unauthenticated caller can be rate-limited, and that must not be fatal when a
		// sidecar would have answered anyway.
		r.Log("github digest for %s unavailable: %v", asset, err)
		return "", nil
	}

	for _, candidate := range release.Assets {
		if candidate.Name != asset {
			continue
		}
		sum, ok := strings.CutPrefix(candidate.Digest, "sha256:")
		if !ok {
			return "", nil
		}
		return strings.ToLower(sum), nil
	}
	return "", nil
}

/* ---------- Groovy, via the Apache archive ---------- */

// resolveGroovy points at the Apache archive rather than a mirror: mirrors drop old versions,
// and the whole point of this list is that old versions keep working.
func (r *Resolver) resolveGroovy(ctx context.Context, request VersionRequest) (*LockedTool, error) {
	base := fmt.Sprintf("https://archive.apache.org/dist/groovy/%s/distribution/", request.Version)
	archive := fmt.Sprintf("apache-groovy-binary-%s.zip", request.Version)

	sum, err := r.fetchChecksum(ctx, base+archive+".sha256")
	if err != nil {
		return nil, err
	}
	if sum == "" {
		r.Log("groovy %s publishes no checksum, hashing the archive", request.Version)
		if sum, err = r.hashRemote(ctx, base+archive); err != nil {
			return nil, fmt.Errorf("toolchain: groovy %s: %w", request.Version, err)
		}
	}

	return &LockedTool{
		Version:      request.Version,
		URL:          base + archive,
		SHA256:       sum,
		JVMTargetMax: request.JVMTargetMax,
		JDK:          request.JDK,
		Default:      request.Default,
	}, nil
}

/* ---------- plumbing ---------- */

func (r *Resolver) getJSON(ctx context.Context, address string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")

	// Anonymous callers get sixty GitHub requests an hour, and this resolver makes more than that
	// in one run. Without a token the digest lookups start failing partway through, which used to
	// mean quietly falling back to metadata that could be stale.
	if strings.HasPrefix(address, GitHubAPI) {
		if token := githubToken(); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}

	response, err := r.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("toolchain: GET %s: %w", address, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("toolchain: GET %s: %s", address, response.Status)
	}
	return json.NewDecoder(response.Body).Decode(into)
}

// fetchChecksum reads a .sha256 sidecar, returning an empty string when there is none. A
// missing sidecar is an ordinary fact about older releases, not an error.
func (r *Resolver) fetchChecksum(ctx context.Context, address string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	response, err := r.HTTP.Do(request)
	if err != nil {
		return "", fmt.Errorf("toolchain: GET %s: %w", address, err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("toolchain: GET %s: %s", address, response.Status)
	}

	// Sidecars come in two shapes: the bare hash, or "hash  filename".
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", nil
	}
	sum := strings.ToLower(fields[0])
	if len(sum) != sha256.Size*2 {
		return "", fmt.Errorf("toolchain: %s does not look like a sha256: %q", address, sum)
	}
	return sum, nil
}

// hashRemote streams an archive and returns its sha256 without keeping it. Kotlin 1.2 is 85 MB,
// so this streams rather than buffering.
func (r *Resolver) hashRemote(ctx context.Context, address string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	response, err := r.HTTP.Do(request)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", address, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", address, response.Status)
	}

	digest := sha256.New()
	if _, err := io.Copy(digest, response.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Majors lists the locked JDK versions newest first, which is the order a keyboard wants.
func (l *Lock) Majors() []int {
	seen := map[int]bool{}
	var majors []int
	for _, jdk := range l.JDK {
		if !seen[jdk.Major] {
			seen[jdk.Major] = true
			majors = append(majors, jdk.Major)
		}
	}
	slices.SortFunc(majors, func(a, b int) int { return b - a })
	return majors
}

// Architectures returns every locked download for one version.
func (l *Lock) Architectures(major int) []LockedJDK {
	var out []LockedJDK
	for _, jdk := range l.JDK {
		if jdk.Major == major {
			out = append(out, jdk)
		}
	}
	slices.SortFunc(out, func(a, b LockedJDK) int { return strings.Compare(a.Architecture, b.Architecture) })
	return out
}

// Representative is the entry a catalog reads a version's metadata from. Which architecture it
// comes from does not matter: release_floor, the flag style and the status describe the version.
func (l *Lock) Representative(major int) (LockedJDK, bool) {
	entries := l.Architectures(major)
	if len(entries) == 0 {
		return LockedJDK{}, false
	}
	return entries[0], true
}

// githubToken reads a token from the environment. Both names are checked because the CLI and the
// Actions runner disagree about which one to set.
func githubToken() string {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	return ""
}
