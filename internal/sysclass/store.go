// Package sysclass serves JDK classes straight out of the JDK.
//
// Asking for java.util.concurrent.ConcurrentHashMap needs no compiler: the class file already
// exists inside every JDK, in lib/modules, and `jimage extract` unpacks it. So this path skips
// containers, compilation and every risk that comes with running a stranger's code — it is a file
// read and a render.
//
// The store is laid out one directory per JDK major version, so the same query against JDK 8 and
// JDK 25 returns genuinely different bytecode, which is most of why anyone would ask.
package sysclass

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrNotFound means no class matched. It is an ordinary outcome: people misremember names.
var ErrNotFound = errors.New("sysclass: no such class")

// ErrAmbiguous carries the candidates when a simple name matches several classes, which is common
// enough that guessing would be wrong: List is both java.util.List and java.awt.List.
type ErrAmbiguous struct {
	Query      string
	Candidates []string
}

func (e *ErrAmbiguous) Error() string {
	return fmt.Sprintf("sysclass: %q matches %d classes", e.Query, len(e.Candidates))
}

// Class is one resolved lookup.
type Class struct {
	// Name is the fully qualified binary name, with dots: java.util.concurrent.ConcurrentHashMap.
	Name string
	// Major is the JDK version it came from.
	Major int
	// Bytes is the class file itself.
	Bytes []byte
}

// FileName is what the class file would be called on disk.
func (c Class) FileName() string {
	simple := c.Name
	if at := strings.LastIndexByte(simple, '.'); at >= 0 {
		simple = simple[at+1:]
	}
	return simple + ".class"
}

// Store reads extracted JDK classes from a directory tree:
//
//	root/25/java/util/concurrent/ConcurrentHashMap.class
//
// Nothing is loaded up front except the index of simple names, which is what makes a bare
// ConcurrentHashMap resolvable. That index is a few thousand strings per version.
type Store struct {
	root string

	once     sync.Once
	indexErr error
	// simple maps a lowercased simple name to the qualified names that carry it, per major.
	simple map[int]map[string][]string
	majors []int

	// MaxBytes bounds one class file. A JDK class is a few kilobytes; anything far larger means
	// the store is pointed at the wrong directory.
	MaxBytes int
}

// Open prepares a store. The directory is not walked until the first lookup that needs the index,
// so starting the bot does not wait on it.
func Open(root string) *Store {
	return &Store{root: root, MaxBytes: 8 << 20}
}

// Majors lists the JDK versions the store holds, oldest first.
func (s *Store) Majors() []int {
	s.build()
	return slices.Clone(s.majors)
}

// Available reports whether the store has anything at all. A deployment without the extracted
// classes mounted should degrade to "compile it yourself" rather than fail.
func (s *Store) Available() bool {
	s.build()
	return len(s.majors) > 0
}

// Lookup resolves a query against one JDK version.
//
// It accepts what people actually type: a qualified name, the same with a .java or .class suffix,
// slashes instead of dots, a nested class spelled with either a dot or a dollar, and a bare simple
// name for anything the index knows.
func (s *Store) Lookup(major int, query string) (*Class, error) {
	s.build()
	if s.indexErr != nil {
		return nil, s.indexErr
	}

	normalized, ok := Normalize(query)
	if !ok {
		return nil, ErrNotFound
	}

	if !strings.Contains(normalized, ".") {
		return s.lookupSimple(major, normalized)
	}
	return s.read(major, normalized)
}

