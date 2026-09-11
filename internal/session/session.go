// Package session holds what a chat has told the bot so far.
//
// State lives in memory and expires. A dropped session costs a user one re-paste, whereas a
// persistent store would mean holding strangers' source code on disk indefinitely.
package session

import (
	"sync"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/render"
)

// File is one source file a user sent. Compiling often produces several classes from one file,
// and users legitimately send several files, so this is always a list.
type File struct {
	Name    string
	Content string
}

// Session is the state behind one keyboard.
type Session struct {
	ChatID    int64
	MessageID int
	Files     []File
	// Query is set instead of Files when the user asked for a JDK class by name. Those need no
	// compiler at all: the class file already exists inside every JDK.
	Query string

	Language  detect.Language
	Detected  detect.Guess
	ReleaseID string
	Target    string
	View      render.View

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TotalBytes is the size of everything the user sent, which the size limit is checked against.
func (s *Session) TotalBytes() int {
	total := 0
	for _, f := range s.Files {
		total += len(f.Content)
	}
	return total
}

// Store keeps sessions per chat, one at a time: a second snippet replaces the first rather than
// stacking up keyboards that all look alike.
type Store struct {
	mu       sync.Mutex
	byChat   map[int64]*Session
	lifetime time.Duration
}

func NewStore(lifetime time.Duration) *Store {
	if lifetime <= 0 {
		lifetime = 30 * time.Minute
	}
	return &Store{byChat: make(map[int64]*Session), lifetime: lifetime}
}

// Start replaces whatever the chat had with a fresh session seeded from a guess.
func (s *Store) Start(chatID int64, files []File, guess detect.Guess) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	session := &Session{
		ChatID:    chatID,
		Files:     files,
		Language:  guess.Language,
		Detected:  guess,
		View:      render.ViewMethods,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.byChat[chatID] = session
	return session
}

// Get returns the chat's session, or nil when there is none or it has expired. An expired
// session is dropped rather than revived, so a stale keyboard reports itself instead of acting
// on code the user has forgotten about.
func (s *Store) Get(chatID int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.byChat[chatID]
	if !ok {
		return nil
	}
	if time.Since(session.UpdatedAt) > s.lifetime {
		delete(s.byChat, chatID)
		return nil
	}
	return session
}

// Update applies a change under the lock and refreshes the expiry, so that a user still tapping
// buttons does not have the session pulled out from under them.
func (s *Store) Update(chatID int64, apply func(*Session)) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.byChat[chatID]
	if !ok {
		return nil
	}
	apply(session)
	session.UpdatedAt = time.Now()
	return session
}

func (s *Store) Delete(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byChat, chatID)
}

// Sweep drops expired sessions and returns how many went. Get already expires lazily, but a chat
// that never comes back would otherwise hold its source code for as long as the process lives.
func (s *Store) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for chatID, session := range s.byChat {
		if time.Since(session.UpdatedAt) > s.lifetime {
			delete(s.byChat, chatID)
			removed++
		}
	}
	return removed
}

// SweepEvery runs Sweep until the channel closes.
func (s *Store) SweepEvery(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.Sweep()
		case <-done:
			return
		}
	}
}
