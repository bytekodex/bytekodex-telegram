package sysclass

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// writeClasses creates each path under root with the path itself as its content, so a test can
// tell which file a lookup actually read.
func writeClasses(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fixture(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	writeClasses(t, root,
		// Flattened, as an unzipped rt.jar or a merged jimage extract.
		"21/java/util/List.class",
		"21/java/awt/List.class",
		"21/java/util/Map.class",
		"21/java/util/Map$Entry.class",
		"21/java/util/TreeMap$Entry.class",
		"21/java/util/HashMap$1.class",
		"21/java/lang/Thread.class",
		"21/java/lang/Thread$State.class",
		"21/java/lang/Long.class",
		"21/java/lang/String.class",
		"21/java/io/Gadget.class",
		"21/java/util/concurrent/Gadget.class",
		// As `jimage extract` leaves it: one directory per module.
		"25/java.base/module-info.class",
		"25/java.base/java/util/List.class",
		"25/java.desktop/java/awt/List.class",
		// Not versions: a name that is not a number, and a number that is not canonical.
		"notes/java/lang/Object.class",
		"025/java/lang/Object.class",
	)
	return Open(root)
}

func TestLookup(t *testing.T) {
	store := fixture(t)
	cases := []struct {
		major int
		query string
		want  string
		file  string
	}{
		{21, "java.util.List", "java.util.List", "21/java/util/List.class"},
		{21, "List", "java.util.List", "21/java/util/List.class"},
		{21, "list", "java.util.List", "21/java/util/List.class"},
		{21, "java.util.list", "java.util.List", "21/java/util/List.class"},
		{21, "java/util/List.java", "java.util.List", "21/java/util/List.class"},
		{21, "Ljava/util/List;", "java.util.List", "21/java/util/List.class"},
		{21, "List<String>", "java.util.List", "21/java/util/List.class"},
		{21, "java.util.Map.Entry", "java.util.Map$Entry", "21/java/util/Map$Entry.class"},
		{21, "Map.Entry", "java.util.Map$Entry", "21/java/util/Map$Entry.class"},
		{21, "Map$Entry", "java.util.Map$Entry", "21/java/util/Map$Entry.class"},
		{21, "Thread.State", "java.lang.Thread$State", "21/java/lang/Thread$State.class"},
		{21, "Long;", "java.lang.Long", "21/java/lang/Long.class"},
		{21, "[Ljava.lang.String;", "java.lang.String", "21/java/lang/String.class"},
		{21, "String[]", "java.lang.String", "21/java/lang/String.class"},
		{21, "java.util.HashMap$1", "java.util.HashMap$1", "21/java/util/HashMap$1.class"},
		// java.io is listed above java.util.concurrent; the longer prefix must not be shadowed by java.util.
		{21, "Gadget", "java.io.Gadget", "21/java/io/Gadget.class"},
		{25, "List", "java.util.List", "25/java.base/java/util/List.class"},
		{25, "java.awt.List", "java.awt.List", "25/java.desktop/java/awt/List.class"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d %q", c.major, c.query), func(t *testing.T) {
			got, err := store.Lookup(c.major, c.query)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if got.Name != c.want || got.Major != c.major || string(got.Bytes) != c.file {
				t.Errorf("got %s from %q, want %s from %q", got.Name, got.Bytes, c.want, c.file)
			}
		})
	}
}

func TestLookupErrors(t *testing.T) {
	store := fixture(t)

	if got := store.Majors(); !slices.Equal(got, []int{21, 25}) {
		t.Errorf("Majors() = %v, want [21 25]", got)
	}

	var ambiguous *ErrAmbiguous
	if _, err := store.Lookup(21, "Entry"); !errors.As(err, &ambiguous) {
		t.Errorf("Entry: err = %v, want ErrAmbiguous", err)
	} else if want := []string{"java.util.Map$Entry", "java.util.TreeMap$Entry"}; !slices.Equal(ambiguous.Candidates, want) {
		t.Errorf("Entry: candidates = %v, want %v", ambiguous.Candidates, want)
	}

	for _, query := range []string{"Nope", "foo()", "", "java.util"} {
		if _, err := store.Lookup(21, query); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: err = %v, want ErrNotFound", query, err)
		}
	}

	var noVersion *ErrNoVersion
	if _, err := store.Lookup(17, "List"); !errors.As(err, &noVersion) {
		t.Errorf("JDK 17: err = %v, want ErrNoVersion", err)
	} else if !slices.Equal(noVersion.Have, []int{21, 25}) {
		t.Errorf("JDK 17: Have = %v, want [21 25]", noVersion.Have)
	}
}

func TestSymlinkedVersion(t *testing.T) {
	target := t.TempDir()
	writeClasses(t, target, "java/lang/Object.class")
	root := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "17")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := Open(root).Lookup(17, "Object")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Name != "java.lang.Object" {
		t.Errorf("got %s, want java.lang.Object", got.Name)
	}
}

func TestStoreAppearingAfterFirstQueryIsPickedUp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store := Open(root)
	store.retry = time.Millisecond

	if store.Available() {
		t.Fatal("Available() before the store exists")
	}
	writeClasses(t, root, "21/java/lang/Object.class")
	time.Sleep(10 * time.Millisecond)
	if !store.Available() {
		t.Fatal("a store that appeared after the first query was never picked up")
	}
}

func TestConcurrentFirstLookup(t *testing.T) {
	store := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Lookup(21, "List"); err != nil {
				t.Errorf("Lookup: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestLooksLikeQuery(t *testing.T) {
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		{"java.util.List", "java.util.List", true},
		{"  `ConcurrentHashMap`  ", "ConcurrentHashMap", true},
		{"[Ljava.lang.String;", "java.lang.String", true},
		{"Ljava/util/List;", "java.util.List", true},
		{"List<String>", "List", true},
		{"LinkedList", "LinkedList", true},
		{"foo();", "", false},
		{"Long;", "", false},
		{"int x = 1", "", false},
		{"class A {}", "", false},
		{"println(1)\nprintln(2)", "", false},
	}
	for _, c := range cases {
		got, ok := LooksLikeQuery(c.text)
		if got != c.want || ok != c.ok {
			t.Errorf("LooksLikeQuery(%q) = %q, %v; want %q, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}
