package commands

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
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

var (
	admin = Caller{GuildID: "g1", UserID: "adm", IsAdmin: true}
	user  = Caller{GuildID: "g1", UserID: "u1"}
)

type env struct {
	store  *fakeStore
	clock  *fakeClock
	cards  *fakeCards
	syncer *fakeSyncer
	h      *Handlers
}

func newEnv(withSyncer bool) *env {
	e := &env{store: newStore(), clock: &fakeClock{now: t0}, cards: &fakeCards{}, syncer: &fakeSyncer{}}
	s := e.store
	s.leagues[1] = domain.League{ID: 1, GuildID: "g1", Provider: "espn", Path: "football/nfl", Name: "NFL", ChannelID: "c1"}
	s.leagues[2] = domain.League{ID: 2, GuildID: "g2", Provider: "espn", Path: "soccer/eng.1", Name: "EPL", ChannelID: "c9"}
	ko := t0.Add(2 * time.Hour)
	s.matches[1] = domain.Match{ID: 1, GuildID: "g1", LeagueID: 1, Competition: "NFL 2026",
		Home: domain.Team{Name: "Dallas"}, Away: domain.Team{Name: "Philly"},
		Kickoff: ko, LockAt: ko.Add(-time.Minute), State: domain.StateOpen}
	s.outcomes[1] = []domain.Outcome{{MatchID: 1, Key: "home", Label: "Dallas"}, {MatchID: 1, Key: "away", Label: "Philly"}}
	s.picks[1] = []domain.Pick{
		{MatchID: 1, UserID: "u1", Outcome: "home"}, {MatchID: 1, UserID: "u2", Outcome: "away"},
		{MatchID: 1, UserID: "u3", Outcome: "away"}, {MatchID: 1, UserID: "u4", Outcome: "away"},
		{MatchID: 1, UserID: "u5", Outcome: "away"},
	}
	s.matches[2] = domain.Match{ID: 2, GuildID: "g2", LeagueID: 2, Competition: "EPL",
		Home: domain.Team{Name: "Arsenal"}, Away: domain.Team{Name: "Spurs"},
		Kickoff: ko, LockAt: ko.Add(-time.Minute), State: domain.StateOpen}
	s.outcomes[2] = []domain.Outcome{{MatchID: 2, Key: "home"}, {MatchID: 2, Key: "draw"}, {MatchID: 2, Key: "away"}}
	past := t0.Add(-3 * time.Hour)
	s.matches[3] = domain.Match{ID: 3, GuildID: "g1", LeagueID: 1, Competition: "NFL 2026",
		Home: domain.Team{Name: "Spain"}, Away: domain.Team{Name: "Italy"},
		Kickoff: past, LockAt: past.Add(-time.Minute), State: domain.StatePostponed}
	s.outcomes[3] = []domain.Outcome{{MatchID: 3, Key: "home", Label: "Spain"}, {MatchID: 3, Key: "draw", Label: "Draw"}, {MatchID: 3, Key: "away", Label: "Italy"}}
	s.picks[3] = []domain.Pick{{MatchID: 3, UserID: "u1", Outcome: "draw"}}
	var sy Syncer
	if withSyncer {
		sy = e.syncer
	}
	e.h = New(s, e.clock, e.cards, sy, time.UTC)
	return e
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name      string
		noSyncer  bool
		setup     func(*env)
		caller    Caller
		path      string
		args      map[string]string
		want      []string // substrings of the reply
		ephemeral bool
		check     func(*testing.T, *env)
	}{
		{name: "unknown", caller: admin, path: "match explode", want: []string{"Unknown command"}, ephemeral: true},
		{name: "group alone is unknown", caller: admin, path: "match", want: []string{"Unknown command"}, ephemeral: true},

		{name: "picks", caller: user, path: "picks", ephemeral: true,
			want: []string{"Spain vs Italy: Draw", "Dallas vs Philly: Dallas", "<t:" + itoa(t0.Add(2*time.Hour).Unix()) + ":R>"}},
		{name: "picks none", caller: Caller{GuildID: "g1", UserID: "nobody"}, path: "picks", ephemeral: true,
			want: []string{"no open picks"}},

		{name: "leaderboard empty", caller: user, path: "leaderboard", want: []string{"No points yet"}},
		{name: "leaderboard competition filter", caller: user, path: "leaderboard",
			args: map[string]string{"competition": "EPL"},
			setup: func(e *env) {
				e.store.ledger = []domain.LedgerEntry{
					{GuildID: "g1", UserID: "a", Competition: "NFL 2026", Points: 3},
					{GuildID: "g1", UserID: "b", Competition: "EPL", Points: 2},
					{GuildID: "g2", UserID: "c", Competition: "EPL", Points: 9},
				}
			},
			want: []string{"Leaderboard: EPL", "1. <@b> 2 points"},
		},

		{name: "league add", caller: admin, path: "league add", ephemeral: true,
			args: map[string]string{"path": "soccer/fifa.world", "channel": "c5", "allow_draw": "true"},
			want: []string{"Added league", "soccer/fifa.world", "<#c5>"},
			check: func(t *testing.T, e *env) {
				var got domain.League
				for _, l := range e.store.leagues {
					if l.Path == "soccer/fifa.world" {
						got = l
					}
				}
				want := domain.League{ID: got.ID, GuildID: "g1", Provider: "espn", Path: "soccer/fifa.world",
					Name: "soccer/fifa.world", ChannelID: "c5", OpenLead: 24 * time.Hour, AllowDraw: true}
				if got != want {
					t.Errorf("league = %+v, want %+v", got, want)
				}
			}},
		{name: "league add named", caller: admin, path: "league add", ephemeral: true,
			args: map[string]string{"path": "football/college-football", "channel": "c5", "name": "CFB"},
			want: []string{"CFB"}},
		{name: "league add duplicate", caller: admin, path: "league add", ephemeral: true,
			args:  map[string]string{"path": "football/nfl", "channel": "c7"},
			want:  []string{"already subscribed"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "league add same path other guild ok", caller: admin, path: "league add", ephemeral: true,
			args: map[string]string{"path": "soccer/eng.1", "channel": "c7"}, want: []string{"Added league"}},
		{name: "league add missing channel", caller: admin, path: "league add", ephemeral: true,
			args: map[string]string{"path": "football/nfl"}, want: []string{"Missing `channel`"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "league add bad bool", caller: admin, path: "league add", ephemeral: true,
			args: map[string]string{"path": "x/y", "channel": "c", "allow_draw": "maybe"}, want: []string{"true or false"}},
		{name: "league add store error", caller: admin, path: "league add", ephemeral: true,
			setup: func(e *env) { e.store.failAdd = errors.New("disk full") },
			args:  map[string]string{"path": "x/y", "channel": "c"}, want: []string{"Could not add the league", "disk full"}},

		{name: "league remove", caller: admin, path: "league remove", ephemeral: true,
			args: map[string]string{"league": "1"}, want: []string{"Removed league 1"},
			check: func(t *testing.T, e *env) {
				if _, ok := e.store.leagues[1]; ok {
					t.Error("league 1 still present")
				}
			}},
		{name: "league remove other guild", caller: admin, path: "league remove", ephemeral: true,
			args: map[string]string{"league": "2"}, want: []string{"not found"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "league remove not integer", caller: admin, path: "league remove", ephemeral: true,
			args: map[string]string{"league": "nfl"}, want: []string{"whole number"}},

		{name: "settle", caller: admin, path: "match settle", ephemeral: true,
			args: map[string]string{"match": "1", "winner": "home"},
			want: []string{"Dallas won", "1 of 5"},
			check: func(t *testing.T, e *env) {
				if m := e.store.matches[1]; m.State != domain.StateFinal {
					t.Errorf("state = %s", m.State)
				}
				r := e.store.results[1]
				if r.Winner != "home" || r.SettledBy != "adm" || !r.SettledAt.Equal(t0) {
					t.Errorf("result = %+v", r)
				}
				// 1 of 5 picked the winner: correct + underdog.
				if got := e.store.net(); !reflect.DeepEqual(got, map[string]int{"u1": 2}) {
					t.Errorf("net = %v", got)
				}
				wantCalls(t, e, "refresh")
			}},
		{name: "settle bad winner", caller: admin, path: "match settle", ephemeral: true,
			args: map[string]string{"match": "1", "winner": "both"}, want: []string{"home, away or draw"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "settle draw on no-draw match", caller: admin, path: "match settle", ephemeral: true,
			args: map[string]string{"match": "1", "winner": "draw"}, want: []string{"no draw outcome"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "settle void match", caller: admin, path: "match settle", ephemeral: true,
			setup: func(e *env) { setState(e, 1, domain.StateVoid) },
			args:  map[string]string{"match": "1", "winner": "home"}, want: []string{"void"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "settle other guild", caller: admin, path: "match settle", ephemeral: true,
			args: map[string]string{"match": "2", "winner": "home"}, want: []string{"Match #2 not found"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "settle missing match", caller: admin, path: "match settle", ephemeral: true,
			args: map[string]string{"match": "99", "winner": "home"}, want: []string{"Match #99 not found"}},
		{name: "settle missing winner", caller: admin, path: "match settle", ephemeral: true,
			args: map[string]string{"match": "1"}, want: []string{"Missing `winner`"}},
		{name: "settle card failure keeps success", caller: admin, path: "match settle", ephemeral: true,
			setup: func(e *env) { e.cards.refreshErr = errors.New("discord down") },
			args:  map[string]string{"match": "1", "winner": "away"},
			want:  []string{"Settled", "saved", "card could not be updated", "discord down"},
			check: func(t *testing.T, e *env) {
				if e.store.matches[1].State != domain.StateFinal {
					t.Error("not final")
				}
			}},

		{name: "void", caller: admin, path: "match void", ephemeral: true,
			setup: func(e *env) {
				e.h.Dispatch(context.Background(), admin, "match settle", map[string]string{"match": "1", "winner": "away"})
				e.cards.calls = nil
			},
			args: map[string]string{"match": "1"}, want: []string{"Voided"},
			check: func(t *testing.T, e *env) {
				if e.store.matches[1].State != domain.StateVoid {
					t.Error("not void")
				}
				if got := e.store.net(); len(got) != 0 {
					t.Errorf("net after void = %v", got)
				}
				wantCalls(t, e, "refresh")
			}},
		{name: "void twice", caller: admin, path: "match void", ephemeral: true,
			setup: func(e *env) { setState(e, 1, domain.StateVoid) },
			args:  map[string]string{"match": "1"}, want: []string{"already void"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},

		{name: "reopen", caller: admin, path: "match reopen", ephemeral: true,
			setup: func(e *env) { setState(e, 1, domain.StateLocked) },
			args:  map[string]string{"match": "1"}, want: []string{"Reopened"},
			check: func(t *testing.T, e *env) {
				m := e.store.matches[1]
				if m.State != domain.StateOpen || !m.LockAt.Equal(m.Kickoff.Add(-time.Minute)) {
					t.Errorf("match = %+v", m)
				}
				wantCalls(t, e, "refresh")
			}},
		{name: "reopen after kickoff", caller: admin, path: "match reopen", ephemeral: true,
			args: map[string]string{"match": "3"}, want: []string{"/match move"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "reopen inside lock lead", caller: admin, path: "match reopen", ephemeral: true,
			setup: func(e *env) {
				setState(e, 1, domain.StateLocked)
				e.clock.now = e.store.matches[1].Kickoff.Add(-30 * time.Second)
				e.store.writes = 0
			},
			args: map[string]string{"match": "1"}, want: []string{"/match move"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "reopen settled", caller: admin, path: "match reopen", ephemeral: true,
			setup: func(e *env) { setState(e, 1, domain.StateFinal) },
			args:  map[string]string{"match": "1"}, want: []string{"Void it first"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},

		{name: "move", caller: admin, path: "match move", ephemeral: true,
			args: map[string]string{"match": "3", "when": "2026-10-12 18:30"}, want: []string{"Moved"},
			check: func(t *testing.T, e *env) {
				m := e.store.matches[3]
				ko := time.Date(2026, 10, 12, 18, 30, 0, 0, time.UTC)
				if m.State != domain.StateOpen || !m.Kickoff.Equal(ko) || !m.LockAt.Equal(ko.Add(-time.Minute)) {
					t.Errorf("match = %+v", m)
				}
				wantCalls(t, e, "refresh", "announce")
			}},
		{name: "move bad time", caller: admin, path: "match move", ephemeral: true,
			args: map[string]string{"match": "3", "when": "tomorrow"}, want: []string{"2006-01-02 15:04"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "move into past", caller: admin, path: "match move", ephemeral: true,
			args: map[string]string{"match": "3", "when": "2026-10-01 10:00"}, want: []string{"future"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "move settled", caller: admin, path: "match move", ephemeral: true,
			setup: func(e *env) { setState(e, 3, domain.StateFinal) },
			args:  map[string]string{"match": "3", "when": "2026-10-12 18:30"}, want: []string{"Void it first"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "move announce failure keeps success", caller: admin, path: "match move", ephemeral: true,
			setup: func(e *env) { e.cards.announceErr = errors.New("rate limited") },
			args:  map[string]string{"match": "3", "when": "2026-10-12 18:30"},
			want:  []string{"Moved", "saved", "rate limited"},
			check: func(t *testing.T, e *env) {
				if e.store.matches[3].State != domain.StateOpen {
					t.Error("not open")
				}
			}},

		{name: "points adjust", caller: admin, path: "points adjust", ephemeral: true,
			args: map[string]string{"user": "u9", "n": "-3", "reason": "late payment of snacks"},
			want: []string{"<@u9>", "-3"},
			check: func(t *testing.T, e *env) {
				want := []domain.LedgerEntry{{GuildID: "g1", UserID: "u9", Points: -3, Reason: domain.ReasonAdjust,
					Note: "late payment of snacks", CreatedAt: t0}}
				if !reflect.DeepEqual(e.store.ledger, want) {
					t.Errorf("ledger = %+v", e.store.ledger)
				}
			}},
		{name: "points adjust zero", caller: admin, path: "points adjust", ephemeral: true,
			args: map[string]string{"user": "u9", "n": "0", "reason": "x"}, want: []string{"cannot be zero"},
			check: func(t *testing.T, e *env) { noWrites(t, e) }},
		{name: "points adjust missing reason", caller: admin, path: "points adjust", ephemeral: true,
			args: map[string]string{"user": "u9", "n": "2"}, want: []string{"Missing `reason`"}},

		{name: "sync", caller: admin, path: "sync", ephemeral: true, want: []string{"Sync done"},
			check: func(t *testing.T, e *env) {
				if e.syncer.calls != 1 {
					t.Errorf("SyncNow calls = %d", e.syncer.calls)
				}
			}},
		{name: "sync error", caller: admin, path: "sync", ephemeral: true,
			setup: func(e *env) { e.syncer.err = errors.New("espn 503") }, want: []string{"espn 503"}},
		{name: "sync nil", caller: admin, path: "sync", noSyncer: true, ephemeral: true,
			want: []string{"Sync is not available yet."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(!tt.noSyncer)
			if tt.setup != nil {
				tt.setup(e)
			}
			got := e.h.Dispatch(context.Background(), tt.caller, tt.path, tt.args)
			for _, w := range tt.want {
				if !strings.Contains(got.Text, w) {
					t.Errorf("reply %q does not contain %q", got.Text, w)
				}
			}
			if got.Ephemeral != tt.ephemeral {
				t.Errorf("ephemeral = %v, want %v (reply %q)", got.Ephemeral, tt.ephemeral, got.Text)
			}
			if tt.check != nil {
				tt.check(t, e)
			}
		})
	}
}

func TestNonAdminRefused(t *testing.T) {
	validArgs := map[string]map[string]string{
		"league add":    {"path": "a/b", "channel": "c"},
		"league remove": {"league": "1"},
		"match settle":  {"match": "1", "winner": "home"},
		"match void":    {"match": "1"},
		"match reopen":  {"match": "1"},
		"match move":    {"match": "3", "when": "2026-10-12 18:30"},
		"points adjust": {"user": "u2", "n": "5", "reason": "x"},
		"sync":          {},
	}
	paths := adminPaths()
	if len(paths) != len(validArgs) {
		t.Fatalf("admin paths %v, test covers %d", paths, len(validArgs))
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			e := newEnv(true)
			// Sanity: the args are valid for an admin.
			ea := newEnv(true)
			for _, x := range []*env{e, ea} {
				setState(x, 1, domain.StateLocked) // so reopen has work to do
			}
			if r := ea.h.Dispatch(context.Background(), admin, p, validArgs[p]); ea.store.writes == 0 && ea.syncer.calls == 0 {
				t.Fatalf("admin run of %s did nothing: %q", p, r.Text)
			}
			r := e.h.Dispatch(context.Background(), user, p, validArgs[p])
			if r.Text != adminRefusal || !r.Ephemeral {
				t.Errorf("reply = %+v", r)
			}
			noWrites(t, e)
			if e.syncer.calls != 0 {
				t.Error("syncer called")
			}
		})
	}
}

func TestSettleTwiceNets(t *testing.T) {
	tests := []struct {
		name    string
		winners []domain.OutcomeKey
		want    map[string]int
	}{
		{"home then away", []domain.OutcomeKey{"home", "away"}, map[string]int{"u2": 1, "u3": 1, "u4": 1, "u5": 1}},
		{"away then home", []domain.OutcomeKey{"away", "home"}, map[string]int{"u1": 2}},
		{"home twice", []domain.OutcomeKey{"home", "home"}, map[string]int{"u1": 2}},
		{"home, away, home", []domain.OutcomeKey{"home", "away", "home"}, map[string]int{"u1": 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(true)
			for _, w := range tt.winners {
				r := e.h.Dispatch(context.Background(), admin, "match settle", map[string]string{"match": "1", "winner": string(w)})
				if !strings.Contains(r.Text, "Settled") {
					t.Fatalf("settle %s: %q", w, r.Text)
				}
			}
			if got := e.store.net(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("net = %v, want %v", got, tt.want)
			}
			if r := e.store.results[1]; r.Winner != tt.winners[len(tt.winners)-1] {
				t.Errorf("result winner = %s", r.Winner)
			}
		})
	}
}

func TestFormatStandings(t *testing.T) {
	tests := []struct {
		name string
		in   []domain.Standing
		want []string
	}{
		{"ties share rank", []domain.Standing{st("a", 5), st("b", 5), st("c", 3), st("d", 1)},
			[]string{"1. <@a> 5 points", "1. <@b> 5 points", "3. <@c> 3 points", "4. <@d> 1 point"}},
		{"tie in middle", []domain.Standing{st("a", 4), st("b", 2), st("c", 2), st("d", 2), st("e", 0)},
			[]string{"1. <@a> 4 points", "2. <@b> 2 points", "2. <@c> 2 points", "2. <@d> 2 points", "5. <@e> 0 points"}},
		{"single", []domain.Standing{st("a", -1)}, []string{"1. <@a> -1 point"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatStandings(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLeaderboardReply(t *testing.T) {
	e := newEnv(true)
	e.store.ledger = []domain.LedgerEntry{
		{GuildID: "g1", UserID: "a", Points: 2}, {GuildID: "g1", UserID: "b", Points: 2},
		{GuildID: "g1", UserID: "c", Points: 1}, {GuildID: "g2", UserID: "z", Points: 50},
	}
	r := e.h.Dispatch(context.Background(), user, "leaderboard", nil)
	want := "Leaderboard (all time)\n1. <@a> 2 points\n1. <@b> 2 points\n3. <@c> 1 point"
	if r.Text != want || r.Ephemeral {
		t.Errorf("reply = %+v, want public %q", r, want)
	}
}

func TestSpecsMatchDispatch(t *testing.T) {
	e := newEnv(true)
	var specPaths []string
	for p := range flatten(Specs()) {
		specPaths = append(specPaths, p)
	}
	var runPaths []string
	for p := range e.h.run {
		runPaths = append(runPaths, p)
	}
	sort.Strings(specPaths)
	sort.Strings(runPaths)
	if !reflect.DeepEqual(specPaths, runPaths) {
		t.Fatalf("spec paths %v != dispatch paths %v", specPaths, runPaths)
	}
	want := []string{"leaderboard", "league add", "league remove", "match move", "match reopen",
		"match settle", "match void", "picks", "points adjust", "sync"}
	if !reflect.DeepEqual(specPaths, want) {
		t.Errorf("paths = %v, want %v", specPaths, want)
	}
	// Every spec path is accepted by Dispatch (never "Unknown command").
	for _, p := range specPaths {
		if r := e.h.Dispatch(context.Background(), admin, p, nil); strings.Contains(r.Text, "Unknown command") {
			t.Errorf("Dispatch rejected spec path %q", p)
		}
	}
	// Spec hygiene: Discord names are lowercase, descriptions 1-100 chars, types known.
	types := map[string]bool{TypeString: true, TypeInteger: true, TypeBoolean: true, TypeUser: true, TypeChannel: true}
	var walk func(s Spec)
	walk = func(s Spec) {
		if s.Name != strings.ToLower(s.Name) || s.Description == "" || len(s.Description) > 100 {
			t.Errorf("bad spec %q", s.Name)
		}
		for _, o := range s.Options {
			if !types[o.Type] || o.Description == "" || len(o.Description) > 100 {
				t.Errorf("bad option %s.%s", s.Name, o.Name)
			}
		}
		for _, sub := range s.Sub {
			walk(sub)
		}
	}
	for _, s := range Specs() {
		walk(s)
	}
	admins := adminPaths()
	if want := []string{"league add", "league remove", "match move", "match reopen", "match settle",
		"match void", "points adjust", "sync"}; !reflect.DeepEqual(admins, want) {
		t.Errorf("admin paths = %v", admins)
	}
}

func adminPaths() []string {
	var out []string
	for p, s := range flatten(Specs()) {
		if s.Admin {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func noWrites(t *testing.T, e *env) {
	t.Helper()
	if e.store.writes != 0 {
		t.Errorf("store writes = %d, want 0", e.store.writes)
	}
	if len(e.cards.calls) != 0 {
		t.Errorf("card calls = %v, want none", e.cards.calls)
	}
}

func wantCalls(t *testing.T, e *env, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(e.cards.calls, want) {
		t.Errorf("card calls = %v, want %v", e.cards.calls, want)
	}
}

func setState(e *env, id int64, s domain.MatchState) {
	m := e.store.matches[id]
	m.State = s
	e.store.matches[id] = m
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func st(u string, p int) domain.Standing { return domain.Standing{UserID: u, Points: p} }
