// Package sysclass serves JDK classes straight out of the JDK.
//
// Asking for java.util.concurrent.ConcurrentHashMap needs no compiler: the class file already
// exists inside every JDK — in lib/modules since JDK 9, which `jimage extract` unpacks, and in
// jre/lib/rt.jar before that, which unzip does. So this path skips containers, compilation and
// every risk that comes with running a stranger's code — it is a file read and a render.
//
// The store is laid out one directory per JDK major version, so the same query against JDK 8 and
// JDK 25 returns genuinely different bytecode, which is most of why anyone would ask.
package sysclass

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotFound means no class matched. It is an ordinary outcome: people misremember names.
var ErrNotFound = errors.New("sysclass: no such class")

// ErrNoVersion means the store holds nothing for that JDK version. Each version is extracted into
// its own directory, so a version can be missing while every other one is there.
type ErrNoVersion struct {
	Major int
	Have  []int
}

func (e *ErrNoVersion) Error() string {
	return fmt.Sprintf("sysclass: no classes stored for JDK %d", e.Major)
}

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

// Store reads extracted JDK classes from a directory tree, one directory per major version, in
// either of two layouts:
//
//	root/25/java/util/concurrent/ConcurrentHashMap.class            flattened, or an unzipped rt.jar
//	root/25/java.base/java/util/concurrent/ConcurrentHashMap.class  as `jimage extract` leaves it
//
// A top-level directory whose name contains a dot is a module, since package directories never
// have one. Nothing is loaded up front except an index of class names; every lookup is answered
// from it, and the file system is only touched to read a class the index already knows.
type Store struct {
	root string

	// MaxBytes bounds one class file. A JDK class is a few kilobytes; anything far larger means
	// the store is pointed at the wrong directory.
	MaxBytes int

	current  atomic.Pointer[snapshot]
	building sync.Mutex
	// retry is how long an incomplete index is trusted before the directory is walked again.
	retry time.Duration
}

// defaultRetry is short enough that a volume mounted after the first query is picked up within
// a minute, and long enough that a store which is simply absent costs one ReadDir per interval.
const defaultRetry = 30 * time.Second

// snapshot is one walk of the store. A complete one is kept for the life of the process; an
// incomplete one is served until it is retried, so a store that was absent, unreadable or partly
// broken on the first query does not stay that way until a restart.
type snapshot struct {
	// err is set when the root itself could not be read. A missing root is not an error.
	err      error
	versions map[int]*version
	// broken holds versions whose directory exists but could not be indexed. They are reported
	// as themselves rather than as ErrNoVersion: "that version is broken" and "we never stocked
	// that version" are different answers.
	broken  map[int]error
	majors  []int
	builtAt time.Time
	// complete is true when the root was read, at least one version was indexed and none broke.
	complete bool
}

type version struct {
	dir string
	// bySimple maps a lowercased simple name to the classes that carry it.
	bySimple map[string][]entry
}

type entry struct {
	// name is the binary name with dots: java.util.Map$Entry.
	name string
	// module is the directory the class sits under in a jimage layout, or empty when flattened.
	module string
}

// Open prepares a store. The directory is not walked until the first call that needs the index,
// so starting the bot does not wait on it.
func Open(root string) *Store {
	return &Store{root: root, MaxBytes: 8 << 20, retry: defaultRetry}
}

// Majors lists the JDK versions the store holds, oldest first.
func (s *Store) Majors() []int {
	return slices.Clone(s.load().majors)
}

// Available reports whether the store has anything at all. A deployment without the extracted
// classes mounted should degrade to "compile it yourself" rather than fail.
func (s *Store) Available() bool {
	return len(s.load().majors) > 0
}

