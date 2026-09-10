package toolchain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeHosts stands in for foojay and the GitHub API so the resolver can be exercised without a
// network. The download URLs it hands out point at the same server, which is why GitHubHost is
// redirected too.
type fakeHosts struct {
	server *httptest.Server
	// packages maps "major/distribution/status" to the JSON packages array.
	packages map[string]string
	// details maps a package id to its detail JSON.
	details map[string]string
	// digests maps an asset name to the sha256 the GitHub API reports.
	digests map[string]string
	// hits counts requests by path prefix, so a test can assert what was not asked for.
	hits map[string]int
}

func newFakeHosts(t *testing.T) *fakeHosts {
	t.Helper()
	f := &fakeHosts{
		packages: map[string]string{},
		details:  map[string]string{},
		digests:  map[string]string{},
		hits:     map[string]int{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/disco/packages", func(w http.ResponseWriter, r *http.Request) {
		f.hits["packages"]++
		query := r.URL.Query()
		key := query.Get("version") + "/" + query.Get("distribution") + "/" + query.Get("release_status")
		body, ok := f.packages[key]
		if !ok {
			body = "[]"
		}
		fmt.Fprintf(w, `{"result": %s}`, body)
	})
	mux.HandleFunc("/disco/ids/", func(w http.ResponseWriter, r *http.Request) {
		f.hits["ids"]++
		id := strings.TrimPrefix(r.URL.Path, "/disco/ids/")
		body, ok := f.details[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"result": [%s]}`, body)
	})
	mux.HandleFunc("/github/repos/", func(w http.ResponseWriter, r *http.Request) {
		f.hits["digest"]++
		var assets []string
		for name, sum := range f.digests {
			assets = append(assets, fmt.Sprintf(`{"name": %q, "digest": "sha256:%s"}`, name, sum))
		}
		fmt.Fprintf(w, `{"assets": [%s]}`, strings.Join(assets, ","))
	})
	// Any other path is a download; a test that reaches one wanted the archive hashed.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.hits["download"]++
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "pretend this is an archive")
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	previousFoojay, previousAPI, previousHost := FoojayAPI, GitHubAPI, GitHubHost
	FoojayAPI = f.server.URL + "/disco"
	GitHubAPI = f.server.URL + "/github"
	GitHubHost = mustHost(t, f.server.URL)
	t.Cleanup(func() { FoojayAPI, GitHubAPI, GitHubHost = previousFoojay, previousAPI, previousHost })

	return f
}

func mustHost(t *testing.T, address string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func (f *fakeHosts) downloadURL(owner, repo, tag, asset string) string {
	return fmt.Sprintf("%s/%s/%s/releases/download/%s/%s", f.server.URL, owner, repo, tag, asset)
}

func testResolver() *Resolver {
	r := NewResolver()
	r.Log = func(string, ...any) {}
	return r
}

func jdkSection(distributions ...string) JDKSection {
	return JDKSection{
		OperatingSystem: "linux",
		Architecture:    "x64",
		Libc:            "glibc",
		ArchiveType:     "tar.gz",
		Distributions:   distributions,
	}
}

// foojay returns musl builds under linux/x64, and one of those on a glibc base image produces a
// container that cannot even start java.
func TestMuslBuildsAreSkipped(t *testing.T) {
	hosts := newFakeHosts(t)
	hosts.packages["25/temurin/ga"] = `[
		{"id": "musl", "distribution": "temurin", "java_version": "25.0.1", "release_status": "ga", "lib_c_type": "musl"},
		{"id": "glibc", "distribution": "temurin", "java_version": "25.0.1", "release_status": "ga", "lib_c_type": "glibc"}
	]`
	hosts.details["glibc"] = fmt.Sprintf(`{"filename": "jdk.tar.gz", "direct_download_uri": %q, "checksum": "%s", "checksum_type": "sha256"}`,
		"https://example.invalid/jdk.tar.gz", strings.Repeat("a", 64))

	resolved, err := testResolver().resolveJDK(context.Background(), jdkSection("temurin"), JDKRequest{Major: 25, ReleaseFloor: 8})
	if err != nil {
		t.Fatalf("resolveJDK: %v", err)
	}
	if resolved.SHA256 != strings.Repeat("a", 64) {
		t.Errorf("picked the wrong package: %+v", resolved)
	}
}

// JDK 7 exists only as Zulu and JDK 28 only as early access, so the preference list is a
// preference and the resolver has to walk past vendors that have nothing.
func TestVendorPreferenceFallsThroughToWhoeverHasABuild(t *testing.T) {
	hosts := newFakeHosts(t)
	hosts.packages["7/zulu/ga"] = `[{"id": "z", "distribution": "zulu", "java_version": "7.0.352", "release_status": "ga", "lib_c_type": "glibc"}]`
	hosts.details["z"] = fmt.Sprintf(`{"filename": "jdk.tar.gz", "direct_download_uri": "https://example.invalid/z.tar.gz", "checksum": "%s", "checksum_type": "sha256"}`,
		strings.Repeat("b", 64))

	resolved, err := testResolver().resolveJDK(context.Background(), jdkSection("temurin", "zulu"), JDKRequest{Major: 7, ReleaseFloor: 7})
	if err != nil {
		t.Fatalf("resolveJDK: %v", err)
	}
	if resolved.Distribution != "zulu" {
		t.Errorf("distribution = %q, want zulu", resolved.Distribution)
	}
}

func TestEarlyAccessIsOnlyUsedWhenAllowedAndOnlyAfterGA(t *testing.T) {
	hosts := newFakeHosts(t)
	hosts.packages["28/temurin/ea"] = `[{"id": "e", "distribution": "temurin", "java_version": "28-ea+14", "release_status": "ea", "lib_c_type": "glibc"}]`
	hosts.details["e"] = fmt.Sprintf(`{"filename": "jdk.tar.gz", "direct_download_uri": "https://example.invalid/e.tar.gz", "checksum": "%s", "checksum_type": "sha256"}`,
		strings.Repeat("c", 64))

	section := jdkSection("temurin")
	if _, err := testResolver().resolveJDK(context.Background(), section, JDKRequest{Major: 28, ReleaseFloor: 8}); err == nil {
		t.Error("used an EA build without being allowed to")
	}

	resolved, err := testResolver().resolveJDK(context.Background(), section, JDKRequest{Major: 28, ReleaseFloor: 8, AllowEarlyAccess: true})
	if err != nil {
		t.Fatalf("resolveJDK: %v", err)
	}
	if resolved.ReleaseStatus != "ea" {
		t.Errorf("release status = %q, want ea", resolved.ReleaseStatus)
	}
}

// The reason this exists: temurin 27-ea and 28-ea were both republished under the same tag and
// filename, and foojay kept serving the previous checksum. Verifying against it would have failed
// every build with a hash mismatch nobody could explain.
func TestARepublishedArchiveTakesTheHostsChecksumOverFoojays(t *testing.T) {
	hosts := newFakeHosts(t)
	stale, actual := strings.Repeat("d", 64), strings.Repeat("e", 64)
	asset := "OpenJDK-jdk_x64_linux_hotspot_28_14-ea.tar.gz"

	hosts.packages["28/temurin/ea"] = `[{"id": "r", "distribution": "temurin", "java_version": "28-ea+14", "release_status": "ea", "lib_c_type": "glibc"}]`
	hosts.details["r"] = fmt.Sprintf(`{"filename": %q, "direct_download_uri": %q, "checksum": %q, "checksum_type": "sha256"}`,
		asset, hosts.downloadURL("adoptium", "temurin28-binaries", "jdk-28+14-ea-beta", asset), stale)
	hosts.digests[asset] = actual

	resolved, err := testResolver().resolveJDK(context.Background(), jdkSection("temurin"),
		JDKRequest{Major: 28, ReleaseFloor: 8, AllowEarlyAccess: true})
	if err != nil {
		t.Fatalf("resolveJDK: %v", err)
	}

	if resolved.SHA256 == stale {
		t.Error("kept foojay's stale checksum")
	}
	if resolved.SHA256 != actual {
		t.Errorf("sha256 = %q, want the host's %q", resolved.SHA256, actual)
	}
	if hosts.hits["download"] > 0 {
		t.Error("downloaded the archive when a digest was available")
	}
}

// Kotlin 2.2 and later carry a digest in the API, and asking for it costs one small request
// instead of 85 MB.
func TestKotlinUsesThePublishedDigestRatherThanDownloading(t *testing.T) {
	hosts := newFakeHosts(t)
	sum := strings.Repeat("f", 64)
	hosts.digests["kotlin-compiler-2.4.20.zip"] = sum

	// The real base is github.com; the test's is the fake server, so the URL is rewritten the
	// same way the resolver builds it.
	resolver := testResolver()
	resolved, err := resolver.resolveKotlinFrom(context.Background(),
		VersionRequest{Version: "2.4.20", JVMTargetMax: "25", JDK: 25},
		hosts.downloadURL("JetBrains", "kotlin", "v2.4.20", ""))
	if err != nil {
		t.Fatalf("resolveKotlin: %v", err)
	}
	if resolved.SHA256 != sum {
		t.Errorf("sha256 = %q, want %q", resolved.SHA256, sum)
	}
	if hosts.hits["download"] > 0 {
		t.Error("downloaded the archive despite a published digest")
	}
}

// Kotlin 1.2 through 1.8 publish neither a digest nor a sidecar, so the archive is hashed. The
// checksum we compute is still worth pinning: it catches a later republish.
func TestAnArchiveWithNoPublishedChecksumIsHashed(t *testing.T) {
	hosts := newFakeHosts(t)

	resolver := testResolver()
	resolved, err := resolver.resolveKotlinFrom(context.Background(),
		VersionRequest{Version: "1.2.71", JVMTargetMax: "1.8", JDK: 8},
		hosts.downloadURL("JetBrains", "kotlin", "v1.2.71", ""))
	if err != nil {
		t.Fatalf("resolveKotlin: %v", err)
	}

	if len(resolved.SHA256) != 64 {
		t.Errorf("sha256 = %q, want a digest computed from the archive", resolved.SHA256)
	}
	if hosts.hits["download"] == 0 {
		t.Error("never fetched the archive it had to hash")
	}
}

func TestAChecksumThatIsNotSHA256IsNotTrusted(t *testing.T) {
	hosts := newFakeHosts(t)
	hosts.packages["25/temurin/ga"] = `[{"id": "m", "distribution": "temurin", "java_version": "25", "release_status": "ga", "lib_c_type": "glibc"}]`
	hosts.details["m"] = fmt.Sprintf(`{"filename": "jdk.tar.gz", "direct_download_uri": %q, "checksum": "0badc0de", "checksum_type": "md5"}`,
		hosts.server.URL+"/somewhere/jdk.tar.gz")

	resolved, err := testResolver().resolveJDK(context.Background(), jdkSection("temurin"), JDKRequest{Major: 25, ReleaseFloor: 8})
	if err != nil {
		t.Fatalf("resolveJDK: %v", err)
	}
	if resolved.SHA256 == "0badc0de" {
		t.Error("recorded an md5 as if it were a sha256")
	}
	if len(resolved.SHA256) != 64 {
		t.Errorf("sha256 = %q, want the archive to have been hashed instead", resolved.SHA256)
	}
}
