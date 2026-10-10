package scheduler

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/internal/game"
)

const path = "soccer/eng.1"

var (
	t0   = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	kick = time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
)

type harness struct {
	store  *fakeStore
	prov   *fakeProvider
	cards  *fakeCards
	alerts *fakeAlerter
	clock  *fakeClock
	s      *Scheduler
	league domain.League
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{store: newStore(), prov: newProvider(), alerts: &fakeAlerter{}, clock: &fakeClock{now: t0}}
	h.cards = &fakeCards{store: h.store}
	h.s = New(h.store, h.prov, h.cards, h.alerts, h.clock, cfg)
	l, err := h.store.AddLeague(context.Background(), domain.League{
		GuildID: "g1", Provider: "espn", Path: path, Name: "EPL", ChannelID: "c1",
		OpenLead: 24 * time.Hour, AllowDraw: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.league = l
	return h
}

func pm(event string, phase domain.Phase, raw string, kickoff time.Time) domain.ProviderMatch {
	return domain.ProviderMatch{
		EventID:     event,
		Competition: "2026-27 English Premier League",
		Home:        domain.Team{Name: "Arsenal", Abbrev: "ARS"},
		Away:        domain.Team{Name: "Chelsea", Abbrev: "CHE"},
		Status:      domain.MatchStatus{Phase: phase, RawName: raw, Kickoff: kickoff},
	}
}

func pre(kickoff time.Time) domain.MatchStatus {
	return domain.MatchStatus{Phase: domain.PhasePre, RawName: "STATUS_SCHEDULED", Kickoff: kickoff}
}

// addMatch inserts a match directly in the given state.
func (h *harness) addMatch(t *testing.T, event string, kickoff time.Time, state domain.MatchState, messageID string) int64 {
	t.Helper()
	m, _, err := h.store.UpsertMatch(context.Background(), domain.Match{
		GuildID: "g1", LeagueID: h.league.ID, ProviderEventID: event, Competition: "EPL 2026",
		Home: domain.Team{Name: "Arsenal"}, Away: domain.Team{Name: "Chelsea"},
		Kickoff: kickoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.State = state
	m.MessageID = messageID
	h.store.put(m)
	return m.ID
}

func (h *harness) pick(id int64, user string, key domain.OutcomeKey) {
	_ = h.store.UpsertPick(context.Background(), domain.Pick{MatchID: id, UserID: user, Outcome: key, UpdatedAt: t0})
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	if err := h.s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
}

func (h *harness) sync(t *testing.T) {
	t.Helper()
	if err := h.s.SyncNow(context.Background()); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
}

func wantCalls(t *testing.T, h *harness, want ...string) {
	t.Helper()
	got := h.cards.calls()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("card calls = %q, want %q", got, want)
	}
}

func wantState(t *testing.T, h *harness, id int64, want domain.MatchState) {
	t.Helper()
	if got := h.store.match(id).State; got != want {
		t.Fatalf("match %d state = %s, want %s", id, got, want)
	}
}

func TestConfigDefaults(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, Config{})
	want := Config{SyncEvery: 6 * time.Hour, HorizonDays: 7, Tick: time.Minute, StaleAfter: 6 * time.Hour, Recheck: 10 * time.Minute}
	if s.cfg != want {
		t.Fatalf("cfg = %+v, want %+v", s.cfg, want)
	}
	s = New(nil, nil, nil, nil, nil, Config{HorizonDays: 2, Tick: time.Second})
	if s.cfg.HorizonDays != 2 || s.cfg.Tick != time.Second || s.cfg.SyncEvery != 6*time.Hour {
		t.Fatalf("cfg = %+v", s.cfg)
	}
}

func TestLifecycle(t *testing.T) {
	h := newHarness(t, Config{})
	h.prov.list(path, utcDay(kick), pm("e1", domain.PhasePre, "STATUS_SCHEDULED", kick))
	h.sync(t)

	ms, _ := h.store.ListMatchesByState(context.Background(), domain.StateScheduled)
	if len(ms) != 1 {
		t.Fatalf("matches after sync = %d, want 1", len(ms))
	}
	m := ms[0]
	id := m.ID
	if m.GuildID != "g1" || m.LeagueID != h.league.ID || !m.LockAt.Equal(kick.Add(-time.Minute)) || m.Competition != "2026-27 English Premier League" {
		t.Fatalf("synced match = %+v", m)
	}

	// Too early to open.
	h.clock.set(kick.Add(-25 * time.Hour))
	h.tick(t)
	wantState(t, h, id, domain.StateScheduled)
	wantCalls(t, h)

	// Within OpenLead: card posted.
	h.clock.set(kick.Add(-23 * time.Hour))
	h.tick(t)
	wantState(t, h, id, domain.StateOpen)
	wantCalls(t, h, "open:"+itoa(id))
	h.cards.reset()
	for _, u := range []string{"a", "b", "c"} {
		h.pick(id, u, domain.OutcomeHome)
	}
	h.pick(id, "d", domain.OutcomeAway)

	// Recheck once before lock; kickoff unchanged.
	h.prov.setStatus("e1", pre(kick), nil)
	h.clock.set(kick.Add(-6 * time.Minute))
	h.tick(t)
	h.clock.set(kick.Add(-5 * time.Minute))
	h.tick(t)
	if n := h.prov.calls("e1"); n != 1 {
		t.Fatalf("recheck status calls = %d, want 1", n)
	}
	wantState(t, h, id, domain.StateOpen)
	wantCalls(t, h)

	// Lock.
	h.clock.set(kick.Add(-time.Minute))
	h.tick(t)
	wantState(t, h, id, domain.StateLocked)
	wantCalls(t, h, "refresh:"+itoa(id)+":locked")

	// Locked but not kicked off: no provider call.
	h.clock.set(kick.Add(-30 * time.Second))
	h.tick(t)
	if n := h.prov.calls("e1"); n != 1 {
		t.Fatalf("status calls before kickoff = %d, want 1", n)
	}

	// Live.
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseLive, RawName: "STATUS_IN_PROGRESS", Kickoff: kick}, nil)
	h.clock.set(kick.Add(time.Minute))
	h.tick(t)
	h.tick(t)
	wantState(t, h, id, domain.StateLive)
	wantCalls(t, h, "refresh:"+itoa(id)+":locked", "refresh:"+itoa(id)+":live")

	// Final.
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseFinal, RawName: "STATUS_FINAL", Kickoff: kick, Winner: domain.OutcomeHome}, nil)
	end := kick.Add(2 * time.Hour)
	h.clock.set(end)
	h.tick(t)
	wantState(t, h, id, domain.StateFinal)
	wantCalls(t, h, "refresh:"+itoa(id)+":locked", "refresh:"+itoa(id)+":live", "refresh:"+itoa(id)+":final")
	if got, want := h.store.points(), map[string]int{"a": 1, "b": 1, "c": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("points = %v, want %v", got, want)
	}
	r, err := h.store.GetResult(context.Background(), id)
	if err != nil || r != (domain.Result{MatchID: id, Winner: domain.OutcomeHome, SettledBy: "auto", SettledAt: end}) {
		t.Fatalf("result = %+v, %v", r, err)
	}

	// Final matches are left alone.
	before := h.prov.calls("e1")
	h.tick(t)
	if h.prov.calls("e1") != before {
		t.Fatal("final match was polled again")
	}
	if len(h.alerts.all()) != 0 {
		t.Fatalf("unexpected alerts: %q", h.alerts.all())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSettleScoring(t *testing.T) {
	type pk struct {
		user string
		key  domain.OutcomeKey
	}
	tests := []struct {
		name     string
		picks    []pk
		winner   domain.OutcomeKey
		want     map[string]int
		underdog int
	}{
		{
			name:   "favourite wins",
			picks:  []pk{{"a", domain.OutcomeHome}, {"b", domain.OutcomeHome}, {"c", domain.OutcomeHome}, {"d", domain.OutcomeAway}},
			winner: domain.OutcomeHome,
			want:   map[string]int{"a": 1, "b": 1, "c": 1},
		},
		{
			name:     "underdog bonus",
			picks:    []pk{{"a", domain.OutcomeHome}, {"b", domain.OutcomeHome}, {"c", domain.OutcomeHome}, {"d", domain.OutcomeHome}, {"e", domain.OutcomeAway}},
			winner:   domain.OutcomeAway,
			want:     map[string]int{"e": 2},
			underdog: 1,
		},
		{
			name:   "too few pickers for bonus",
			picks:  []pk{{"a", domain.OutcomeHome}, {"b", domain.OutcomeHome}, {"e", domain.OutcomeAway}},
			winner: domain.OutcomeAway,
			want:   map[string]int{"e": 1},
		},
		{
			name:   "draw nobody picked",
			picks:  []pk{{"a", domain.OutcomeHome}, {"b", domain.OutcomeAway}},
			winner: domain.OutcomeDraw,
			want:   map[string]int{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{})
			id := h.addMatch(t, "e1", kick, domain.StateLive, "msg")
			for _, p := range tc.picks {
				h.pick(id, p.user, p.key)
			}
			h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseFinal, RawName: "STATUS_FINAL", Kickoff: kick, Winner: tc.winner}, nil)
			h.clock.set(kick.Add(2 * time.Hour))
			h.tick(t)
			wantState(t, h, id, domain.StateFinal)
			if got := h.store.points(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("points = %v, want %v", got, tc.want)
			}
			n := 0
			for _, e := range h.store.ledger {
				if e.Reason == domain.ReasonUnderdog {
					n++
				}
				if e.GuildID != "g1" || e.MatchID != id || e.Competition != "EPL 2026" {
					t.Fatalf("ledger entry = %+v", e)
				}
			}
			if n != tc.underdog {
				t.Fatalf("underdog entries = %d, want %d", n, tc.underdog)
			}
		})
	}
}

