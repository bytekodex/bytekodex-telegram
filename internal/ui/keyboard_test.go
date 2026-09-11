package ui

import (
	"strings"
	"testing"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/render"
	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
	"github.com/go-telegram/bot/models"
)

// testCatalog is a small stand-in for the resolved lock, so these tests do not depend on which
// versions happen to be pinned today.
func testCatalog() *toolchain.Catalog {
	return toolchain.FromLock(&toolchain.Lock{
		JDK: []toolchain.LockedJDK{
			{Major: 8, ReleaseFloor: 8, ReleaseStatus: "ga"},
			{Major: 25, ReleaseFloor: 8, ReleaseFlag: true, ReleaseStatus: "ga"},
		},
		Kotlin: []toolchain.LockedTool{
			{Version: "1.9.25", JVMTargetMax: "21", JDK: 21},
			{Version: "2.4.20", JVMTargetMax: "25", JDK: 25, Default: true},
		},
		Groovy: []toolchain.LockedTool{
			{Version: "5.1.2", JVMTargetMax: "25", JDK: 25, Default: true},
		},
	})
}

func TestEncodeDecodeRoundTrips(t *testing.T) {
	cases := []Callback{
		{Action: ActionCompile},
		{Action: ActionSetLanguage, Value: "kotlin"},
		{Action: ActionOpen, Value: string(PanelView)},
	}
	for _, want := range cases {
		got, err := Decode(Encode(want.Action, want.Value))
		if err != nil {
			t.Fatalf("Decode(Encode(%+v)): %v", want, err)
		}
		if got != want {
			t.Errorf("round trip gave %+v, want %+v", got, want)
		}
	}
}

func TestDecodeRejectsForeignCallbacks(t *testing.T) {
	for _, data := range []string{"", "go", "other:go", "bk"} {
		if _, err := Decode(data); err == nil {
			t.Errorf("Decode(%q) accepted a callback that is not ours", data)
		}
	}
}

// Every button the bot can build has to fit Telegram's 64-byte callback limit. Encode panics
// past it, so walking the whole catalog here is what keeps that panic out of production.
func TestEveryCallbackFitsTheLimit(t *testing.T) {
	store := session.NewStore(0)
	catalog := testCatalog()
	for _, language := range catalog.Languages() {
		s := store.Start(1, nil, detect.Guess{Language: language})
		chain, _ := catalog.For(language)
		for _, release := range chain.Releases {
			store.Update(1, func(s *session.Session) { s.ReleaseID = release.ID })
			for _, panel := range []Panel{PanelMain, PanelLanguage, PanelRelease, PanelTarget, PanelView} {
				for _, row := range Keyboard(catalog, s, panel, nil).InlineKeyboard {
					for _, button := range row {
						if len(button.CallbackData) > maxCallbackData {
							t.Errorf("%q carries %d bytes of callback data", button.Text, len(button.CallbackData))
						}
					}
				}
			}
		}
	}
}

func TestMainPanelOffersCompileOnlyForAKnownLanguage(t *testing.T) {
	store := session.NewStore(0)

	unknown := store.Start(1, nil, detect.Guess{Language: detect.Unknown})
	if hasButton(t, Keyboard(testCatalog(), unknown, PanelMain, nil), "Compile") {
		t.Error("offered to compile a snippet with no language")
	}

	known := store.Start(2, nil, detect.Guess{Language: detect.Kotlin, Confident: true})
	if !hasButton(t, Keyboard(testCatalog(), known, PanelMain, nil), "Compile") {
		t.Error("no Compile button for Kotlin")
	}
}

func TestCaptionStatesTheGuessWithoutAsking(t *testing.T) {
	store := session.NewStore(0)

	confident := store.Start(1, nil, detect.Guess{Language: detect.Java, Confident: true})
	if caption := Caption(testCatalog(), confident); !strings.Contains(caption, "Looks like Java") {
		t.Errorf("caption = %q", caption)
	}

	unsure := store.Start(2, nil, detect.Guess{Language: detect.Groovy})
	if caption := Caption(testCatalog(), unsure); !strings.Contains(caption, "Might be Groovy") {
		t.Errorf("caption = %q", caption)
	}

	none := store.Start(3, nil, detect.Guess{})
	if caption := Caption(testCatalog(), none); strings.Contains(caption, "?") {
		t.Errorf("caption asks a question: %q", caption)
	}
}

