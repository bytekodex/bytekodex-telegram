// Package bot wires Telegram to the compiler and the renderer.
//
// One message per snippet, rewritten in place. A user pastes code, the bot says what it thinks
// the language is and shows a keyboard, and nothing is asked as a blocking question.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/compile"
	"github.com/bytekodex/bytekodex-telegram/internal/detect"
	"github.com/bytekodex/bytekodex-telegram/internal/render"
	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
	"github.com/bytekodex/bytekodex-telegram/internal/ui"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// maxImagesPerAlbum is Telegram's media group limit. Compiling one file often yields several
// classes, so hitting this is normal rather than exceptional.
const maxImagesPerAlbum = 10

// minCodeLength keeps ordinary chatter out. Anything shorter is not a snippet.
const minCodeLength = 24

// Handler holds everything the callbacks need.
type Handler struct {
	Sessions *session.Store
	Compiler compile.Compiler
	Renderer *render.Renderer
	Catalog  *toolchain.Catalog
	Log      *slog.Logger

	// MaxSourceBytes bounds one snippet.
	MaxSourceBytes int
}

// Register attaches the handlers to a bot.
func (h *Handler) Register(b *bot.Bot) {
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, h.start)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, h.start)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.CallbackQuery != nil
	}, h.callback)
	// Anything else that carries text is treated as code, which is the whole point.
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && update.Message.Text != ""
	}, h.code)
}

// greeting names the languages the catalog actually has, so it cannot promise a compiler that
// was never locked.
func (h *Handler) greeting() string {
	names := make([]string, 0, 4)
	for _, language := range h.Catalog.Languages() {
		names = append(names, language.Display())
	}

	return "Drop in some JVM source and I will show you the bytecode.\n\n" +
		"I will guess the language, you can change it, and everything else has a sensible default. " +
		strings.Join(names, ", ") + ", several versions each."
}

func (h *Handler) start(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.send(ctx, b, update.Message.Chat.ID, h.greeting())
}

func (h *Handler) code(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	code := normalizeSource(stripFence(message.Text))

	if len(code) < minCodeLength {
		return
	}
	limit := h.MaxSourceBytes
	if limit > 0 && len(code) > limit {
		h.send(ctx, b, message.Chat.ID, fmt.Sprintf("That is %d KiB of source; I top out at %d KiB.", len(code)>>10, limit>>10))
		return
	}

	guess := detect.Source(code)
	name := "Snippet" + guess.Language.Extension()
	s := h.Sessions.Start(message.Chat.ID, []session.File{{Name: name, Content: code}}, guess)

	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      s.ChatID,
		Text:        ui.Caption(h.Catalog, s),
		ReplyMarkup: ui.Keyboard(h.Catalog, s, ui.PanelMain),
	})
	if err != nil {
		h.Log.Error("sending the keyboard", "error", err)
		return
	}
	h.Sessions.Update(s.ChatID, func(s *session.Session) { s.MessageID = sent.ID })
}

func (h *Handler) callback(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	press, err := ui.Decode(query.Data)
	if err != nil {
		return
	}
	// Telegram spins the button until it is answered, so this comes first and always happens.
	defer h.answer(ctx, b, query.ID, "")

	chatID := query.Message.Message.Chat.ID
	s := h.Sessions.Get(chatID)
	if s == nil {
		h.answer(ctx, b, query.ID, "That snippet has expired — send it again.")
		return
	}

	switch press.Action {
	case ui.ActionOpen:
		h.repaint(ctx, b, s, ui.Panel(press.Value))

	case ui.ActionSetLanguage:
		s = h.Sessions.Update(chatID, func(s *session.Session) {
			s.Language = detect.Language(press.Value)
			// A different language means a different compiler, so the version and target that
			// belonged to the old one no longer mean anything.
			s.ReleaseID, s.Target = "", ""
		})
		h.repaint(ctx, b, s, ui.PanelMain)

	case ui.ActionSetRelease:
		s = h.Sessions.Update(chatID, func(s *session.Session) {
			s.ReleaseID = press.Value
			s.Target = ""
		})
		h.repaint(ctx, b, s, ui.PanelMain)

	case ui.ActionSetTarget:
		s = h.Sessions.Update(chatID, func(s *session.Session) { s.Target = press.Value })
		h.repaint(ctx, b, s, ui.PanelMain)

	case ui.ActionToggleView:
		flag, ok := ui.ViewByID(press.Value)
		if !ok {
			return
		}
		s = h.Sessions.Update(chatID, func(s *session.Session) { s.View ^= flag })
		h.repaint(ctx, b, s, ui.PanelView)

	case ui.ActionCompile:
		h.compileAndSend(ctx, b, s)

	case ui.ActionPage:
		page, err := strconv.Atoi(press.Value)
		if err != nil {
			return
		}
		h.Sessions.Update(chatID, func(s *session.Session) { s.Page = page })

	case ui.ActionCancel:
		h.Sessions.Delete(chatID)
		h.edit(ctx, b, chatID, s.MessageID, "Dropped.", nil)
	}
}

