// Package scheduler keeps matches moving through their lifecycle: it pulls
// schedules from the provider, opens cards ahead of kickoff, locks picks,
// watches started games and settles them when the provider reports a result.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/internal/game"
)

// Cards is implemented by card.Service.
type Cards interface {
	Open(ctx context.Context, matchID int64) error
	Refresh(ctx context.Context, matchID int64) error
	AnnounceMove(ctx context.Context, matchID int64) error
}

// Alerter posts to the admin channel.
type Alerter interface {
	Alert(ctx context.Context, text string) error
}

// Config tunes the loop. Zero fields take the documented defaults.
type Config struct {
	SyncEvery   time.Duration // default 6h
	HorizonDays int           // default 7
	Tick        time.Duration // default 60s
	StaleAfter  time.Duration // default 6h: alert when a started match has no final
	Recheck     time.Duration // default 10m: re-read kickoff this long before lock
}

// Defaults for zero Config fields.
const (
	DefaultSyncEvery   = 6 * time.Hour
	DefaultHorizonDays = 7
	DefaultTick        = 60 * time.Second
	DefaultStaleAfter  = 6 * time.Hour
	DefaultRecheck     = 10 * time.Minute
)

// After this many consecutive provider failures for one match, the watcher
// only retries it every backoffEvery ticks.
const (
	backoffAfter = 3
	backoffEvery = 5
)

func (c Config) withDefaults() Config {
	if c.SyncEvery <= 0 {
		c.SyncEvery = DefaultSyncEvery
	}
	if c.HorizonDays <= 0 {
		c.HorizonDays = DefaultHorizonDays
	}
	if c.Tick <= 0 {
		c.Tick = DefaultTick
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = DefaultStaleAfter
	}
	if c.Recheck <= 0 {
		c.Recheck = DefaultRecheck
	}
	return c
}

// Scheduler drives sync, open, lock, watch and settle.
type Scheduler struct {
	store    domain.Store
	provider domain.Provider
	cards    Cards
	alerter  Alerter
	clock    domain.Clock
	cfg      Config

	// mu serialises whole passes (SyncNow, Tick) and guards the maps below.
	mu        sync.Mutex
	tickN     int
	rechecked map[int64]time.Time       // match ID -> LockAt already rechecked
	alerted   map[int64]map[string]bool // match ID -> alert reasons already sent
	failures  map[int64]int             // match ID -> consecutive provider failures
	lastTry   map[int64]int             // match ID -> tick number of last status call
}

// New returns a Scheduler. Zero Config fields take the defaults.
func New(store domain.Store, provider domain.Provider, cards Cards, alerter Alerter, clock domain.Clock, cfg Config) *Scheduler {
	return &Scheduler{
		store: store, provider: provider, cards: cards, alerter: alerter, clock: clock,
		cfg:       cfg.withDefaults(),
		rechecked: map[int64]time.Time{},
		alerted:   map[int64]map[string]bool{},
		failures:  map[int64]int{},
		lastTry:   map[int64]int{},
	}
}

// Run syncs, then ticks every cfg.Tick and syncs every cfg.SyncEvery until
// ctx is done. Errors are logged and the loop keeps going.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.SyncNow(ctx); err != nil {
		log.Printf("scheduler: sync: %v", err)
	}
	if err := s.Tick(ctx); err != nil {
		log.Printf("scheduler: tick: %v", err)
	}
	tick := time.NewTicker(s.cfg.Tick)
	defer tick.Stop()
	syncT := time.NewTicker(s.cfg.SyncEvery)
	defer syncT.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := s.Tick(ctx); err != nil {
				log.Printf("scheduler: tick: %v", err)
			}
		case <-syncT.C:
			if err := s.SyncNow(ctx); err != nil {
				log.Printf("scheduler: sync: %v", err)
			}
		}
	}
}

