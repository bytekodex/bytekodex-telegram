// Command bot runs the Bytekodex Telegram bot.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/bytekodex/bytekodex-telegram/internal/bot"
	"github.com/bytekodex/bytekodex-telegram/internal/compile"
	"github.com/bytekodex/bytekodex-telegram/internal/render"
	"github.com/bytekodex/bytekodex-telegram/internal/session"
	"github.com/bytekodex/bytekodex-telegram/internal/toolchain"
	tgbot "github.com/go-telegram/bot"
)

func main() {
	if err := run(); err != nil {
		slog.Error("shutting down", "error", err)
		os.Exit(1)
	}
}

type config struct {
	token          string
	fontPath       string
	fontSize       float32
	depsVolume     string
	containerCmd   string
	lockPath       string
	sessionTTL     time.Duration
	maxSourceBytes int
	workers        int
}

func load() (config, error) {
	c := config{
		token:        os.Getenv("BYTEKODEX_TELEGRAM_TOKEN"),
		// Fira Code at 40, the same as the old painter used. It is the look the project already
		// had, and the size is generous on purpose: these images get scaled down in a chat, and
		// text that was rendered small and then shrunk further is what makes bytecode unreadable.
		fontPath:     env("BYTEKODEX_FONT", "/usr/share/fonts/truetype/firacode/FiraCode-Regular.ttf"),
		depsVolume:   os.Getenv("BYTEKODEX_DEPS"),
		containerCmd: env("BYTEKODEX_CONTAINER_RUNTIME", "docker"),
		lockPath:     env("BYTEKODEX_TOOLCHAIN_LOCK", "toolchains/lock.json"),
		// One renderer per core: each owns a glyph cache, and the cache is the reason they cannot
		// simply be shared.
		workers: runtime.GOMAXPROCS(0),
		fontSize:       40,
		sessionTTL:     30 * time.Minute,
		maxSourceBytes: 256 << 10,
	}
	if c.token == "" {
		return config{}, errors.New("BYTEKODEX_TELEGRAM_TOKEN is not set")
	}
	if size := os.Getenv("BYTEKODEX_FONT_SIZE"); size != "" {
		parsed, err := strconv.ParseFloat(size, 32)
		if err != nil {
			return config{}, fmt.Errorf("BYTEKODEX_FONT_SIZE: %w", err)
		}
		c.fontSize = float32(parsed)
	}
	if ttl := os.Getenv("BYTEKODEX_SESSION_TTL"); ttl != "" {
		parsed, err := time.ParseDuration(ttl)
		if err != nil {
			return config{}, fmt.Errorf("BYTEKODEX_SESSION_TTL: %w", err)
		}
		c.sessionTTL = parsed
	}
	return c, nil
}

func run() error {
	config, err := load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	font, err := os.ReadFile(config.fontPath)
	if err != nil {
		return fmt.Errorf("reading the font: %w", err)
	}
	renderer, err := render.New(font, config.fontSize, config.workers)
	if err != nil {
		return fmt.Errorf("starting the renderer: %w", err)
	}
	defer renderer.Close()

	// The catalog is data, not code: which compilers exist comes from the resolved lock, so a new
	// Kotlin release reaches users without a rebuild.
	lock, err := toolchain.LoadLock(config.lockPath)
	if err != nil {
		return err
	}
	catalog := toolchain.FromLock(lock)
	if len(catalog.Languages()) == 0 {
		return fmt.Errorf("%s resolves no toolchains at all", config.lockPath)
	}

	sessions := session.NewStore(config.sessionTTL)
	done := make(chan struct{})
	defer close(done)
	go sessions.SweepEvery(config.sessionTTL/2, done)

	handler := &bot.Handler{
		Sessions: sessions,
		Compiler: &compile.Container{
			Runtime:    config.containerCmd,
			Limits:     compile.DefaultLimits(),
			DepsVolume: config.depsVolume,
		},
		Renderer:       renderer,
		Catalog:        catalog,
		Log:            log,
		MaxSourceBytes: config.maxSourceBytes,
	}

	b, err := tgbot.New(config.token)
	if err != nil {
		return fmt.Errorf("connecting to Telegram: %w", err)
	}
	handler.Register(b)

	// Long polling holds a connection open, so the context is what actually stops it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("listening", "workers", config.workers, "font", config.fontPath)
	b.Start(ctx)
	log.Info("stopped", "opcodes_decoded", render.OpcodesDecoded())
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