// lookupSimple resolves a bare name through the index.
func (s *Store) lookupSimple(major int, name string) (*Class, error) {
	candidates := s.simple[major][strings.ToLower(name)]
	switch len(candidates) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return s.read(major, candidates[0])
	}

	// A match with the same capitalization wins over one that only matched case-insensitively.
	exact := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if simpleNameOf(candidate) == name {
			exact = append(exact, candidate)
		}
	}
	if len(exact) == 1 {
		return s.read(major, exact[0])
	}
	if len(exact) == 0 {
		exact = candidates
	}

	// Names really do collide — List is java.util.List and java.awt.List, Timer is three different
	// classes — but one of them is nearly always what was meant. Ranking by package settles the
	// common cases without asking, and only a genuine tie becomes a question.
	best := rank(exact[0])
	winners := []string{}
	for _, candidate := range exact {
		switch score := rank(candidate); {
		case score < best:
			best, winners = score, []string{candidate}
		case score == best:
			winners = append(winners, candidate)
		}
	}
	if len(winners) == 1 {
		return s.read(major, winners[0])
	}
	return nil, &ErrAmbiguous{Query: name, Candidates: winners}
}

// read loads a qualified name, trying the spellings a nested class can take.
//
// java.util.Map.Entry and java.util.Map$Entry are the same class, and a user has no reason to know
// which one the file system uses, so both are attempted: the dots are turned into a dollar one
// segment at a time, from the right.
func (s *Store) read(major int, qualified string) (*Class, error) {
	if !slices.Contains(s.majors, major) {
		return nil, ErrNotFound
	}

	segments := strings.Split(qualified, ".")
	for split := len(segments) - 1; split >= 1; split-- {
		relative := path.Join(segments[:split]...) + "/" + strings.Join(segments[split:], "$") + ".class"

		blob, err := s.readFile(major, relative)
		switch {
		case err == nil:
			name := strings.Join(segments[:split], ".") + "." + strings.Join(segments[split:], "$")
			return &Class{Name: name, Major: major, Bytes: blob}, nil
		case errors.Is(err, fs.ErrNotExist):
			continue
		default:
			return nil, err
		}
	}
	return nil, ErrNotFound
}

func (s *Store) readFile(major int, relative string) ([]byte, error) {
	// The path is built from a stranger's message, so it is resolved and checked to still be
	// inside the version's directory. Without this, a query full of ".." reads any file the
	// process can.
	base := filepath.Join(s.root, strconv.Itoa(major))
	full := filepath.Join(base, filepath.FromSlash(relative))
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return nil, fs.ErrNotExist
	}

	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	if s.MaxBytes > 0 && info.Size() > int64(s.MaxBytes) {
		return nil, fmt.Errorf("sysclass: %s is %d bytes", relative, info.Size())
	}
	return os.ReadFile(full)
}

// build indexes every simple name once. Walking 4700 files per version takes a moment, and doing
// it lazily keeps it off the startup path.
func (s *Store) build() {
	s.once.Do(func() {
		s.simple = map[int]map[string][]string{}

		entries, err := os.ReadDir(s.root)
		if err != nil {
			// A missing store is not an error: the bot simply cannot answer these queries.
			if !errors.Is(err, fs.ErrNotExist) {
				s.indexErr = fmt.Errorf("sysclass: reading %s: %w", s.root, err)
			}
			return
		}

		for _, entry := range entries {
			major, convErr := strconv.Atoi(entry.Name())
			if convErr != nil || !entry.IsDir() {
				continue
			}
			names, walkErr := s.indexOne(filepath.Join(s.root, entry.Name()))
			if walkErr != nil {
				s.indexErr = walkErr
				return
			}
			if len(names) == 0 {
				continue
			}
			s.simple[major] = names
			s.majors = append(s.majors, major)
		}
		sort.Ints(s.majors)
	})
}

func (s *Store) indexOne(dir string) (map[string][]string, error) {
	names := map[string][]string{}

	err := filepath.WalkDir(dir, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".class") {
			return nil
		}

		relative, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		qualified := strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(relative), ".class"), "/", ".")

		// A nested class is reachable by its own simple name too: Map$Entry answers to "Entry".
		simple := simpleNameOf(qualified)
		key := strings.ToLower(simple)
		names[key] = append(names[key], qualified)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sysclass: indexing %s: %w", dir, err)
	}

	for key := range names {
		// Shortest first, so the shallowest package wins when a name is ambiguous, and stable so
		// the same query always answers the same way.
		sort.Slice(names[key], func(i, j int) bool {
			if len(names[key][i]) != len(names[key][j]) {
				return len(names[key][i]) < len(names[key][j])
			}
			return names[key][i] < names[key][j]
		})
	}
	return names, nil
}

