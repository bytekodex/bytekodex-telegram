// Package ui builds the one keyboard the bot shows.
//
// The whole interaction is a single message that rewrites itself. A user drops in code, sees what
// was detected, and either presses Compile straight away or opens one of the pickers first.
// Nothing blocks on a question, and no state lives in the message text.
package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/render"
	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
	"github.com/go-telegram/bot/models"
)

// Action is what a button does.
type Action string

const (
	// ActionOpen switches the keyboard to a picker without changing anything.
	ActionOpen Action = "open"
	// ActionSetLanguage, ActionSetRelease and ActionSetTarget record a choice and go back.
	ActionSetLanguage Action = "lang"
	ActionSetRelease  Action = "rel"
	ActionSetTarget   Action = "tgt"
	// ActionToggleView flips one section of the dump on or off.
	ActionToggleView Action = "view"
	// ActionCompile does the work.
	ActionCompile Action = "go"
	// ActionPage moves between rendered pages.
	ActionPage Action = "page"
	// ActionCancel drops the session.
	ActionCancel Action = "x"
)

// Panel names which picker is on screen.
type Panel string

const (
	PanelMain     Panel = ""
	PanelLanguage Panel = "lang"
	PanelRelease  Panel = "rel"
	PanelTarget   Panel = "tgt"
	PanelView     Panel = "view"
)

// prefix keeps our callbacks apart from anything else in a group chat.
const prefix = "bk"

// Callback is a decoded button press.
type Callback struct {
	Action Action
	Value  string
}

// ErrNotOurs reports a callback that belongs to another bot or another version of this one.
var ErrNotOurs = errors.New("ui: callback is not ours")

// maxCallbackData is Telegram's limit, in bytes. Exceeding it is rejected at send time, which
// would break the keyboard for everyone rather than for one button, so it is checked here.
const maxCallbackData = 64

// Encode builds callback data. Values are short IDs, never labels, so a reworded label cannot
// break a keyboard that is already on someone's screen.
func Encode(action Action, value string) string {
	data := prefix + ":" + string(action)
	if value != "" {
		data += ":" + value
	}
	if len(data) > maxCallbackData {
		// Truncating would produce a button that silently does the wrong thing.
		panic(fmt.Sprintf("ui: callback data %q exceeds %d bytes", data, maxCallbackData))
	}
	return data
}

// Decode parses callback data back.
func Decode(data string) (Callback, error) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 2 || parts[0] != prefix {
		return Callback{}, ErrNotOurs
	}
	callback := Callback{Action: Action(parts[1])}
	if len(parts) == 3 {
		callback.Value = parts[2]
	}
	return callback, nil
}

// views are the toggleable sections, in the order they appear.
var views = []struct {
	Flag  render.View
	Label string
	ID    string
}{
	{render.ViewConstantPool, "Constant pool", "pool"},
	{render.ViewLocals, "Locals", "locals"},
	{render.ViewStackMap, "Stack map", "stack"},
	{render.ViewLineNumbers, "Line numbers", "lines"},
	{render.ViewAttributes, "Attributes", "attrs"},
}

// ViewByID maps a button back to its flag.
func ViewByID(id string) (render.View, bool) {
	for _, v := range views {
		if v.ID == id {
			return v.Flag, true
		}
	}
	return 0, false
}

// Caption is the text above the keyboard. It states what was detected rather than asking about
// it, because a question the user has to answer before anything happens is the thing to avoid.
func Caption(catalog *toolchain.Catalog, s *session.Session) string {
	var b strings.Builder

	switch {
	case s.Language == detect.Unknown:
		b.WriteString("I could not tell what language this is — pick one below.")
	case s.Detected.Confident && s.Language == s.Detected.Language:
		fmt.Fprintf(&b, "Looks like %s.", s.Language.Display())
	case s.Language == s.Detected.Language:
		fmt.Fprintf(&b, "Might be %s — change it if I got that wrong.", s.Language.Display())
	default:
		fmt.Fprintf(&b, "%s, as you asked.", s.Language.Display())
	}

	if chain, ok := catalog.For(s.Language); ok {
		release := chain.DefaultRelease()
		if chosen, found := chain.Release(s.ReleaseID); found {
			release = chosen
		}
		target := s.Target
		if target == "" {
			target = release.DefaultTarget()
		}
		fmt.Fprintf(&b, "\n%s", toolchain.Describe(s.Language, release, target))
	}

	if n := len(s.Files); n > 1 {
		fmt.Fprintf(&b, "\n%d files", n)
	}
	return b.String()
}

// Stock reports which JDK versions have system classes stored for them. It is an interface rather
// than the store itself so that this package keeps knowing nothing about how classes are stored.
type Stock interface {
	Majors() []int
}