func (h *Handler) compileAndSend(ctx context.Context, b *bot.Bot, s *session.Session) {
	chain, ok := h.Catalog.For(s.Language)
	if !ok {
		h.edit(ctx, b, s.ChatID, s.MessageID, "Pick a language first.", ui.Keyboard(h.Catalog, s, ui.PanelLanguage))
		return
	}
	release := chain.DefaultRelease()
	if chosen, found := chain.Release(s.ReleaseID); found {
		release = chosen
	}
	target := s.Target
	if target == "" {
		target = release.DefaultTarget()
	}

	h.edit(ctx, b, s.ChatID, s.MessageID, "Compiling with "+release.Label+"…", nil)

	result, err := h.Compiler.Compile(ctx, chain, release, target, s.Files)
	if err != nil {
		h.reportCompileFailure(ctx, b, s, err)
		return
	}

	images := make([]*render.Image, 0, len(result.Artifacts))
	// Every image holds a pooled buffer, so they all go back even if the upload fails.
	defer func() {
		for _, image := range images {
			image.Close()
		}
	}()

	media := make([]models.InputMedia, 0, maxImagesPerAlbum)
	for _, artifact := range result.Artifacts {
		if len(media) == maxImagesPerAlbum {
			break
		}
		image, err := h.Renderer.Render(artifact.Bytes, render.Options{
			Platform: render.JVM,
			Kind:     render.Binary,
			View:     s.View | render.ViewMethods,
			Corner:   20,
		})
		if err != nil {
			h.Log.Warn("rendering a class", "class", artifact.Name, "error", err)
			continue
		}
		images = append(images, image)
		// Documents rather than photos. sendPhoto re-encodes on Telegram's servers, and JPEG on
		// small sharp glyphs is exactly the wrong trade: the whole product here is legible text.
		// A document keeps the bytes, and the width+height cap does not apply to it either.
		//
		// The PNG is streamed straight out of the buffer Rust wrote it into: no temporary file,
		// and no second copy of the bytes.
		name := attachmentName(artifact.Name)
		media = append(media, &models.InputMediaDocument{
			Media:           "attach://" + name,
			MediaAttachment: image,
			Caption:         artifact.Name,
		})
	}

	if len(media) == 0 {
		h.edit(ctx, b, s.ChatID, s.MessageID, "Compiled, but nothing could be rendered.", ui.Keyboard(h.Catalog, s, ui.PanelMain))
		return
	}

	if err := h.sendImages(ctx, b, s, media); err != nil {
		h.Log.Error("sending images", "error", err)
		h.edit(ctx, b, s.ChatID, s.MessageID, "Rendered, but Telegram would not take the images.", ui.Keyboard(h.Catalog, s, ui.PanelMain))
		return
	}

	summary := fmt.Sprintf("%s · %d class(es) in %s", toolchain.Describe(s.Language, release, target),
		len(result.Artifacts), result.Took.Round(time.Millisecond))
	if skipped := len(result.Artifacts) - len(media); skipped > 0 {
		summary += fmt.Sprintf(" · %d not shown", skipped)
	}
	h.edit(ctx, b, s.ChatID, s.MessageID, summary, ui.Keyboard(h.Catalog, s, ui.PanelMain))
}

// sendImages uses an album for several classes and a single document for one, because a media
// group of one is rejected.
func (h *Handler) sendImages(ctx context.Context, b *bot.Bot, s *session.Session, media []models.InputMedia) error {
	if len(media) == 1 {
		only := media[0].(*models.InputMediaDocument)
		_, err := b.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID:   s.ChatID,
			Document: &models.InputFileUpload{Filename: attachmentName(only.Caption), Data: only.MediaAttachment},
			Caption:  only.Caption,
		})
		return err
	}
	_, err := b.SendMediaGroup(ctx, &bot.SendMediaGroupParams{ChatID: s.ChatID, Media: media})
	return err
}

