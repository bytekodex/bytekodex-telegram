package sysclass

import (
	"os"
	"testing"
)

// Against a real `jimage extract` of a JDK, not a fixture. Skipped unless BYTEKODEX_SYSCLASSES
// points at one, so the suite still runs without a JDK to hand.
func TestAgainstARealExtraction(t *testing.T) {
	root := os.Getenv("BYTEKODEX_SYSCLASSES")
	if root == "" {
		t.Skip("BYTEKODEX_SYSCLASSES is not set")
	}
	store := Open(root)
	if !store.Available() {
		t.Fatalf("%s holds no classes", root)
	}
	major := store.Majors()[0]

	for _, query := range []string{
		"java.util.concurrent.ConcurrentHashMap",
		"ConcurrentHashMap",
		"java.lang.String",
		"String",
		"List",
		"Long",
		"Locale",
		"LinkedList",
		"java.util.Map.Entry",
		"Ljava/lang/Object;",
		"java.util.stream.Collectors.java",
	} {
		class, err := store.Lookup(major, query)
		if err != nil {
			t.Errorf("Lookup(%q): %v", query, err)
			continue
		}
		if len(class.Bytes) < 100 || string(class.Bytes[:4]) != "\xca\xfe\xba\xbe" {
			t.Errorf("Lookup(%q) returned %d bytes, not a class file", query, len(class.Bytes))
		}
		t.Logf("%-42s -> %s (%d bytes)", query, class.Name, len(class.Bytes))
	}
}