func TestPostponedThenMovedBySync(t *testing.T) {
	h := newHarness(t, Config{})
	id := h.addMatch(t, "e1", kick, domain.StateOpen, "msg")
	h.pick(id, "a", domain.OutcomeHome)
	h.pick(id, "b", domain.OutcomeAway)

	h.prov.list(path, utcDay(kick), pm("e1", domain.PhasePostponed, "STATUS_POSTPONED", kick))
	h.sync(t)
	wantState(t, h, id, domain.StatePostponed)
	wantCalls(t, h, "refresh:"+itoa(id)+":postponed")

	// Postponed matches are not polled by Tick.
	h.clock.set(kick.Add(time.Hour))
	h.tick(t)
	if h.prov.calls("e1") != 0 {
		t.Fatal("postponed match was polled")
	}

	// Listed again at a time already past: stays postponed.
	h.cards.reset()
	h.prov.list(path, utcDay(kick), pm("e1", domain.PhasePre, "STATUS_SCHEDULED", kick))
	h.sync(t)
	wantState(t, h, id, domain.StatePostponed)
	wantCalls(t, h)

	// New kickoff three days out.
	newKick := kick.Add(72 * time.Hour)
	h.prov.list(path, utcDay(kick))
	h.prov.list(path, utcDay(newKick), pm("e1", domain.PhasePre, "STATUS_SCHEDULED", newKick))
	h.sync(t)
	m := h.store.match(id)
	if m.State != domain.StateOpen || !m.Kickoff.Equal(newKick) || !m.LockAt.Equal(game.LockTime(newKick)) || m.MessageID != "msg" {
		t.Fatalf("moved match = %+v", m)
	}
	wantCalls(t, h, "refresh:"+itoa(id)+":open", "announce:"+itoa(id))
	if picks, _ := h.store.ListPicks(context.Background(), id); len(picks) != 2 {
		t.Fatalf("picks after move = %d, want 2", len(picks))
	}

	// A further sync with the same listing changes nothing.
	h.cards.reset()
	h.sync(t)
	wantCalls(t, h)
}