func (h *Handler) repaint(ctx context.Context, b *bot.Bot, s *session.Session, panel ui.Panel) {
	if s == nil {
		return
	}
	h.edit(ctx, b, s.ChatID, s.MessageID, ui.Caption(h.Catalog, s), ui.Keyboard(h.Catalog, s, panel))
}

func (h *Handler) edit(ctx context.Context, b *bot.Bot, chatID int64, messageID int, text string, markup models.ReplyMarkup) {
	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: markup,
	})
	// Editing a message to what it already says is an error Telegram reports and we do not care
	// about, which happens whenever a user taps the button they are already on.
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		h.Log.Warn("editing the keyboard", "error", err)
	}
}

func (h *Handler) send(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		h.Log.Warn("sending a message", "error", err)
	}
}

func (h *Handler) answer(ctx context.Context, b *bot.Bot, queryID, text string) {
	_, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: queryID,
		Text:            text,
	})
	if err != nil {
		h.Log.Debug("answering a callback", "error", err)
	}
}

// reportCompileFailure shows the compiler's own words, rendered rather than pasted.
//
// A diagnostic is the second most common thing a user sees here, and a chat message flattens
// everything that makes one readable: which file, which line, what the caret pointed at. So it
// goes through the same renderer as the bytecode would have, and only falls back to text if even
// that fails.
func (h *Handler) reportCompileFailure(ctx context.Context, b *bot.Bot, s *session.Session, failure error) {
	summary, output := compileMessage(failure)

	if output == "" {
		h.edit(ctx, b, s.ChatID, s.MessageID, summary, ui.Keyboard(h.Catalog, s, ui.PanelMain))
		return
	}

	image, renderErr := h.Renderer.Render([]byte(output), render.Options{
		Kind:   render.Diagnostic,
		Corner: 20,
	})
	if renderErr != nil {
		h.Log.Warn("rendering a diagnostic", "error", renderErr)
		// Better a wall of text than nothing at all, but truncated: a generated file can produce
		// hundreds of diagnostics and Telegram rejects a message over 4096 characters.
		h.edit(ctx, b, s.ChatID, s.MessageID, summary+"\n\n"+firstLines(output, 25),
			ui.Keyboard(h.Catalog, s, ui.PanelMain))
		return
	}
	defer image.Close()

	_, err := b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   s.ChatID,
		Document: &models.InputFileUpload{Filename: "diagnostics.png", Data: image},
		Caption:  summary,
	})
	if err != nil {
		h.Log.Error("sending a diagnostic", "error", err)
	}
	h.edit(ctx, b, s.ChatID, s.MessageID, summary, ui.Keyboard(h.Catalog, s, ui.PanelMain))
}

// compileMessage separates the one-line summary from the compiler's output, so the summary can
// be a caption and the output can be an image.
func compileMessage(err error) (summary, output string) {
	var failed *compile.Failed
	switch {
	case errors.Is(err, compile.ErrTimeout):
		return "That took too long to compile.", ""
	case errors.As(err, &failed):
		if failed.Output == "" {
			return "The compiler produced neither classes nor a message.", ""
		}
		return "The compiler was not happy.", failed.Output
	default:
		return "Something went wrong while compiling.", ""
	}
}

// firstLines is the fallback for when even rendering failed.
func firstLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n")
}

// normalizeSource cleans up pasted text before anything looks at it.
//
// Trailing whitespace is not cosmetic here: the image is as wide as its longest line, so a line
// padded with spaces makes every page wider and slower to render for no visible reason. Carriage
// returns matter for the same reason — a stray \r would be measured as a glyph.
func normalizeSource(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\v\f\u00a0")
	}

	// Blank lines at either end contribute rows to the page and nothing to the reader.
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// stripFence removes a Markdown code fence, which is how most people paste code into Telegram.
func stripFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return text
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	// The opening fence may name a language, which is a hint we deliberately ignore: it is wrong
	// often enough, and the detector reads the code itself.
	if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
		trimmed = trimmed[newline+1:]
	}
	return strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
}

// attachmentName turns a class path into something Telegram accepts as a filename.
func attachmentName(class string) string {
	name := strings.TrimSuffix(class, ".class")
	name = strings.NewReplacer("/", ".", `\`, ".").Replace(name)
	if name == "" {
		name = "bytecode"
	}
	return name + ".png"
}