// Keyboard builds the markup for a panel. stock may be nil, which offers every version.
func Keyboard(catalog *toolchain.Catalog, s *session.Session, panel Panel, stock Stock) *models.InlineKeyboardMarkup {
	switch panel {
	case PanelLanguage:
		return languagePanel(catalog, s)
	case PanelRelease:
		return releasePanel(catalog, s, stock)
	case PanelTarget:
		return targetPanel(catalog, s)
	case PanelView:
		return viewPanel(s)
	default:
		return mainPanel(catalog, s)
	}
}

func mainPanel(catalog *toolchain.Catalog, s *session.Session) *models.InlineKeyboardMarkup {
	chain, known := catalog.For(s.Language)
	release := chain.DefaultRelease()
	if chosen, ok := chain.Release(s.ReleaseID); ok {
		release = chosen
	}
	target := s.Target
	if target == "" {
		target = release.DefaultTarget()
	}

	rows := [][]models.InlineKeyboardButton{{
		{Text: "Language: " + s.Language.Display(), CallbackData: Encode(ActionOpen, string(PanelLanguage))},
	}}
	if known {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Version: " + release.Label, CallbackData: Encode(ActionOpen, string(PanelRelease))},
			{Text: "Target: " + target, CallbackData: Encode(ActionOpen, string(PanelTarget))},
		})
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Show: " + viewSummary(s.View), CallbackData: Encode(ActionOpen, string(PanelView))},
		})
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Compile", CallbackData: Encode(ActionCompile, "")},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "Discard", CallbackData: Encode(ActionCancel, "")},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func languagePanel(catalog *toolchain.Catalog, s *session.Session) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, language := range catalog.Languages() {
		label := language.Display()
		if language == s.Language {
			label = "· " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: label, CallbackData: Encode(ActionSetLanguage, string(language))},
		})
	}
	return withBack(rows)
}

// releasesPerRow keeps the version panel readable. Every Java release from 7 to 28 is offered, and
// one button per row would be a list long enough to scroll.
const releasesPerRow = 4

func releasePanel(catalog *toolchain.Catalog, s *session.Session, stock Stock) *models.InlineKeyboardMarkup {
	chain, ok := catalog.For(s.Language)
	if !ok {
		return mainPanel(catalog, s)
	}

	// While a system class is on screen there is nothing to compile, only bytes to read off disk,
	// so the only versions worth offering are the ones the store actually holds.
	var stocked []int
	if s.Query != "" && stock != nil {
		stocked = stock.Majors()
	}

	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, release := range chain.Releases {
		if stocked != nil && !slices.Contains(stocked, release.Major) {
			continue
		}

		label := release.Label
		if release.ID == s.ReleaseID {
			label = "· " + label
		}
		row = append(row, models.InlineKeyboardButton{
			Text: label, CallbackData: Encode(ActionSetRelease, release.ID),
		})
		if len(row) == releasesPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return withBack(rows)
}

func targetPanel(catalog *toolchain.Catalog, s *session.Session) *models.InlineKeyboardMarkup {
	chain, ok := catalog.For(s.Language)
	if !ok {
		return mainPanel(catalog, s)
	}
	release := chain.DefaultRelease()
	if chosen, found := chain.Release(s.ReleaseID); found {
		release = chosen
	}

	// Targets go on one row: there are two or three of them and they are two characters wide.
	row := make([]models.InlineKeyboardButton, 0, len(release.Targets))
	for _, target := range release.Targets {
		label := target
		if target == s.Target {
			label = "· " + label
		}
		row = append(row, models.InlineKeyboardButton{Text: label, CallbackData: Encode(ActionSetTarget, target)})
	}
	return withBack([][]models.InlineKeyboardButton{row})
}

func viewPanel(s *session.Session) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, view := range views {
		mark := "off"
		if s.View&view.Flag != 0 {
			mark = "on"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: fmt.Sprintf("%s — %s", view.Label, mark), CallbackData: Encode(ActionToggleView, view.ID)},
		})
	}
	return withBack(rows)
}

// Pager is the strip under a rendered page. It is omitted for a single page rather than shown
// disabled, since a button that does nothing is worse than no button.
func Pager(page, total int) *models.InlineKeyboardMarkup {
	if total <= 1 {
		return nil
	}
	row := []models.InlineKeyboardButton{}
	if page > 0 {
		row = append(row, models.InlineKeyboardButton{Text: "‹", CallbackData: Encode(ActionPage, fmt.Sprint(page-1))})
	}
	row = append(row, models.InlineKeyboardButton{
		Text:         fmt.Sprintf("%d / %d", page+1, total),
		CallbackData: Encode(ActionPage, fmt.Sprint(page)),
	})
	if page+1 < total {
		row = append(row, models.InlineKeyboardButton{Text: "›", CallbackData: Encode(ActionPage, fmt.Sprint(page+1))})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

func withBack(rows [][]models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "Back", CallbackData: Encode(ActionOpen, string(PanelMain))},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func viewSummary(view render.View) string {
	var on []string
	for _, v := range views {
		if view&v.Flag != 0 {
			on = append(on, strings.ToLower(v.Label))
		}
	}
	if len(on) == 0 {
		return "methods only"
	}
	return "methods, " + strings.Join(on, ", ")
}
