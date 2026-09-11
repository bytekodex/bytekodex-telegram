package session

import (
	"strconv"
	"testing"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
)

func files() []File {
	return []File{{Name: "Main.java", Content: "class Main {}"}}
}

func TestStartReplacesThePreviousSnippet(t *testing.T) {
	store := NewStore(time.Minute)
	store.Start(1, files(), detect.Guess{Language: detect.Java, Confident: true})
	store.Start(1, []File{{Name: "Other.kt", Content: "fun main() {}"}}, detect.Guess{Language: detect.Kotlin})

	session := store.Get(1)
	if session == nil {
		t.Fatal("no session")
	}
	if len(session.Files) != 1 || session.Files[0].Name != "Other.kt" {
		t.Errorf("kept the old files: %+v", session.Files)
	}
	if session.Language != detect.Kotlin {
		t.Errorf("Language = %q, want kotlin", session.Language)
	}
}

func TestExpiredSessionIsGoneNotRevived(t *testing.T) {
	store := NewStore(time.Nanosecond)
	store.Start(7, files(), detect.Guess{Language: detect.Java})
	time.Sleep(time.Millisecond)

	if session := store.Get(7); session != nil {
		t.Fatal("expired session was returned")
	}
	if session := store.Update(7, func(s *Session) { s.Target = "8" }); session != nil {
		t.Error("expired session accepted an update")
	}
}

func TestUpdateRefreshesTheExpiry(t *testing.T) {
	store := NewStore(50 * time.Millisecond)
	store.Start(2, files(), detect.Guess{Language: detect.Java})

	for i := range 5 {
		time.Sleep(20 * time.Millisecond)
		target := strconv.Itoa(i)
		if store.Update(2, func(s *Session) { s.Target = target }) == nil {
			t.Fatal("session expired while it was being used")
		}
	}
	if session := store.Get(2); session == nil || session.Target != "4" {
		t.Errorf("session = %+v, want target 4", session)
	}
}

func TestSweepDropsWhatNobodyCameBackFor(t *testing.T) {
	store := NewStore(time.Millisecond)
	store.Start(1, files(), detect.Guess{Language: detect.Java})
	store.Start(2, files(), detect.Guess{Language: detect.Java})
	time.Sleep(5 * time.Millisecond)

	store.Start(3, files(), detect.Guess{Language: detect.Java})
	if removed := store.Sweep(); removed != 2 {
		t.Errorf("Sweep() = %d, want 2", removed)
	}
	if store.Get(3) == nil {
		t.Error("swept a live session")
	}
}

func TestTotalBytesCountsEveryFile(t *testing.T) {
	store := NewStore(time.Minute)
	session := store.Start(1, []File{
		{Name: "A.java", Content: "12345"},
		{Name: "B.java", Content: "123"},
	}, detect.Guess{Language: detect.Java})

	if got := session.TotalBytes(); got != 8 {
		t.Errorf("TotalBytes() = %d, want 8", got)
	}
}
