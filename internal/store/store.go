// Package store implements domain.Store over database/sql, SQLite dialect.
// It imports no driver; the caller opens the *sql.DB (and enables foreign
// keys if wanted).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/migrations"
)

var _ domain.Store = (*Store)(nil)

// Store is a domain.Store backed by a *sql.DB.
type Store struct {
	db *sql.DB
}

// New wraps db. Call Migrate before using the store.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Migrate applies every .sql file in migrations.FS in name order, once each.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if n > 0 {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.applyMigration(ctx, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().Unix()); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

func ts(t time.Time) int64 { return t.Unix() }

func fromTS(n int64) time.Time { return time.Unix(n, 0).UTC() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scanner interface {
	Scan(dest ...any) error
}

// ---- leagues ----

const leagueCols = `id, guild_id, provider, path, name, channel_id, open_lead_sec, allow_draw`

func scanLeague(r scanner) (domain.League, error) {
	var l domain.League
	var lead int64
	var draw int
	if err := r.Scan(&l.ID, &l.GuildID, &l.Provider, &l.Path, &l.Name, &l.ChannelID, &lead, &draw); err != nil {
		return domain.League{}, err
	}
	l.OpenLead = time.Duration(lead) * time.Second
	l.AllowDraw = draw != 0
	return l, nil
}

// AddLeague inserts a league and returns it with ID set.
func (s *Store) AddLeague(ctx context.Context, l domain.League) (domain.League, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO leagues (guild_id, provider, path, name, channel_id, open_lead_sec, allow_draw)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.GuildID, l.Provider, l.Path, l.Name, l.ChannelID, int64(l.OpenLead/time.Second), boolInt(l.AllowDraw))
	if err != nil {
		return domain.League{}, fmt.Errorf("add league: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.League{}, fmt.Errorf("add league: %w", err)
	}
	l.ID = id
	return l, nil
}

// RemoveLeague deletes a league subscription. Its matches are kept.
func (s *Store) RemoveLeague(ctx context.Context, guildID string, leagueID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM leagues WHERE id = ? AND guild_id = ?`, leagueID, guildID)
	if err != nil {
		return fmt.Errorf("remove league: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove league: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("league %d: %w", leagueID, domain.ErrNotFound)
	}
	return nil
}

// ListLeagues returns a guild's leagues; an empty guildID returns all.
func (s *Store) ListLeagues(ctx context.Context, guildID string) ([]domain.League, error) {
	q := `SELECT ` + leagueCols + ` FROM leagues`
	var args []any
	if guildID != "" {
		q += ` WHERE guild_id = ?`
		args = append(args, guildID)
	}
	q += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list leagues: %w", err)
	}
	defer rows.Close()
	var out []domain.League
	for rows.Next() {
		l, err := scanLeague(rows)
		if err != nil {
			return nil, fmt.Errorf("list leagues: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLeague returns one league or ErrNotFound.
func (s *Store) GetLeague(ctx context.Context, leagueID int64) (domain.League, error) {
	l, err := scanLeague(s.db.QueryRowContext(ctx, `SELECT `+leagueCols+` FROM leagues WHERE id = ?`, leagueID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.League{}, fmt.Errorf("league %d: %w", leagueID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.League{}, fmt.Errorf("get league: %w", err)
	}
	return l, nil
}

// ---- matches ----

const matchCols = `id, guild_id, league_id, provider_event_id, competition,
	home_name, home_abbrev, home_logo, away_name, away_abbrev, away_logo,
	kickoff, lock_at, state, message_id`

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanMatch(r scanner) (domain.Match, error) {
	var m domain.Match
	var kickoff, lockAt int64
	var state string
	if err := r.Scan(&m.ID, &m.GuildID, &m.LeagueID, &m.ProviderEventID, &m.Competition,
		&m.Home.Name, &m.Home.Abbrev, &m.Home.LogoURL,
		&m.Away.Name, &m.Away.Abbrev, &m.Away.LogoURL,
		&kickoff, &lockAt, &state, &m.MessageID); err != nil {
		return domain.Match{}, err
	}
	m.Kickoff = fromTS(kickoff)
	m.LockAt = fromTS(lockAt)
	m.State = domain.MatchState(state)
	return m, nil
}

// UpsertMatch inserts a match, or updates teams, competition and kickoff of
// the existing row with the same LeagueID and ProviderEventID.
func (s *Store) UpsertMatch(ctx context.Context, m domain.Match) (domain.Match, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Match{}, false, fmt.Errorf("upsert match: %w", err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM matches WHERE league_id = ? AND provider_event_id = ?`,
		m.LeagueID, m.ProviderEventID).Scan(&id)
	created := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		created = true
		lockAt := m.LockAt
		if lockAt.IsZero() {
			lockAt = m.Kickoff.Add(-domain.LockLead)
		}
		state := m.State
		if state == "" {
			state = domain.StateScheduled
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO matches (guild_id, league_id, provider_event_id, competition,
				home_name, home_abbrev, home_logo, away_name, away_abbrev, away_logo,
				kickoff, lock_at, state, message_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.GuildID, m.LeagueID, m.ProviderEventID, m.Competition,
			m.Home.Name, m.Home.Abbrev, m.Home.LogoURL,
			m.Away.Name, m.Away.Abbrev, m.Away.LogoURL,
			ts(m.Kickoff), ts(lockAt), string(state), m.MessageID)
		if err != nil {
			return domain.Match{}, false, fmt.Errorf("insert match: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return domain.Match{}, false, fmt.Errorf("insert match: %w", err)
		}
	case err != nil:
		return domain.Match{}, false, fmt.Errorf("upsert match: %w", err)
	default:
		if _, err := tx.ExecContext(ctx,
			`UPDATE matches SET competition = ?, home_name = ?, home_abbrev = ?, home_logo = ?,
				away_name = ?, away_abbrev = ?, away_logo = ?, kickoff = ?
			 WHERE id = ?`,
			m.Competition, m.Home.Name, m.Home.Abbrev, m.Home.LogoURL,
			m.Away.Name, m.Away.Abbrev, m.Away.LogoURL, ts(m.Kickoff), id); err != nil {
			return domain.Match{}, false, fmt.Errorf("update match: %w", err)
		}
	}

	stored, err := scanMatch(tx.QueryRowContext(ctx, `SELECT `+matchCols+` FROM matches WHERE id = ?`, id))
	if err != nil {
		return domain.Match{}, false, fmt.Errorf("reload match: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Match{}, false, fmt.Errorf("upsert match: %w", err)
	}
	return stored, created, nil
}

// UpdateMatch writes Kickoff, LockAt, State and MessageID for m.ID.
func (s *Store) UpdateMatch(ctx context.Context, m domain.Match) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE matches SET kickoff = ?, lock_at = ?, state = ?, message_id = ? WHERE id = ?`,
		ts(m.Kickoff), ts(m.LockAt), string(m.State), m.MessageID, m.ID)
	if err != nil {
		return fmt.Errorf("update match: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update match: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("match %d: %w", m.ID, domain.ErrNotFound)
	}
	return nil
}

func getMatch(ctx context.Context, q queryer, matchID int64) (domain.Match, error) {
	m, err := scanMatch(q.QueryRowContext(ctx, `SELECT `+matchCols+` FROM matches WHERE id = ?`, matchID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Match{}, fmt.Errorf("match %d: %w", matchID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Match{}, fmt.Errorf("get match: %w", err)
	}
	return m, nil
}

// GetMatch returns one match or ErrNotFound.
func (s *Store) GetMatch(ctx context.Context, matchID int64) (domain.Match, error) {
	return getMatch(ctx, s.db, matchID)
}

// ListMatchesByState returns matches in any of the given states, ordered by
// kickoff.
func (s *Store) ListMatchesByState(ctx context.Context, states ...domain.MatchState) ([]domain.Match, error) {
	if len(states) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = string(st)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+matchCols+` FROM matches WHERE state IN (`+marks+`) ORDER BY kickoff, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list matches: %w", err)
	}
	defer rows.Close()
	var out []domain.Match
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, fmt.Errorf("list matches: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- outcomes ----

// SetOutcomes replaces the outcomes of a match.
func (s *Store) SetOutcomes(ctx context.Context, matchID int64, outcomes []domain.Outcome) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set outcomes: %w", err)
	}
	defer tx.Rollback()
	m, err := getMatch(ctx, tx, matchID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outcomes WHERE match_id = ?`, matchID); err != nil {
		return fmt.Errorf("set outcomes: %w", err)
	}
	for _, o := range outcomes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outcomes (guild_id, match_id, key, label, emoji) VALUES (?, ?, ?, ?, ?)`,
			m.GuildID, matchID, string(o.Key), o.Label, o.Emoji); err != nil {
			return fmt.Errorf("set outcomes: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set outcomes: %w", err)
	}
	return nil
}

