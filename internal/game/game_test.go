package game

import (
	"testing"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

var t0 = time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC)

func picks(home, away, draw int) []domain.Pick {
	var ps []domain.Pick
	add := func(n int, k domain.OutcomeKey) {
		for i := 0; i < n; i++ {
			ps = append(ps, domain.Pick{MatchID: 1, UserID: string(k) + string(rune('a'+i)), Outcome: k})
		}
	}
	add(home, domain.OutcomeHome)
	add(away, domain.OutcomeAway)
	add(draw, domain.OutcomeDraw)
	return ps
}

func total(es []domain.LedgerEntry) map[string]int {
	m := map[string]int{}
	for _, e := range es {
		m[e.UserID] += e.Points
	}
	for u, n := range m {
		if n == 0 {
			delete(m, u)
		}
	}
	return m
}

func TestCanPick(t *testing.T) {
	m := domain.Match{State: domain.StateOpen, Kickoff: t0, LockAt: LockTime(t0)}
	cases := []struct {
		name  string
		now   time.Time
		state domain.MatchState
		want  bool
	}{
		{"well before", t0.Add(-time.Hour), domain.StateOpen, true},
		{"just before lock", t0.Add(-61 * time.Second), domain.StateOpen, true},
		{"at lock", t0.Add(-time.Minute), domain.StateOpen, false},
		{"after kickoff", t0.Add(time.Second), domain.StateOpen, false},
		{"postponed", t0.Add(-time.Hour), domain.StatePostponed, false},
		{"scheduled", t0.Add(-time.Hour), domain.StateScheduled, false},
	}
	for _, c := range cases {
		m.State = c.state
		if got := CanPick(c.now, m); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestScore(t *testing.T) {
	m := domain.Match{ID: 1, GuildID: "g", Competition: "NFL 2026"}
	cases := []struct {
		name             string
		home, away, draw int
		winner           domain.OutcomeKey
		wantEach         int // points per winning picker
		wantWinners      int
	}{
		{"favorite wins", 5, 1, 0, domain.OutcomeHome, 1, 5},
		{"underdog 1 of 6", 5, 1, 0, domain.OutcomeAway, 2, 1},
		{"exactly a third, no bonus", 4, 2, 0, domain.OutcomeAway, 1, 2},
		{"1 of 4 gets bonus", 3, 1, 0, domain.OutcomeAway, 2, 1},
		{"1 of 3, too few pickers", 2, 1, 0, domain.OutcomeAway, 1, 1},
		{"draw underdog", 3, 3, 1, domain.OutcomeDraw, 2, 1},
		{"nobody right", 3, 3, 0, domain.OutcomeDraw, 0, 0},
		{"no picks", 0, 0, 0, domain.OutcomeHome, 0, 0},
	}
	for _, c := range cases {
		got := total(Score(m, picks(c.home, c.away, c.draw), c.winner, t0))
		if len(got) != c.wantWinners {
			t.Errorf("%s: %d winners, want %d", c.name, len(got), c.wantWinners)
		}
		for u, n := range got {
			if n != c.wantEach {
				t.Errorf("%s: %s got %d, want %d", c.name, u, n, c.wantEach)
			}
		}
	}
}

func TestResettleAndVoid(t *testing.T) {
	m := domain.Match{ID: 1, GuildID: "g", Competition: "c"}
	ps := picks(5, 1, 0)
	ledger := Settle(m, ps, domain.OutcomeHome, nil, t0)
	if got := total(ledger); len(got) != 5 {
		t.Fatalf("first settle: %v", got)
	}
	ledger = append(ledger, Settle(m, ps, domain.OutcomeAway, ledger, t0)...)
	got := total(ledger)
	if len(got) != 1 || got["awaya"] != 2 {
		t.Fatalf("after re-settle: %v", got)
	}
	ledger = append(ledger, Void(m, ledger, t0)...)
	if got := total(ledger); len(got) != 0 {
		t.Fatalf("after void: %v", got)
	}
	for _, e := range ledger {
		if e.MatchID != 1 || e.Competition != "c" || e.GuildID != "g" {
			t.Fatalf("entry missing match fields: %+v", e)
		}
	}
}

func TestPostponeMove(t *testing.T) {
	m := domain.Match{State: domain.StateLocked, Kickoff: t0, LockAt: LockTime(t0)}
	m = Postpone(m)
	if m.State != domain.StatePostponed || CanPick(t0.Add(-time.Hour), m) {
		t.Fatalf("postpone: %+v", m)
	}
	nk := t0.Add(48 * time.Hour)
	m = Move(m, nk)
	if m.State != domain.StateOpen || !m.Kickoff.Equal(nk) || !m.LockAt.Equal(nk.Add(-time.Minute)) {
		t.Fatalf("move: %+v", m)
	}
	if !CanPick(t0, m) {
		t.Fatal("moved match should accept picks")
	}
}
