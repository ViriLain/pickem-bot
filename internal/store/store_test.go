package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func st(user string, points int) domain.Standing {
	return domain.Standing{UserID: user, Points: points}
}

var kick = time.Date(2026, 6, 11, 19, 0, 0, 0, time.UTC)

func seedLeague(t *testing.T, s *Store) domain.League {
	t.Helper()
	l, err := s.AddLeague(context.Background(), domain.League{
		GuildID: "g1", Provider: "espn", Path: "soccer/fifa.world", Name: "World Cup",
		ChannelID: "c1", OpenLead: 24 * time.Hour, AllowDraw: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func seedMatch(t *testing.T, s *Store, l domain.League, event string, at time.Time) domain.Match {
	t.Helper()
	m, _, err := s.UpsertMatch(context.Background(), domain.Match{
		GuildID: l.GuildID, LeagueID: l.ID, ProviderEventID: event, Competition: "World Cup 2026",
		Home:    domain.Team{Name: "Spain", Abbrev: "ESP", LogoURL: "h.png"},
		Away:    domain.Team{Name: "Brazil", Abbrev: "BRA", LogoURL: "a.png"},
		Kickoff: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMigrateTwice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", n)
	}
}

func TestLeagues(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	l := seedLeague(t, s)
	if l.ID == 0 {
		t.Fatal("ID not set")
	}
	other, err := s.AddLeague(ctx, domain.League{GuildID: "g2", Provider: "espn", Path: "football/nfl", Name: "NFL", ChannelID: "c2", OpenLead: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLeague(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, l) {
		t.Fatalf("GetLeague = %+v, want %+v", got, l)
	}

	tests := []struct {
		guild string
		want  int
	}{{"g1", 1}, {"g2", 1}, {"", 2}, {"nope", 0}}
	for _, tc := range tests {
		ls, err := s.ListLeagues(ctx, tc.guild)
		if err != nil {
			t.Fatal(err)
		}
		if len(ls) != tc.want {
			t.Errorf("ListLeagues(%q) = %d, want %d", tc.guild, len(ls), tc.want)
		}
	}

	if err := s.RemoveLeague(ctx, "g1", other.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("remove with wrong guild = %v, want ErrNotFound", err)
	}
	if err := s.RemoveLeague(ctx, "g1", l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLeague(ctx, l.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetLeague after remove = %v, want ErrNotFound", err)
	}
	if err := s.RemoveLeague(ctx, "g1", l.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second remove = %v, want ErrNotFound", err)
	}
}

func TestUpsertMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	l := seedLeague(t, s)

	m, created, err := s.UpsertMatch(ctx, domain.Match{
		GuildID: "g1", LeagueID: l.ID, ProviderEventID: "e1", Competition: "WC",
		Home: domain.Team{Name: "Spain", Abbrev: "ESP"}, Away: domain.Team{Name: "Brazil", Abbrev: "BRA"},
		Kickoff: kick,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || m.ID == 0 {
		t.Fatalf("created=%v id=%d", created, m.ID)
	}
	if want := kick.Add(-domain.LockLead); !m.LockAt.Equal(want) {
		t.Errorf("LockAt = %v, want %v", m.LockAt, want)
	}
	if m.State != domain.StateScheduled {
		t.Errorf("State = %q, want scheduled", m.State)
	}
	if m.Kickoff.Location() != time.UTC || m.LockAt.Location() != time.UTC {
		t.Error("times not in UTC")
	}

	// Bot-owned fields change.
	newKick := kick.Add(48 * time.Hour)
	m.Kickoff = newKick
	m.LockAt = newKick.Add(-domain.LockLead)
	m.State = domain.StateOpen
	m.MessageID = "msg1"
	if err := s.UpdateMatch(ctx, m); err != nil {
		t.Fatal(err)
	}

	// A provider re-sync must not touch state, lock or message.
	m2, created, err := s.UpsertMatch(ctx, domain.Match{
		GuildID: "g1", LeagueID: l.ID, ProviderEventID: "e1", Competition: "WC 2026",
		Home: domain.Team{Name: "España", Abbrev: "ESP", LogoURL: "h2"}, Away: domain.Team{Name: "Brasil", Abbrev: "BRA", LogoURL: "a2"},
		Kickoff: kick.Add(72 * time.Hour), LockAt: kick, State: domain.StateFinal, MessageID: "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true on update")
	}
	if m2.ID != m.ID {
		t.Errorf("ID changed: %d vs %d", m2.ID, m.ID)
	}
	if m2.State != domain.StateOpen || m2.MessageID != "msg1" || !m2.LockAt.Equal(newKick.Add(-domain.LockLead)) {
		t.Errorf("state/message/lock overwritten: %+v", m2)
	}
	if m2.Competition != "WC 2026" || m2.Home.Name != "España" || m2.Away.LogoURL != "a2" || !m2.Kickoff.Equal(kick.Add(72*time.Hour)) {
		t.Errorf("provider fields not updated: %+v", m2)
	}

	// Explicit LockAt and State are honored on create.
	m3, created, err := s.UpsertMatch(ctx, domain.Match{
		GuildID: "g1", LeagueID: l.ID, ProviderEventID: "e2", Kickoff: kick,
		LockAt: kick.Add(-time.Hour), State: domain.StateLive,
	})
	if err != nil || !created {
		t.Fatalf("err=%v created=%v", err, created)
	}
	if !m3.LockAt.Equal(kick.Add(-time.Hour)) || m3.State != domain.StateLive {
		t.Errorf("explicit values lost: %+v", m3)
	}

	got, err := s.GetMatch(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m2) {
		t.Errorf("GetMatch = %+v, want %+v", got, m2)
	}
}

func TestMatchNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.GetMatch(ctx, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetMatch = %v", err)
	}
	if err := s.UpdateMatch(ctx, domain.Match{ID: 99}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdateMatch = %v", err)
	}
	if err := s.SetOutcomes(ctx, 99, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetOutcomes = %v", err)
	}
	if err := s.UpsertPick(ctx, domain.Pick{MatchID: 99, UserID: "u", Outcome: domain.OutcomeHome, UpdatedAt: kick}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpsertPick = %v", err)
	}
	if err := s.SaveResult(ctx, domain.Result{MatchID: 99, Winner: domain.OutcomeHome}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SaveResult = %v", err)
	}
	if _, err := s.GetResult(ctx, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetResult = %v", err)
	}
}

func TestListMatchesByState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	l := seedLeague(t, s)
	late := seedMatch(t, s, l, "late", kick.Add(2*time.Hour))
	early := seedMatch(t, s, l, "early", kick)
	done := seedMatch(t, s, l, "done", kick.Add(-time.Hour))
	done.State = domain.StateFinal
	if err := s.UpdateMatch(ctx, done); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListMatchesByState(ctx, domain.StateScheduled)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != early.ID || got[1].ID != late.ID {
		t.Errorf("scheduled order wrong: %+v", got)
	}
	got, err = s.ListMatchesByState(ctx, domain.StateScheduled, domain.StateFinal)
	if err != nil || len(got) != 3 || got[0].ID != done.ID {
		t.Errorf("multi-state: %v %+v", err, got)
	}
	got, err = s.ListMatchesByState(ctx)
	if err != nil || len(got) != 0 {
		t.Errorf("no states: %v %+v", err, got)
	}
}

func TestOutcomesOrderAndReplace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMatch(t, s, seedLeague(t, s), "e1", kick)

	err := s.SetOutcomes(ctx, m.ID, []domain.Outcome{
		{Key: domain.OutcomeAway, Label: "Brazil", Emoji: "B"},
		{Key: domain.OutcomeDraw, Label: "Draw"},
		{Key: domain.OutcomeHome, Label: "Spain", Emoji: "S"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListOutcomes(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Outcome{
		{MatchID: m.ID, Key: domain.OutcomeHome, Label: "Spain", Emoji: "S"},
		{MatchID: m.ID, Key: domain.OutcomeDraw, Label: "Draw"},
		{MatchID: m.ID, Key: domain.OutcomeAway, Label: "Brazil", Emoji: "B"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %+v, want %+v", got, want)
	}

	// Replace with two outcomes; the draw must be gone.
	if err := s.SetOutcomes(ctx, m.ID, []domain.Outcome{
		{Key: domain.OutcomeAway, Label: "Brasil"},
		{Key: domain.OutcomeHome, Label: "España"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListOutcomes(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != domain.OutcomeHome || got[0].Label != "España" || got[1].Key != domain.OutcomeAway {
		t.Fatalf("after replace = %+v", got)
	}

	// A duplicate key fails and rolls back, leaving the old set intact.
	err = s.SetOutcomes(ctx, m.ID, []domain.Outcome{
		{Key: domain.OutcomeHome, Label: "x"}, {Key: domain.OutcomeHome, Label: "y"},
	})
	if err == nil {
		t.Fatal("duplicate key accepted")
	}
	got, _ = s.ListOutcomes(ctx, m.ID)
	if len(got) != 2 || got[0].Label != "España" {
		t.Errorf("rollback failed: %+v", got)
	}

	empty, err := s.ListOutcomes(ctx, 12345)
	if err != nil || len(empty) != 0 {
		t.Errorf("unknown match: %v %+v", err, empty)
	}
}

func TestPicks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	l := seedLeague(t, s)
	m1 := seedMatch(t, s, l, "e1", kick)
	m2 := seedMatch(t, s, l, "e2", kick.Add(time.Hour))
	m3 := seedMatch(t, s, l, "e3", kick.Add(2*time.Hour))
	m4 := seedMatch(t, s, l, "e4", kick.Add(3*time.Hour))
	m3.State = domain.StateFinal
	m4.State = domain.StateVoid
	for _, m := range []domain.Match{m3, m4} {
		if err := s.UpdateMatch(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	t0 := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	picks := []domain.Pick{
		{MatchID: m1.ID, UserID: "u1", Outcome: domain.OutcomeHome, UpdatedAt: t0},
		{MatchID: m2.ID, UserID: "u1", Outcome: domain.OutcomeAway, UpdatedAt: t0},
		{MatchID: m3.ID, UserID: "u1", Outcome: domain.OutcomeDraw, UpdatedAt: t0},
		{MatchID: m4.ID, UserID: "u1", Outcome: domain.OutcomeHome, UpdatedAt: t0},
		{MatchID: m1.ID, UserID: "u2", Outcome: domain.OutcomeAway, UpdatedAt: t0},
	}
	for _, p := range picks {
		if err := s.UpsertPick(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	// Change u1's pick on m1.
	t1 := t0.Add(time.Minute)
	if err := s.UpsertPick(ctx, domain.Pick{MatchID: m1.ID, UserID: "u1", Outcome: domain.OutcomeDraw, UpdatedAt: t1}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPicks(ctx, m1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListPicks = %+v", got)
	}
	byUser := map[string]domain.Pick{}
	for _, p := range got {
		byUser[p.UserID] = p
	}
	if p := byUser["u1"]; p.Outcome != domain.OutcomeDraw || !p.UpdatedAt.Equal(t1) || p.UpdatedAt.Location() != time.UTC {
		t.Errorf("u1 pick not replaced: %+v", p)
	}
	if byUser["u2"].Outcome != domain.OutcomeAway {
		t.Errorf("u2 pick changed: %+v", byUser["u2"])
	}

	// Open picks exclude final and void matches, and other users and guilds.
	open, err := s.ListUserOpenPicks(ctx, "g1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].MatchID != m1.ID || open[1].MatchID != m2.ID {
		t.Errorf("open picks = %+v", open)
	}
	if open, _ := s.ListUserOpenPicks(ctx, "g2", "u1"); len(open) != 0 {
		t.Errorf("other guild open picks = %+v", open)
	}
	if open, _ := s.ListUserOpenPicks(ctx, "g1", "nobody"); len(open) != 0 {
		t.Errorf("unknown user open picks = %+v", open)
	}

	// guild_id is filled from the match.
	var guild string
	if err := s.db.QueryRow(`SELECT guild_id FROM picks WHERE match_id = ? AND user_id = 'u1'`, m1.ID).Scan(&guild); err != nil || guild != "g1" {
		t.Errorf("pick guild = %q, %v", guild, err)
	}
}

func TestResults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := seedMatch(t, s, seedLeague(t, s), "e1", kick)

	if _, err := s.GetResult(ctx, m.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("before save: %v", err)
	}
	r1 := domain.Result{MatchID: m.ID, Winner: domain.OutcomeHome, SettledBy: "auto", SettledAt: kick.Add(2 * time.Hour)}
	if err := s.SaveResult(ctx, r1); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetResult(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(got, r1) {
		t.Fatalf("GetResult = %+v, %v; want %+v", got, err, r1)
	}
	r2 := domain.Result{MatchID: m.ID, Winner: domain.OutcomeDraw, SettledBy: "admin1", SettledAt: kick.Add(5 * time.Hour)}
	if err := s.SaveResult(ctx, r2); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetResult(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(got, r2) {
		t.Fatalf("after replace = %+v, %v; want %+v", got, err, r2)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&n); err != nil || n != 1 {
		t.Errorf("results rows = %d, %v", n, err)
	}
}

func TestLedgerAndLeaderboard(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	l := seedLeague(t, s)
	m := seedMatch(t, s, l, "e1", kick)
	at := kick.Add(3 * time.Hour)

	entries := []domain.LedgerEntry{
		{GuildID: "g1", UserID: "u2", MatchID: m.ID, Competition: "WC", Points: 1, Reason: domain.ReasonCorrect, CreatedAt: at},
		{GuildID: "g1", UserID: "u1", MatchID: m.ID, Competition: "WC", Points: 1, Reason: domain.ReasonCorrect, CreatedAt: at},
		{GuildID: "g1", UserID: "u1", MatchID: m.ID, Competition: "WC", Points: 1, Reason: domain.ReasonUnderdog, CreatedAt: at},
		{GuildID: "g1", UserID: "u3", Competition: "NFL", Points: 5, Reason: domain.ReasonAdjust, Note: "bonus", CreatedAt: at},
		{GuildID: "g1", UserID: "u4", MatchID: m.ID, Competition: "WC", Points: 1, Reason: domain.ReasonCorrect, CreatedAt: at},
		{GuildID: "g1", UserID: "u4", MatchID: m.ID, Competition: "WC", Points: -1, Reason: domain.ReasonReversal, CreatedAt: at.Add(time.Minute)},
		{GuildID: "g2", UserID: "u1", Competition: "WC", Points: 9, Reason: domain.ReasonAdjust, CreatedAt: at},
	}
	if err := s.AppendLedger(ctx, entries); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLedger(ctx, nil); err != nil {
		t.Fatalf("empty append: %v", err)
	}

	forMatch, err := s.LedgerForMatch(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forMatch) != 5 {
		t.Fatalf("LedgerForMatch = %d entries, want 5", len(forMatch))
	}
	if forMatch[0].ID == 0 || forMatch[4].Reason != domain.ReasonReversal || forMatch[4].Points != -1 || !forMatch[4].CreatedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("unexpected entries: %+v", forMatch)
	}
	if none, err := s.LedgerForMatch(ctx, 777); err != nil || len(none) != 0 {
		t.Errorf("unknown match ledger: %v %+v", err, none)
	}

	tests := []struct {
		name, comp string
		want       []domain.Standing
	}{
		{"all-time", "", []domain.Standing{st("u3", 5), st("u1", 2), st("u2", 1), st("u4", 0)}},
		{"WC", "WC", []domain.Standing{st("u1", 2), st("u2", 1), st("u4", 0)}},
		{"NFL", "NFL", []domain.Standing{st("u3", 5)}},
		{"none", "MLB", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Leaderboard(ctx, "g1", tc.comp)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	// Ties break by user_id ascending.
	if err := s.AppendLedger(ctx, []domain.LedgerEntry{
		{GuildID: "g3", UserID: "b", Points: 2, Reason: domain.ReasonAdjust, CreatedAt: at},
		{GuildID: "g3", UserID: "a", Points: 2, Reason: domain.ReasonAdjust, CreatedAt: at},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Leaderboard(ctx, "g3", "")
	if err != nil || !reflect.DeepEqual(got, []domain.Standing{st("a", 2), st("b", 2)}) {
		t.Errorf("tie order = %+v, %v", got, err)
	}
}

func TestAppendLedgerAtomic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := kick
	// Go types cannot produce a constraint violation, so force one with a trigger.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_bad BEFORE INSERT ON ledger
		WHEN NEW.reason = 'bad' BEGIN SELECT RAISE(ABORT, 'bad reason'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.AppendLedger(ctx, []domain.LedgerEntry{
		{GuildID: "g1", UserID: "u1", Points: 1, Reason: domain.ReasonAdjust, CreatedAt: at},
		{GuildID: "g1", UserID: "u1", Points: 1, Reason: "bad", CreatedAt: at},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	got, err := s.Leaderboard(ctx, "g1", "")
	if err != nil || len(got) != 0 {
		t.Errorf("partial write survived: %+v, %v", got, err)
	}
}