func TestRescheduledWithoutPostponement(t *testing.T) {
	newKick := kick.Add(2 * time.Hour)
	tests := []struct {
		name      string
		state     domain.MatchState
		messageID string
		now       time.Time
		viaTick   bool // provider Status via Tick instead of a sync listing
		wantState domain.MatchState
	}{
		{name: "scheduled via sync", state: domain.StateScheduled, now: t0, wantState: domain.StateScheduled},
		{name: "open via sync", state: domain.StateOpen, messageID: "msg", now: t0, wantState: domain.StateOpen},
		{name: "locked via sync reopens", state: domain.StateLocked, messageID: "msg", now: kick.Add(-30 * time.Second), wantState: domain.StateOpen},
		{name: "locked without card goes back to scheduled", state: domain.StateLocked, now: kick.Add(-30 * time.Second), wantState: domain.StateScheduled},
		{name: "open recheck before lock", state: domain.StateOpen, messageID: "msg", now: kick.Add(-5 * time.Minute), viaTick: true, wantState: domain.StateOpen},
		{name: "locked watcher after kickoff reopens", state: domain.StateLocked, messageID: "msg", now: kick.Add(time.Minute), viaTick: true, wantState: domain.StateOpen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{})
			id := h.addMatch(t, "e1", kick, tc.state, tc.messageID)
			h.clock.set(tc.now)
			if tc.viaTick {
				h.prov.setStatus("e1", pre(newKick), nil)
				h.tick(t)
			} else {
				h.prov.list(path, utcDay(kick), pm("e1", domain.PhasePre, "STATUS_SCHEDULED", newKick))
				h.sync(t)
			}
			m := h.store.match(id)
			if m.State != tc.wantState || !m.Kickoff.Equal(newKick) || !m.LockAt.Equal(game.LockTime(newKick)) {
				t.Fatalf("match = %+v", m)
			}
			wantCalls(t, h, "refresh:"+itoa(id)+":"+string(tc.wantState))
			if tc.viaTick {
				// Recheck happens once per lock time; the new lock is outside the window.
				before := h.prov.calls("e1")
				h.tick(t)
				if tc.state == domain.StateOpen && h.prov.calls("e1") != before {
					t.Fatal("rechecked again")
				}
			}
		})
	}
}