// Lookup resolves a query against one JDK version.
//
// It accepts what people actually type: a qualified name, the same with a .java or .class suffix,
// slashes instead of dots, a JVM descriptor, a nested class spelled with either a dot or a dollar
// with or without its package (Map.Entry, Map$Entry, java.util.Map.Entry), and a bare simple name
// for anything the index knows.
func (s *Store) Lookup(major int, query string) (*Class, error) {
	snap := s.load()
	if snap.err != nil {
		return nil, snap.err
	}
	if err := snap.broken[major]; err != nil {
		return nil, err
	}

	// "we never stocked that version" and "no such class" are different answers, and telling
	// someone their class does not exist when the truth is that JDK 14 was never extracted would
	// send them looking for a mistake they did not make.
	v := snap.versions[major]
	if v == nil {
		return nil, &ErrNoVersion{Major: major, Have: slices.Clone(snap.majors)}
	}

	normalized, ok := Normalize(query)
	if !ok {
		return nil, ErrNotFound
	}

	found, err := v.resolve(normalized)
	if err != nil {
		return nil, err
	}
	blob, err := s.readFile(v.dir, found)
	if errors.Is(err, fs.ErrNotExist) {
		// Indexed, then removed: from the user's side that is simply not there.
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Class{Name: found.name, Major: major, Bytes: blob}, nil
}

// resolve finds the class a normalized query names.
//
// Every spelling goes through the index. The query is compared with dots and dollars treated
// alike, against the whole binary name first and then against a trailing part of it, so
// java.util.Map.Entry, Map.Entry, Map$Entry and Entry all reach java.util.Map$Entry. Because
// nothing is probed on disk, case sensitivity is the same on every file system.
func (v *version) resolve(query string) (entry, error) {
	want := strings.ReplaceAll(query, "$", ".")
	candidates := v.bySimple[strings.ToLower(simpleNameOf(query))]

	// Tiers, most specific first; within each, a match with the same capitalization wins over one
	// that only matched case-insensitively. java.util.List as typed beats any longer name ending
	// in it, and List beats list.
	var tiers [4][]entry
	for _, candidate := range candidates {
		dotted := strings.ReplaceAll(candidate.name, "$", ".")
		switch {
		case dotted == want:
			tiers[0] = append(tiers[0], candidate)
		case strings.EqualFold(dotted, want):
			tiers[1] = append(tiers[1], candidate)
		case hasNameSuffix(dotted, want, false):
			tiers[2] = append(tiers[2], candidate)
		case hasNameSuffix(dotted, want, true):
			tiers[3] = append(tiers[3], candidate)
		}
	}
	for _, tier := range tiers {
		if len(tier) > 0 {
			return pick(query, tier)
		}
	}
	return entry{}, ErrNotFound
}

// hasNameSuffix reports whether suffix is a whole trailing part of a dotted name: Map.Entry is a
// suffix of java.util.Map.Entry, but not of java.util.TreeMap.Entry.
func hasNameSuffix(name, suffix string, fold bool) bool {
	if len(name) <= len(suffix) || name[len(name)-len(suffix)-1] != '.' {
		return false
	}
	tail := name[len(name)-len(suffix):]
	if fold {
		return strings.EqualFold(tail, suffix)
	}
	return tail == suffix
}

// pick chooses among classes that matched equally well.
func pick(query string, matches []entry) (entry, error) {
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Names really do collide — List is java.util.List and java.awt.List, Timer is three different
	// classes — but one of them is nearly always what was meant. Ranking by package settles the
	// common cases without asking, and only a genuine tie becomes a question.
	best := rank(matches[0].name)
	var winners []entry
	for _, candidate := range matches {
		switch score := rank(candidate.name); {
		case score < best:
			best, winners = score, []entry{candidate}
		case score == best:
			winners = append(winners, candidate)
		}
	}
	if len(winners) == 1 {
		return winners[0], nil
	}

	names := make([]string, len(winners))
	for i, winner := range winners {
		names[i] = winner.name
	}
	return entry{}, &ErrAmbiguous{Query: query, Candidates: names}
}

func (s *Store) readFile(dir string, found entry) ([]byte, error) {
	relative := strings.ReplaceAll(found.name, ".", "/") + ".class"
	full := filepath.Join(dir, found.module, filepath.FromSlash(relative))
	// The path comes from the index, not from the message, but the check costs nothing and keeps
	// a stranger's input from ever reaching outside the version's directory if that changes.
	if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) {
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

	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var reader io.Reader = file
	if s.MaxBytes > 0 {
		// The size was checked before opening; the limit holds even if the file grew since.
		reader = io.LimitReader(file, int64(s.MaxBytes)+1)
	}
	blob, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if s.MaxBytes > 0 && len(blob) > s.MaxBytes {
		return nil, fmt.Errorf("sysclass: %s is over %d bytes", relative, s.MaxBytes)
	}
	return blob, nil
}

// load returns the current index, walking the store when there is none yet or when an
// incomplete one is due for a retry. Only the very first walk makes callers wait; a retry runs
// while everyone else keeps being served the previous answer.
func (s *Store) load() *snapshot {
	snap := s.current.Load()
	if snap != nil && (snap.complete || time.Since(snap.builtAt) < s.retryAfter()) {
		return snap
	}

	if snap == nil {
		s.building.Lock()
	} else if !s.building.TryLock() {
		return snap
	}
	defer s.building.Unlock()

	// Someone else may have finished a walk while this caller waited for the lock.
	if latest := s.current.Load(); latest != snap {
		return latest
	}
	next := s.build()
	s.current.Store(next)
	return next
}

func (s *Store) retryAfter() time.Duration {
	if s.retry > 0 {
		return s.retry
	}
	return defaultRetry
}

// build indexes every version directory. Walking a JDK's classes takes a moment, and doing it
// lazily keeps it off the startup path.
func (s *Store) build() *snapshot {
	snap := &snapshot{
		versions: map[int]*version{},
		broken:   map[int]error{},
		builtAt:  time.Now(),
	}

	entries, err := os.ReadDir(s.root)
	if err != nil {
		// A missing store is not an error: the bot simply cannot answer these queries until it
		// appears.
		if !errors.Is(err, fs.ErrNotExist) {
			snap.err = fmt.Errorf("sysclass: reading %s: %w", s.root, err)
		}
		return snap
	}

	for _, dirEntry := range entries {
		major, convErr := strconv.Atoi(dirEntry.Name())
		// Only the canonical spelling: "025" and "+25" would parse to the same major as "25" and
		// silently replace it.
		if convErr != nil || major <= 0 || strconv.Itoa(major) != dirEntry.Name() {
			continue
		}
		dir := filepath.Join(s.root, dirEntry.Name())
		// A version directory is often a symlink to wherever that JDK was extracted. DirEntry
		// describes the link itself, so the target is asked directly.
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			continue
		}

		names, indexErr := indexVersion(dir)
		if indexErr != nil {
			// One unreadable version must not take the others down with it.
			snap.broken[major] = indexErr
			continue
		}
		if len(names) == 0 {
			continue
		}
		snap.versions[major] = &version{dir: dir, bySimple: names}
		snap.majors = append(snap.majors, major)
	}
	sort.Ints(snap.majors)
	snap.complete = len(snap.majors) > 0 && len(snap.broken) == 0
	return snap
}