// ListOutcomes returns a match's outcomes in the order home, draw, away.
func (s *Store) ListOutcomes(ctx context.Context, matchID int64) ([]domain.Outcome, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT match_id, key, label, emoji FROM outcomes WHERE match_id = ?
		 ORDER BY CASE key WHEN 'home' THEN 0 WHEN 'draw' THEN 1 WHEN 'away' THEN 2 ELSE 3 END, key`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list outcomes: %w", err)
	}
	defer rows.Close()
	var out []domain.Outcome
	for rows.Next() {
		var o domain.Outcome
		var key string
		if err := rows.Scan(&o.MatchID, &key, &o.Label, &o.Emoji); err != nil {
			return nil, fmt.Errorf("list outcomes: %w", err)
		}
		o.Key = domain.OutcomeKey(key)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- picks ----

// UpsertPick stores a pick, replacing the user's earlier pick for the match.
func (s *Store) UpsertPick(ctx context.Context, p domain.Pick) error {
	m, err := s.GetMatch(ctx, p.MatchID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO picks (guild_id, match_id, user_id, outcome, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (match_id, user_id) DO UPDATE SET outcome = excluded.outcome, updated_at = excluded.updated_at`,
		m.GuildID, p.MatchID, p.UserID, string(p.Outcome), ts(p.UpdatedAt)); err != nil {
		return fmt.Errorf("upsert pick: %w", err)
	}
	return nil
}

