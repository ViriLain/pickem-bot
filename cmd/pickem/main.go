// Command pickem runs the pick'em Discord bot. Configuration comes from the
// environment; see README.md.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // PICKEM_TZ must load in a scratch image with no zoneinfo

	_ "modernc.org/sqlite"

	"github.com/ViriLain/pickem-bot/internal/card"
	"github.com/ViriLain/pickem-bot/internal/commands"
	"github.com/ViriLain/pickem-bot/internal/discord"
	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/internal/espn"
	"github.com/ViriLain/pickem-bot/internal/scheduler"
	"github.com/ViriLain/pickem-bot/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(); err != nil {
		log.Fatalf("pickem: %v", err)
	}
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", envTZ, cfg.TZ, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Store.
	db, err := sql.Open("sqlite", sqliteDSN(cfg.DBPath))
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBPath, err)
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	st := store.New(db)
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("close store: %v", err)
		}
	}()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBPath, err)
	}
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database %s: %w", cfg.DBPath, err)
	}

	// Bot (not connected yet), cards, scheduler, commands.
	bot, err := discord.New(cfg.Token, cfg.GuildID, cfg.AdminRoleID, cfg.AdminChannelID)
	if err != nil {
		return err
	}
	clock := domain.SystemClock{}
	cards := card.New(st, clock, bot)
	provider := espn.New(&http.Client{Timeout: 15 * time.Second}, "")
	sched := scheduler.New(st, provider, cards, bot, clock, scheduler.Config{})
	cmds := commands.New(st, clock, cards, sched, loc)

	if err := bot.Start(ctx, cards, cmds); err != nil {
		return err
	}
	defer func() {
		if err := bot.Close(); err != nil {
			log.Printf("close discord session: %v", err)
		}
	}()
	log.Printf("pickem running: guild %s, db %s, time zone %s", cfg.GuildID, cfg.DBPath, loc)

	err = sched.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	log.Printf("shutting down")
	return nil
}
