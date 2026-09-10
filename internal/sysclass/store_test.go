package sysclass

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fixture builds a small store on disk shaped like a real extraction.
func fixture(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()

	for _, class := range []struct {
		major int
		path  string
	}{
		{25, "java/util/concurrent/ConcurrentHashMap.class"},
		{25, "java/util/List.class"},
		{25, "java/util/Map.class"},
		{25, "java/util/Map$Entry.class"},
		{25, "java/awt/List.class"},
		{25, "java/lang/String.class"},
		{8, "java/util/concurrent/ConcurrentHashMap.class"},
		{8, "java/lang/String.class"},
	} {
		full := filepath.Join(root, itoa(class.major), filepath.FromSlash(class.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		// The content only has to be distinguishable; nothing here parses it.
		if err := os.WriteFile(full, []byte("\xca\xfe\xba\xbe"+class.path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Open(root)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// The spellings a person actually types. Each of these has to reach the same class.
func TestEverySpellingOfAQualifiedNameResolves(t *testing.T) {
	store := fixture(t)

	for _, query := range []string{
		"java.util.concurrent.ConcurrentHashMap",
		"java.util.concurrent.ConcurrentHashMap.java",
		"java.util.concurrent.ConcurrentHashMap.class",
		"java/util/concurrent/ConcurrentHashMap",
		"Ljava/util/concurrent/ConcurrentHashMap;",
		"  java.util.concurrent.ConcurrentHashMap  ",
		"`java.util.concurrent.ConcurrentHashMap`",
		"ConcurrentHashMap",
		"concurrenthashmap",
	} {
		class, err := store.Lookup(25, query)
		if err != nil {
			t.Errorf("Lookup(%q): %v", query, err)
			continue
		}
		if class.Name != "java.util.concurrent.ConcurrentHashMap" {
			t.Errorf("Lookup(%q) = %q", query, class.Name)
		}
	}
}

// A nested class is one class with two spellings, and a user has no reason to know which one the
// file system uses.
func TestNestedClassesResolveWithADotOrADollar(t *testing.T) {
	store := fixture(t)

	for _, query := range []string{"java.util.Map.Entry", "java.util.Map$Entry", "Entry"} {
		class, err := store.Lookup(25, query)
		if err != nil {
			t.Errorf("Lookup(%q): %v", query, err)
			continue
		}
		if class.Name != "java.util.Map$Entry" {
			t.Errorf("Lookup(%q) = %q", query, class.Name)
		}
	}
}

// java.util.List and java.awt.List both exist, and the shallower package is what someone typing
// "List" almost always means.
func TestAnAmbiguousSimpleNamePrefersTheShallowestPackage(t *testing.T) {
	store := fixture(t)

	class, err := store.Lookup(25, "List")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if class.Name != "java.util.List" {
		t.Errorf("got %q, want java.util.List", class.Name)
	}
}

// The version is half the point: the same class from JDK 8 and JDK 25 is different bytecode.
func TestTheSameQueryAgainstTwoVersionsReadsTwoFiles(t *testing.T) {
	store := fixture(t)

	modern, err := store.Lookup(25, "java.lang.String")
	if err != nil {
		t.Fatal(err)
	}
	ancient, err := store.Lookup(8, "java.lang.String")
	if err != nil {
		t.Fatal(err)
	}
	if modern.Major == ancient.Major {
		t.Error("both lookups came from the same version")
	}

	// A class only present in one version must not leak into the other.
	if _, err := store.Lookup(8, "java.util.Map"); !errors.Is(err, ErrNotFound) {
		t.Errorf("JDK 8 answered for a class it does not have: %v", err)
	}
}

func TestAVersionTheStoreDoesNotHaveIsNotFound(t *testing.T) {
	store := fixture(t)
	if _, err := store.Lookup(21, "java.lang.String"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMajorsAreReportedOldestFirst(t *testing.T) {
	store := fixture(t)
	majors := store.Majors()
	if len(majors) != 2 || majors[0] != 8 || majors[1] != 25 {
		t.Errorf("Majors() = %v, want [8 25]", majors)
	}
}

// The path is built out of a stranger's message, so traversal has to be impossible rather than
// unlikely.
func TestPathTraversalCannotEscapeTheStore(t *testing.T) {
	store := fixture(t)

	for _, query := range []string{
		"../../../etc/passwd",
		"java.util...........String",
		"/etc/passwd",
		"..",
	} {
		if _, err := store.Lookup(25, query); err == nil {
			t.Errorf("Lookup(%q) succeeded", query)
		}
	}
}

// Telling a query from a snippet has to be right in both directions: a snippet mistaken for a
// query gets a pointless "no such class", and a query mistaken for a snippet gets compiled.
func TestQueriesAreToldApartFromSourceCode(t *testing.T) {
	queries := []string{
		"java.util.concurrent.ConcurrentHashMap",
		"ConcurrentHashMap",
		"java/lang/String.java",
		"Map$Entry",
	}
	for _, text := range queries {
		if _, ok := LooksLikeQuery(text); !ok {
			t.Errorf("LooksLikeQuery(%q) = false, want true", text)
		}
	}

	snippets := []string{
		"class A {}",
		"fun main() = println(1)",
		"int x = 1;",
		"java.util.List<String> xs",
		"class A {\n}",
		"System.out.println(\"hi\")",
		"",
		"   ",
	}
	for _, text := range snippets {
		if got, ok := LooksLikeQuery(text); ok {
			t.Errorf("LooksLikeQuery(%q) = %q, true; want false", text, got)
		}
	}
}

func TestAMissingStoreIsNotAnError(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "never-created"))

	if store.Available() {
		t.Error("reported itself available with nothing on disk")
	}
	if _, err := store.Lookup(25, "java.lang.String"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestFileNameIsTheSimpleName(t *testing.T) {
	cases := map[string]string{
		"java.util.concurrent.ConcurrentHashMap": "ConcurrentHashMap.class",
		"java.util.Map$Entry":                    "Map$Entry.class",
	}
	for name, want := range cases {
		if got := (Class{Name: name}).FileName(); got != want {
			t.Errorf("FileName(%q) = %q, want %q", name, got, want)
		}
	}
}