func TestCanceledVoids(t *testing.T) {
	t.Run("final match canceled via sync reverses points", func(t *testing.T) {
		h := newHarness(t, Config{})
		id := h.addMatch(t, "e1", kick, domain.StateLive, "msg")
		h.pick(id, "a", domain.OutcomeHome)
		h.pick(id, "b", domain.OutcomeAway)
		h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseFinal, RawName: "STATUS_FINAL", Kickoff: kick, Winner: domain.OutcomeHome}, nil)
		h.clock.set(kick.Add(2 * time.Hour))
		h.tick(t)
		if got := h.store.points(); !reflect.DeepEqual(got, map[string]int{"a": 1}) {
			t.Fatalf("points after settle = %v", got)
		}
		h.cards.reset()
		h.prov.list(path, utcDay(kick), pm("e1", domain.PhaseCanceled, "STATUS_CANCELED", kick))
		h.sync(t)
		wantState(t, h, id, domain.StateVoid)
		wantCalls(t, h, "refresh:"+itoa(id)+":void")
		if got := h.store.points(); len(got) != 0 {
			t.Fatalf("points after void = %v, want none", got)
		}
		last := h.store.ledger[len(h.store.ledger)-1]
		if last.Reason != domain.ReasonReversal || last.Points != -1 || last.UserID != "a" {
			t.Fatalf("reversal = %+v", last)
		}
		// Syncing again does not reverse twice.
		n := len(h.store.ledger)
		h.sync(t)
		if len(h.store.ledger) != n {
			t.Fatal("void reapplied")
		}
	})
	t.Run("live match canceled via tick", func(t *testing.T) {
		h := newHarness(t, Config{})
		id := h.addMatch(t, "e1", kick, domain.StateLive, "msg")
		h.pick(id, "a", domain.OutcomeHome)
		h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseCanceled, RawName: "STATUS_CANCELED", Kickoff: kick}, nil)
		h.clock.set(kick.Add(time.Hour))
		h.tick(t)
		wantState(t, h, id, domain.StateVoid)
		if len(h.store.ledger) != 0 {
			t.Fatalf("ledger = %+v, want empty", h.store.ledger)
		}
		if picks, _ := h.store.ListPicks(context.Background(), id); len(picks) != 1 {
			t.Fatal("picks lost")
		}
	})
}

