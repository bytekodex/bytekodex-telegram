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

func TestEncodeDecodeRoundTrips(t *testing.T) {
	cases := []Callback{
		{Action: ActionCompile},
		{Action: ActionSetLanguage, Value: "kotlin"},
		{Action: ActionOpen, Value: string(PanelView)},
		{Action: ActionPage, Value: "12"},
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
	for _, language := range toolchain.Languages() {
		s := store.Start(1, nil, detect.Guess{Language: language})
		chain, _ := toolchain.For(language)
		for _, release := range chain.Releases {
			store.Update(1, func(s *session.Session) { s.ReleaseID = release.ID })
			for _, panel := range []Panel{PanelMain, PanelLanguage, PanelRelease, PanelTarget, PanelView} {
				for _, row := range Keyboard(s, panel).InlineKeyboard {
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
	if hasButton(t, Keyboard(unknown, PanelMain), "Compile") {
		t.Error("offered to compile a snippet with no language")
	}

	known := store.Start(2, nil, detect.Guess{Language: detect.Kotlin, Confident: true})
	if !hasButton(t, Keyboard(known, PanelMain), "Compile") {
		t.Error("no Compile button for Kotlin")
	}
}

func TestCaptionStatesTheGuessWithoutAsking(t *testing.T) {
	store := session.NewStore(0)

	confident := store.Start(1, nil, detect.Guess{Language: detect.Java, Confident: true})
	if caption := Caption(confident); !strings.Contains(caption, "Looks like Java") {
		t.Errorf("caption = %q", caption)
	}

	unsure := store.Start(2, nil, detect.Guess{Language: detect.Groovy})
	if caption := Caption(unsure); !strings.Contains(caption, "Might be Groovy") {
		t.Errorf("caption = %q", caption)
	}

	none := store.Start(3, nil, detect.Guess{})
	if caption := Caption(none); strings.Contains(caption, "?") {
		t.Errorf("caption asks a question: %q", caption)
	}
}

func TestPagerAppearsOnlyWhenThereIsSomewhereToGo(t *testing.T) {
	if Pager(0, 1) != nil {
		t.Error("built a pager for a single page")
	}
	markup := Pager(1, 3)
	if markup == nil {
		t.Fatal("no pager for three pages")
	}
	if got := len(markup.InlineKeyboard[0]); got != 3 {
		t.Errorf("middle page has %d buttons, want back, counter and forward", got)
	}
	if first := Pager(0, 3); len(first.InlineKeyboard[0]) != 2 {
		t.Error("first page should not offer a back button")
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

func hasButton(t *testing.T, markup *models.InlineKeyboardMarkup, text string) bool {
	t.Helper()
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.Text == text {
				return true
			}
		}
	}
	return false
}