func TestViewToggleSurvivesARoundTrip(t *testing.T) {
	for _, view := range views {
		flag, ok := ViewByID(view.ID)
		if !ok || flag != view.Flag {
			t.Errorf("ViewByID(%q) = %v, %v", view.ID, flag, ok)
		}
	}
	if _, ok := ViewByID("nonsense"); ok {
		t.Error("ViewByID accepted an unknown id")
	}
}

func TestViewSummaryReadsAsEnglish(t *testing.T) {
	if got := viewSummary(render.ViewMethods); got != "methods only" {
		t.Errorf("viewSummary(methods) = %q", got)
	}
	if got := viewSummary(render.ViewMethods | render.ViewLocals); !strings.Contains(got, "locals") {
		t.Errorf("viewSummary = %q, want locals mentioned", got)
	}
}

// fakeStock stands in for a machine where only some of the sysclasses images were extracted.
type fakeStock []int

func (f fakeStock) Majors() []int { return f }

func TestAVersionPanelForASystemClassOnlyOffersStockedVersions(t *testing.T) {
	catalog := testCatalog()
	store := session.NewStore(0)
	s := store.Start(1, nil, detect.Guess{Language: detect.Java, Confident: true})
	store.Update(1, func(s *session.Session) { s.Query = "java.lang.String" })
	s = store.Get(1)

	// The catalog knows 8 and 25; the store only has 8, so 25 must not be offered for a lookup.
	if got := countReleaseButtons(Keyboard(catalog, s, PanelRelease, fakeStock{8})); got != 1 {
		t.Errorf("offered %d versions for a lookup, want only the stocked one", got)
	}

	// While compiling there is a compiler behind every button, so the whole catalog is on offer.
	store.Update(1, func(s *session.Session) { s.Query = "" })
	if got := countReleaseButtons(Keyboard(catalog, store.Get(1), PanelRelease, fakeStock{8})); got != 2 {
		t.Errorf("offered %d versions to compile against, want the whole catalog", got)
	}
}

// Twenty-two Java releases one per row would be a list long enough to scroll past.
func TestTheVersionPanelPacksSeveralVersionsPerRow(t *testing.T) {
	lock := &toolchain.Lock{}
	for major := 7; major <= 28; major++ {
		lock.JDK = append(lock.JDK, toolchain.LockedJDK{
			Major: major, ReleaseFloor: 8, ReleaseFlag: major >= 9, ReleaseStatus: "ga",
		})
	}
	store := session.NewStore(0)
	s := store.Start(1, nil, detect.Guess{Language: detect.Java, Confident: true})

	markup := Keyboard(toolchain.FromLock(lock), s, PanelRelease, nil)

	if got := countReleaseButtons(markup); got != 22 {
		t.Fatalf("%d version buttons, want 22", got)
	}
	rows := len(markup.InlineKeyboard) - 1 // the last row is Back
	if rows > 6 {
		t.Errorf("22 versions in %d rows, want them packed %d to a row", rows, releasesPerRow)
	}
	for _, row := range markup.InlineKeyboard {
		if len(row) > releasesPerRow {
			t.Errorf("row of %d buttons, want at most %d", len(row), releasesPerRow)
		}
	}
}

func countReleaseButtons(markup *models.InlineKeyboardMarkup) int {
	count := 0
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if decoded, err := Decode(button.CallbackData); err == nil && decoded.Action == ActionSetRelease {
				count++
			}
		}
	}
	return count
}

// hasButton matches on substring rather than exact text: a button's icon is cosmetic, and a test
// asserting "Compile" appears somewhere should not need editing every time the icon in front of
// it changes.
func hasButton(t *testing.T, markup *models.InlineKeyboardMarkup, text string) bool {
	t.Helper()
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.Text, text) {
				return true
			}
		}
	}
	return false
}