func indexVersion(dir string) (map[string][]entry, error) {
	// WalkDir does not follow a symlinked root, so the walk starts from the target.
	walkRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("sysclass: indexing %s: %w", dir, err)
	}

	names := map[string][]entry{}
	// One copy of each module name, shared by all of its classes.
	modules := map[string]string{}

	err = filepath.WalkDir(walkRoot, func(full string, dirEntry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".class") {
			return nil
		}

		relative, err := filepath.Rel(walkRoot, full)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)

		var module string
		if first, rest, nested := strings.Cut(relative, "/"); nested && strings.Contains(first, ".") {
			if _, seen := modules[first]; !seen {
				modules[first] = first
			}
			module, relative = modules[first], rest
		}

		name := strings.ReplaceAll(strings.TrimSuffix(relative, ".class"), "/", ".")
		// A nested class is reachable by its own simple name too: Map$Entry answers to "Entry".
		key := strings.ToLower(simpleNameOf(name))
		names[key] = append(names[key], entry{name: name, module: module})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sysclass: indexing %s: %w", dir, err)
	}

	for _, list := range names {
		// Shortest first and otherwise alphabetical, so candidates are listed the same way every
		// time the same query is asked.
		sort.Slice(list, func(i, j int) bool {
			if len(list[i].name) != len(list[j].name) {
				return len(list[i].name) < len(list[j].name)
			}
			return list[i].name < list[j].name
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

// rank scores a candidate; lower is better. The longest matching prefix decides, so
// java.util.concurrent.TimeUnit is ranked as java.util.concurrent rather than as java.util.
// Anything outside the preferred list is ranked by how deep its package is, since a shallower
// package is the more central class more often than not.
func rank(qualified string) int {
	best, matched := -1, 0
	for i, prefix := range preferredPackages {
		if len(prefix) > matched && strings.HasPrefix(qualified, prefix) {
			best, matched = i, len(prefix)
		}
	}
	if best >= 0 {
		// Direct members of a preferred package beat classes in its subpackages.
		return best*10 + strings.Count(qualified[matched:], ".")
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

	if body, ok := descriptorBody(name); ok {
		name = body
	}
	// A semicolon carried over from an import line is not part of the name.
	name = strings.TrimSuffix(name, ";")

	// Array brackets and type arguments are not part of the class: String[] asks about String,
	// List<String> about List.
	for strings.HasSuffix(name, "[]") {
		name = strings.TrimSuffix(name, "[]")
	}
	if at := strings.IndexByte(name, '<'); at > 0 && strings.HasSuffix(name, ">") {
		name = name[:at]
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

// descriptorBody unwraps a JVM descriptor: Ljava/util/List; from bytecode, [Ljava.lang.String;
// from a ClassCastException. It demands a package separator inside, because every JDK class has
// a package, and without that test a leading L would be stripped from anything ending in a
// semicolon: Long; would become ong.
func descriptorBody(s string) (string, bool) {
	s = strings.TrimLeft(s, "[")
	if len(s) > 2 && s[0] == 'L' && strings.HasSuffix(s, ";") && strings.ContainsAny(s, "/.") {
		return s[1 : len(s)-1], true
	}
	return "", false
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
	// A name long enough to be a package path is still a name, but nothing sensible is this long.
	if trimmed == "" || len(trimmed) > 256 {
		return "", false
	}

	checked := trimmed
	// A descriptor ends in a semicolon, yet it is a name and no statement in any language.
	if body, ok := descriptorBody(strings.Trim(trimmed, "`\"'")); ok {
		checked = body
	}
	if strings.ContainsAny(checked, "{}();=\n\r\t ") {
		return "", false
	}
	return Normalize(trimmed)
}