func TestUnknownStatusAlertsOnce(t *testing.T) {
	h := newHarness(t, Config{})
	id := h.addMatch(t, "e1", kick, domain.StateLocked, "msg")
	h.clock.set(kick.Add(time.Hour))
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseUnknown, RawName: "STATUS_WEIRD", Kickoff: kick}, nil)
	for i := 0; i < 3; i++ {
		h.tick(t)
	}
	alerts := h.alerts.all()
	if len(alerts) != 1 {
		t.Fatalf("alerts = %q, want 1", alerts)
	}
	for _, want := range []string{"Match #" + itoa(id), "Arsenal vs Chelsea", "STATUS_WEIRD", "/match settle", "/match void", "/match move"} {
		if !strings.Contains(alerts[0], want) {
			t.Fatalf("alert %q lacks %q", alerts[0], want)
		}
	}
	wantState(t, h, id, domain.StateLocked)

	// A different raw status is a new reason.
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseUnknown, RawName: "STATUS_ODD", Kickoff: kick}, nil)
	h.tick(t)
	h.tick(t)
	if n := len(h.alerts.all()); n != 2 {
		t.Fatalf("alerts = %d, want 2", n)
	}

	// Not listed any more: one more alert, once.
	h.prov.mu.Lock()
	delete(h.prov.status, "e1")
	h.prov.mu.Unlock()
	h.tick(t)
	h.tick(t)
	alerts = h.alerts.all()
	if len(alerts) != 3 || !strings.Contains(alerts[2], "not listed") {
		t.Fatalf("alerts = %q", alerts)
	}
}

func TestStaleAlertsOnce(t *testing.T) {
	h := newHarness(t, Config{})
	id := h.addMatch(t, "e1", kick, domain.StateLive, "msg")
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseLive, RawName: "STATUS_IN_PROGRESS", Kickoff: kick}, nil)
	h.clock.set(kick.Add(5 * time.Hour))
	h.tick(t)
	if n := len(h.alerts.all()); n != 0 {
		t.Fatalf("alerts before stale = %d", n)
	}
	h.clock.set(kick.Add(7 * time.Hour))
	for i := 0; i < 3; i++ {
		h.tick(t)
	}
	alerts := h.alerts.all()
	if len(alerts) != 1 || !strings.Contains(alerts[0], "no final result") || !strings.Contains(alerts[0], "#"+itoa(id)) || !strings.Contains(alerts[0], "STATUS_IN_PROGRESS") {
		t.Fatalf("alerts = %q", alerts)
	}
}

func TestProviderErrorIsolatedAndBackedOff(t *testing.T) {
	h := newHarness(t, Config{})
	bad := h.addMatch(t, "e1", kick, domain.StateLocked, "msg1")
	good := h.addMatch(t, "e2", kick, domain.StateLive, "msg2")
	h.pick(good, "a", domain.OutcomeAway)
	boom := errors.New("espn down")
	h.prov.setStatus("e1", domain.MatchStatus{}, boom)
	h.prov.setStatus("e2", domain.MatchStatus{Phase: domain.PhaseFinal, RawName: "STATUS_FINAL", Kickoff: kick, Winner: domain.OutcomeAway}, nil)
	h.clock.set(kick.Add(2 * time.Hour))

	err := h.s.Tick(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Tick err = %v, want %v", err, boom)
	}
	wantState(t, h, good, domain.StateFinal)
	wantState(t, h, bad, domain.StateLocked)
	if got := h.store.points(); !reflect.DeepEqual(got, map[string]int{"a": 1}) {
		t.Fatalf("points = %v", got)
	}

	// Ticks 2 and 3 retry; after 3 failures only every 5th tick.
	var calledOn []int
	for tick := 2; tick <= 13; tick++ {
		before := h.prov.calls("e1")
		_ = h.s.Tick(context.Background())
		if h.prov.calls("e1") > before {
			calledOn = append(calledOn, tick)
		}
	}
	if want := []int{2, 3, 8, 13}; !reflect.DeepEqual(calledOn, want) {
		t.Fatalf("retried on ticks %v, want %v", calledOn, want)
	}

	// Recovery resets the backoff.
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseFinal, RawName: "STATUS_FINAL", Kickoff: kick, Winner: domain.OutcomeHome}, nil)
	for i := 0; i < 5; i++ {
		_ = h.s.Tick(context.Background())
	}
	wantState(t, h, bad, domain.StateFinal)
}