func (s *Store) queryPicks(ctx context.Context, query string, args ...any) ([]domain.Pick, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list picks: %w", err)
	}
	defer rows.Close()
	var out []domain.Pick
	for rows.Next() {
		var p domain.Pick
		var outcome string
		var at int64
		if err := rows.Scan(&p.MatchID, &p.UserID, &outcome, &at); err != nil {
			return nil, fmt.Errorf("list picks: %w", err)
		}
		p.Outcome = domain.OutcomeKey(outcome)
		p.UpdatedAt = fromTS(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPicks returns every pick for a match.
func (s *Store) ListPicks(ctx context.Context, matchID int64) ([]domain.Pick, error) {
	return s.queryPicks(ctx,
		`SELECT match_id, user_id, outcome, updated_at FROM picks WHERE match_id = ? ORDER BY updated_at, user_id`, matchID)
}

// ListUserOpenPicks returns a user's picks on matches not yet final or void.
func (s *Store) ListUserOpenPicks(ctx context.Context, guildID, userID string) ([]domain.Pick, error) {
	return s.queryPicks(ctx,
		`SELECT p.match_id, p.user_id, p.outcome, p.updated_at
		 FROM picks p JOIN matches m ON m.id = p.match_id
		 WHERE p.guild_id = ? AND p.user_id = ? AND m.state NOT IN ('final', 'void')
		 ORDER BY m.kickoff, p.match_id`, guildID, userID)
}

// ---- results ----

// SaveResult stores or replaces a match's result.
func (s *Store) SaveResult(ctx context.Context, r domain.Result) error {
	m, err := s.GetMatch(ctx, r.MatchID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO results (guild_id, match_id, winner, settled_by, settled_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (match_id) DO UPDATE SET winner = excluded.winner,
			settled_by = excluded.settled_by, settled_at = excluded.settled_at`,
		m.GuildID, r.MatchID, string(r.Winner), r.SettledBy, ts(r.SettledAt)); err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	return nil
}

// GetResult returns a match's result or ErrNotFound.
func (s *Store) GetResult(ctx context.Context, matchID int64) (domain.Result, error) {
	var r domain.Result
	var winner string
	var at int64
	err := s.db.QueryRowContext(ctx,
		`SELECT match_id, winner, settled_by, settled_at FROM results WHERE match_id = ?`, matchID).
		Scan(&r.MatchID, &winner, &r.SettledBy, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Result{}, fmt.Errorf("result for match %d: %w", matchID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Result{}, fmt.Errorf("get result: %w", err)
	}
	r.Winner = domain.OutcomeKey(winner)
	r.SettledAt = fromTS(at)
	return r, nil
}

// ---- ledger ----

// AppendLedger adds entries atomically.
func (s *Store) AppendLedger(ctx context.Context, entries []domain.LedgerEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("append ledger: %w", err)
	}
	defer tx.Rollback()
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ledger (guild_id, user_id, match_id, competition, points, reason, note, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			e.GuildID, e.UserID, e.MatchID, e.Competition, e.Points, e.Reason, e.Note, ts(e.CreatedAt)); err != nil {
			return fmt.Errorf("append ledger: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("append ledger: %w", err)
	}
	return nil
}

// LedgerForMatch returns every entry written for a match, oldest first.
func (s *Store) LedgerForMatch(ctx context.Context, matchID int64) ([]domain.LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, guild_id, user_id, match_id, competition, points, reason, note, created_at
		 FROM ledger WHERE match_id = ? ORDER BY created_at, id`, matchID)
	if err != nil {
		return nil, fmt.Errorf("ledger for match: %w", err)
	}
	defer rows.Close()
	var out []domain.LedgerEntry
	for rows.Next() {
		var e domain.LedgerEntry
		var at int64
		if err := rows.Scan(&e.ID, &e.GuildID, &e.UserID, &e.MatchID, &e.Competition, &e.Points, &e.Reason, &e.Note, &at); err != nil {
			return nil, fmt.Errorf("ledger for match: %w", err)
		}
		e.CreatedAt = fromTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Leaderboard sums the ledger per user, highest first. An empty competition
// returns the all-time board. Every user with ledger rows is included.
func (s *Store) Leaderboard(ctx context.Context, guildID, competition string) ([]domain.Standing, error) {
	q := `SELECT user_id, SUM(points) FROM ledger WHERE guild_id = ?`
	args := []any{guildID}
	if competition != "" {
		q += ` AND competition = ?`
		args = append(args, competition)
	}
	q += ` GROUP BY user_id ORDER BY SUM(points) DESC, user_id ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("leaderboard: %w", err)
	}
	defer rows.Close()
	var out []domain.Standing
	for rows.Next() {
		var st domain.Standing
		if err := rows.Scan(&st.UserID, &st.Points); err != nil {
			return nil, fmt.Errorf("leaderboard: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