// SyncNow pulls every league's schedule from yesterday through today plus
// HorizonDays (UTC dates; yesterday covers ESPN's US Eastern bucketing),
// upserts each game and reconciles postponements, cancellations and moved
// kickoffs. A failing league/day is collected and the sync continues.
func (s *Scheduler) SyncNow(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now().UTC()
	leagues, err := s.store.ListLeagues(ctx, "")
	if err != nil {
		return fmt.Errorf("scheduler: list leagues: %w", err)
	}
	today := utcDay(now)
	var errs []error
	for _, l := range leagues {
		for d := -1; d <= s.cfg.HorizonDays; d++ {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			day := today.AddDate(0, 0, d)
			games, err := s.provider.Schedule(ctx, l.Path, day)
			if err != nil {
				errs = append(errs, fmt.Errorf("scheduler: schedule %s %s: %w", l.Path, day.Format("2006-01-02"), err))
				continue
			}
			for _, g := range games {
				if err := s.syncGame(ctx, l, g, now); err != nil {
					errs = append(errs, fmt.Errorf("scheduler: sync %s event %s: %w", l.Path, g.EventID, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Scheduler) syncGame(ctx context.Context, l domain.League, g domain.ProviderMatch, now time.Time) error {
	kickoff := g.Status.Kickoff.UTC()
	if g.Status.Kickoff.IsZero() {
		return errors.New("provider listed no kickoff")
	}
	comp := g.Competition
	if comp == "" {
		comp = l.Name
	}
	m, _, err := s.store.UpsertMatch(ctx, domain.Match{
		GuildID:         l.GuildID,
		LeagueID:        l.ID,
		ProviderEventID: g.EventID,
		Competition:     comp,
		Home:            g.Home,
		Away:            g.Away,
		Kickoff:         kickoff,
		LockAt:          game.LockTime(kickoff),
		State:           domain.StateScheduled,
	})
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	_, err = s.reconcile(ctx, m, g.Status, now)
	return err
}

// Tick runs one pass over open, lock, watch and settle. Each match is
// handled independently; failures are joined.
func (s *Scheduler) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tickN++
	now := s.clock.Now().UTC()
	leagues, err := s.store.ListLeagues(ctx, "")
	if err != nil {
		return fmt.Errorf("scheduler: list leagues: %w", err)
	}
	byID := make(map[int64]domain.League, len(leagues))
	for _, l := range leagues {
		byID[l.ID] = l
	}
	matches, err := s.store.ListMatchesByState(ctx,
		domain.StateScheduled, domain.StateOpen, domain.StateLocked, domain.StateLive)
	if err != nil {
		return fmt.Errorf("scheduler: list matches: %w", err)
	}
	var errs []error
	for _, m := range matches {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		l, ok := byID[m.LeagueID]
		if err := s.tickMatch(ctx, m, l, ok, now); err != nil {
			errs = append(errs, fmt.Errorf("scheduler: match %d: %w", m.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Scheduler) tickMatch(ctx context.Context, m domain.Match, l domain.League, haveLeague bool, now time.Time) error {
	if m.LockAt.IsZero() {
		m.LockAt = game.LockTime(m.Kickoff)
	}
	switch m.State {
	case domain.StateScheduled:
		if !haveLeague {
			return nil // league removed: never post a card for it
		}
		if !now.Before(m.LockAt) {
			// Too late to open: lock without a card so it is still settled.
			m.State = domain.StateLocked
			_, err := s.save(ctx, m)
			return err
		}
		if !now.Before(m.Kickoff.Add(-l.OpenLead)) {
			if err := s.cards.Open(ctx, m.ID); err != nil {
				return fmt.Errorf("open card: %w", err)
			}
		}
		return nil

	case domain.StateOpen:
		if !now.Before(m.LockAt) {
			m.State = domain.StateLocked
			_, err := s.save(ctx, m)
			return err
		}
		if haveLeague && !now.Before(m.LockAt.Add(-s.cfg.Recheck)) {
			if done, ok := s.rechecked[m.ID]; !ok || !done.Equal(m.LockAt) {
				return s.recheck(ctx, m, l, now)
			}
		}
		return nil

	case domain.StateLocked, domain.StateLive:
		if now.Before(m.Kickoff) {
			return nil
		}
		return s.watch(ctx, m, l, haveLeague, now)
	}
	return nil
}

// recheck reads the provider once per match and lock time shortly before
// lock, to catch a moved kickoff or a postponement.
func (s *Scheduler) recheck(ctx context.Context, m domain.Match, l domain.League, now time.Time) error {
	st, err := s.provider.Status(ctx, l.Path, m.ProviderEventID, utcDay(m.Kickoff))
	if errors.Is(err, domain.ErrNotFound) {
		s.rechecked[m.ID] = m.LockAt // the watcher alerts if it stays missing
		return nil
	}
	if err != nil {
		return fmt.Errorf("recheck status: %w", err)
	}
	s.rechecked[m.ID] = m.LockAt
	_, err = s.reconcile(ctx, m, st, now)
	return err
}

// watch reads the provider for a started match and acts on the result.
func (s *Scheduler) watch(ctx context.Context, m domain.Match, l domain.League, haveLeague bool, now time.Time) error {
	if !haveLeague {
		return s.alertOnce(ctx, m, "league-removed", fmt.Sprintf(
			"Match #%d %s: its league (ID %d) is no longer subscribed, so the result cannot be read. %s",
			m.ID, teams(m), m.LeagueID, adminHint))
	}
	if s.failures[m.ID] >= backoffAfter && s.tickN-s.lastTry[m.ID] < backoffEvery {
		return s.checkStale(ctx, m, "unavailable (provider errors)", now)
	}
	s.lastTry[m.ID] = s.tickN

	var errs []error
	raw := ""
	st, err := s.provider.Status(ctx, l.Path, m.ProviderEventID, utcDay(m.Kickoff))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		delete(s.failures, m.ID)
		raw = "not listed"
		if err := s.alertOnce(ctx, m, "not-found", fmt.Sprintf(
			"Match #%d %s: the provider no longer lists event %s near %s (status: not listed). %s",
			m.ID, teams(m), m.ProviderEventID, m.Kickoff.Format("2006-01-02"), adminHint)); err != nil {
			errs = append(errs, err)
		}
	case err != nil:
		s.failures[m.ID]++
		raw = "unavailable (provider errors)"
		errs = append(errs, fmt.Errorf("status: %w", err))
	default:
		delete(s.failures, m.ID)
		raw = st.RawName
		m, err = s.applyWatch(ctx, m, st, now)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.checkStale(ctx, m, raw, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Scheduler) applyWatch(ctx context.Context, m domain.Match, st domain.MatchStatus, now time.Time) (domain.Match, error) {
	switch st.Phase {
	case domain.PhaseLive:
		if m.State == domain.StateLocked {
			m.State = domain.StateLive
			return s.save(ctx, m)
		}
		return m, nil
	case domain.PhaseFinal:
		if st.Winner == domain.OutcomeNone {
			return m, s.alertOnce(ctx, m, "final-no-winner", fmt.Sprintf(
				"Match #%d %s: the provider reports it finished (status %q) but gives no winner. %s",
				m.ID, teams(m), st.RawName, adminHint))
		}
		return s.settle(ctx, m, st.Winner, now)
	case domain.PhasePostponed, domain.PhaseCanceled, domain.PhasePre:
		return s.reconcile(ctx, m, st, now)
	default:
		return m, s.alertOnce(ctx, m, "unknown:"+st.RawName, fmt.Sprintf(
			"Match #%d %s: the provider status %q is not recognised, so it was not settled. %s",
			m.ID, teams(m), st.RawName, adminHint))
	}
}

// checkStale alerts once when a started match still has no final result
// StaleAfter past kickoff.
func (s *Scheduler) checkStale(ctx context.Context, m domain.Match, raw string, now time.Time) error {
	if m.State != domain.StateLocked && m.State != domain.StateLive {
		return nil
	}
	if !now.After(m.Kickoff.Add(s.cfg.StaleAfter)) {
		return nil
	}
	if raw == "" {
		raw = "unknown"
	}
	return s.alertOnce(ctx, m, "stale", fmt.Sprintf(
		"Match #%d %s kicked off at %s and has no final result after %s (provider status %q). %s",
		m.ID, teams(m), m.Kickoff.Format("2006-01-02 15:04 MST"), s.cfg.StaleAfter, raw, adminHint))
}

// reconcile applies a provider reading to a stored match: postponement,
// cancellation, a new kickoff for a postponed match, or a rescheduled
// kickoff without postponement. Other phases are left to the watcher.
func (s *Scheduler) reconcile(ctx context.Context, m domain.Match, st domain.MatchStatus, now time.Time) (domain.Match, error) {
	switch st.Phase {
	case domain.PhasePostponed:
		switch m.State {
		case domain.StateFinal, domain.StateVoid, domain.StatePostponed:
			return m, nil
		}
		return s.save(ctx, game.Postpone(m))
	case domain.PhaseCanceled:
		if m.State == domain.StateVoid {
			return m, nil
		}
		return s.void(ctx, m, now)
	case domain.PhasePre:
		return s.reschedule(ctx, m, st.Kickoff, now)
	}
	return m, nil
}

func (s *Scheduler) reschedule(ctx context.Context, m domain.Match, kickoff, now time.Time) (domain.Match, error) {
	if kickoff.IsZero() {
		kickoff = m.Kickoff
	}
	kickoff = kickoff.UTC()
	switch m.State {
	case domain.StatePostponed:
		if !kickoff.After(now.Add(domain.LockLead)) {
			return m, nil
		}
		m = game.Move(m, kickoff)
		if m.MessageID == "" {
			m.State = domain.StateScheduled // no card yet: let Tick open it
		}
		var err error
		m, err = s.save(ctx, m)
		if err != nil || m.MessageID == "" {
			return m, err
		}
		if err := s.cards.AnnounceMove(ctx, m.ID); err != nil {
			return m, fmt.Errorf("announce move: %w", err)
		}
		return m, nil
	case domain.StateScheduled, domain.StateOpen, domain.StateLocked, domain.StateLive:
		lock := game.LockTime(kickoff)
		if kickoff.Equal(m.Kickoff) && m.LockAt.Equal(lock) {
			return m, nil
		}
		m.Kickoff = kickoff
		m.LockAt = lock
		if (m.State == domain.StateLocked || m.State == domain.StateLive) && lock.After(now) {
			if m.MessageID != "" {
				m.State = domain.StateOpen
			} else {
				m.State = domain.StateScheduled
			}
		}
		return s.save(ctx, m)
	}
	return m, nil
}

// settle awards points for winner. The order (append ledger, save result,
// update state) is safe to repeat after a crash: game.Settle reverses every
// prior entry before scoring.
func (s *Scheduler) settle(ctx context.Context, m domain.Match, winner domain.OutcomeKey, now time.Time) (domain.Match, error) {
	picks, err := s.store.ListPicks(ctx, m.ID)
	if err != nil {
		return m, fmt.Errorf("list picks: %w", err)
	}
	prior, err := s.store.LedgerForMatch(ctx, m.ID)
	if err != nil {
		return m, fmt.Errorf("ledger: %w", err)
	}
	if entries := game.Settle(m, picks, winner, prior, now); len(entries) > 0 {
		if err := s.store.AppendLedger(ctx, entries); err != nil {
			return m, fmt.Errorf("append ledger: %w", err)
		}
	}
	if err := s.store.SaveResult(ctx, domain.Result{MatchID: m.ID, Winner: winner, SettledBy: "auto", SettledAt: now}); err != nil {
		return m, fmt.Errorf("save result: %w", err)
	}
	m.State = domain.StateFinal
	m, err = s.save(ctx, m)
	if err == nil {
		s.forget(m.ID)
	}
	return m, err
}

// void reverses every point awarded for m and marks it void.
func (s *Scheduler) void(ctx context.Context, m domain.Match, now time.Time) (domain.Match, error) {
	prior, err := s.store.LedgerForMatch(ctx, m.ID)
	if err != nil {
		return m, fmt.Errorf("ledger: %w", err)
	}
	if entries := game.Void(m, prior, now); len(entries) > 0 {
		if err := s.store.AppendLedger(ctx, entries); err != nil {
			return m, fmt.Errorf("append ledger: %w", err)
		}
	}
	m.State = domain.StateVoid
	m, err = s.save(ctx, m)
	if err == nil {
		s.forget(m.ID)
	}
	return m, err
}

// save writes m and refreshes its card.
func (s *Scheduler) save(ctx context.Context, m domain.Match) (domain.Match, error) {
	if err := s.store.UpdateMatch(ctx, m); err != nil {
		return m, fmt.Errorf("update match: %w", err)
	}
	if err := s.cards.Refresh(ctx, m.ID); err != nil {
		return m, fmt.Errorf("refresh card: %w", err)
	}
	return m, nil
}

func (s *Scheduler) alertOnce(ctx context.Context, m domain.Match, reason, text string) error {
	if s.alerted[m.ID][reason] {
		return nil
	}
	if err := s.alerter.Alert(ctx, text); err != nil {
		return fmt.Errorf("alert: %w", err)
	}
	if s.alerted[m.ID] == nil {
		s.alerted[m.ID] = map[string]bool{}
	}
	s.alerted[m.ID][reason] = true
	return nil
}

func (s *Scheduler) forget(matchID int64) {
	delete(s.rechecked, matchID)
	delete(s.alerted, matchID)
	delete(s.failures, matchID)
	delete(s.lastTry, matchID)
}

const adminHint = "Use /match settle, /match void or /match move."

func teams(m domain.Match) string { return m.Home.Name + " vs " + m.Away.Name }

func utcDay(t time.Time) time.Time {
	y, mo, d := t.UTC().Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
}