func TestSyncNowCoversHorizon(t *testing.T) {
	h := newHarness(t, Config{HorizonDays: 2})
	l2, _ := h.store.AddLeague(context.Background(), domain.League{GuildID: "g2", Path: "football/nfl", Name: "NFL", OpenLead: time.Hour})

	yesterday := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	horizon := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	// ESPN lists an evening-Eastern game under the previous date.
	h.prov.list(path, yesterday, pm("y1", domain.PhasePre, "STATUS_SCHEDULED", time.Date(2026, 10, 9, 0, 15, 0, 0, time.UTC)))
	h.prov.list(path, horizon, pm("h1", domain.PhasePre, "STATUS_SCHEDULED", horizon.Add(18*time.Hour)))
	h.prov.list("football/nfl", horizon, pm("n1", domain.PhasePre, "STATUS_SCHEDULED", horizon.Add(17*time.Hour)))
	boom := errors.New("timeout")
	h.prov.scheduleErr[dayKey("football/nfl", t0)] = boom

	err := h.s.SyncNow(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("SyncNow err = %v, want %v", err, boom)
	}
	var want []string
	for _, p := range []string{path, "football/nfl"} {
		for _, d := range []string{"20261008", "20261009", "20261010", "20261011"} {
			want = append(want, p+"|"+d)
		}
	}
	got := append([]string(nil), h.prov.scheduleCalls...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schedule calls = %q, want %q", got, want)
	}

	ms, _ := h.store.ListMatchesByState(context.Background(), domain.StateScheduled)
	byEvent := map[string]domain.Match{}
	for _, m := range ms {
		byEvent[m.ProviderEventID] = m
	}
	for ev, wantLeague := range map[string]domain.League{"y1": h.league, "h1": h.league, "n1": l2} {
		m, ok := byEvent[ev]
		if !ok {
			t.Fatalf("event %s not synced", ev)
		}
		if m.LeagueID != wantLeague.ID || m.GuildID != wantLeague.GuildID {
			t.Fatalf("event %s = %+v, want league %d guild %s", ev, m, wantLeague.ID, wantLeague.GuildID)
		}
	}
	if len(ms) != 3 {
		t.Fatalf("synced %d matches, want 3", len(ms))
	}
}

func TestSettleTwiceDoesNotDoublePoints(t *testing.T) {
	h := newHarness(t, Config{})
	id := h.addMatch(t, "e1", kick, domain.StateLive, "msg")
	for _, u := range []string{"a", "b", "c", "d"} {
		h.pick(id, u, domain.OutcomeHome)
	}
	h.pick(id, "e", domain.OutcomeAway)
	h.prov.setStatus("e1", domain.MatchStatus{Phase: domain.PhaseFinal, RawName: "STATUS_FINAL", Kickoff: kick, Winner: domain.OutcomeAway}, nil)
	h.clock.set(kick.Add(2 * time.Hour))
	h.tick(t)
	first := h.store.points()
	if !reflect.DeepEqual(first, map[string]int{"e": 2}) {
		t.Fatalf("points = %v", first)
	}

	// Simulate a crash after AppendLedger but before UpdateMatch.
	m := h.store.match(id)
	m.State = domain.StateLive
	h.store.put(m)
	h.clock.set(kick.Add(3 * time.Hour))
	h.tick(t)
	wantState(t, h, id, domain.StateFinal)
	if got := h.store.points(); !reflect.DeepEqual(got, first) {
		t.Fatalf("points after re-settle = %v, want %v", got, first)
	}
}

func TestScheduledPastLockLocksWithoutCard(t *testing.T) {
	h := newHarness(t, Config{})
	id := h.addMatch(t, "e1", kick, domain.StateScheduled, "")
	h.clock.set(kick.Add(-30 * time.Second))
	h.tick(t)
	wantState(t, h, id, domain.StateLocked)
	for _, c := range h.cards.calls() {
		if strings.HasPrefix(c, "open:") {
			t.Fatalf("card opened past lock: %q", h.cards.calls())
		}
	}
}

func TestRemovedLeague(t *testing.T) {
	h := newHarness(t, Config{})
	sched := h.addMatch(t, "e1", kick, domain.StateScheduled, "")
	locked := h.addMatch(t, "e2", kick, domain.StateLocked, "msg")
	_ = h.store.RemoveLeague(context.Background(), "g1", h.league.ID)
	h.clock.set(kick.Add(time.Hour))
	h.tick(t)
	h.tick(t)
	wantState(t, h, sched, domain.StateScheduled)
	wantState(t, h, locked, domain.StateLocked)
	if n := len(h.alerts.all()); n != 1 {
		t.Fatalf("alerts = %d, want 1", n)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	h := newHarness(t, Config{Tick: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.s.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		h.prov.mu.Lock()
		n := len(h.prov.scheduleCalls)
		h.prov.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not sync")
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}