// preferredPackages is which package wins when a simple name is ambiguous, most preferred first.
// It encodes what people mean rather than anything about the JDK: java.util.List over java.awt.List,
// java.util.Timer over javax.management.timer.Timer.
var preferredPackages = []string{
	"java.lang.",
	"java.util.",
	"java.io.",
	"java.nio.",
	"java.util.concurrent.",
	"java.time.",
	"java.util.function.",
	"java.util.stream.",
	"java.math.",
	"java.net.",
	"java.text.",
	"java.sql.",
}

// rank scores a candidate; lower is better. Anything outside the preferred list is ranked by how
// deep its package is, since a shallower package is the more central class more often than not.
func rank(qualified string) int {
	for i, prefix := range preferredPackages {
		if strings.HasPrefix(qualified, prefix) {
			// Direct members of a preferred package beat classes in its subpackages.
			depth := strings.Count(strings.TrimPrefix(qualified, prefix), ".")
			return i*10 + depth
		}
	}
	return len(preferredPackages)*10 + strings.Count(qualified, ".")
}

// simpleNameOf is the last dot-or-dollar separated part: java.util.Map$Entry becomes Entry.
func simpleNameOf(qualified string) string {
	if at := strings.LastIndexAny(qualified, ".$"); at >= 0 {
		return qualified[at+1:]
	}
	return qualified
}

// Normalize turns what someone typed into a dotted name, reporting whether it could be one at all.
//
// The trailing .java is deliberate: people copy a file name out of an IDE, and the difference
// between a class and its source file is not something they should have to think about here.
func Normalize(query string) (string, bool) {
	name := strings.TrimSpace(query)
	name = strings.Trim(name, "`\"'")
	name = strings.TrimSpace(name)

	// A descriptor is unwrapped only when it is actually one. Stripping a leading L unconditionally
	// would turn List into ist, and Long, Locale and LinkedList with it.
	if len(name) > 2 && name[0] == 'L' && strings.HasSuffix(name, ";") {
		name = name[1 : len(name)-1]
	}

	// Slashes are how the class file itself spells a package, and people paste that form out of
	// stack traces and bytecode.
	name = strings.ReplaceAll(name, "/", ".")

	for _, suffix := range []string{".java", ".class", ".kt"} {
		if trimmed, cut := strings.CutSuffix(name, suffix); cut {
			name = trimmed
			break
		}
	}
	name = strings.Trim(name, ".")
	if name == "" {
		return "", false
	}

	for _, segment := range strings.Split(name, ".") {
		if segment == "" || !isIdentifier(segment) {
			return "", false
		}
	}
	return name, true
}

// isIdentifier is deliberately loose: it accepts what a Java name can be, including the dollar in
// a nested class, and rejects anything with punctuation or spaces, which is what tells a class
// name apart from a line of code.
func isIdentifier(segment string) bool {
	for i, r := range segment {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '$':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// LooksLikeQuery reports whether a message is a class name rather than source code.
//
// The test is that source code has punctuation a name cannot: braces, semicolons, parentheses,
// more than one line. Being strict here matters in both directions — a snippet mistaken for a
// query gets a "no such class", and a query mistaken for a snippet gets sent to a compiler.
func LooksLikeQuery(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.ContainsAny(trimmed, "{}();=\n\r\t ") {
		return "", false
	}
	// A name long enough to be a package path is still a name, but nothing sensible is this long.
	if len(trimmed) > 256 {
		return "", false
	}
	return Normalize(trimmed)
}
