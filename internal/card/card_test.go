package card

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

var kickoff = time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC)

// ---- fakes ----

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeStore struct {
	domain.Store // unimplemented methods panic
	leagues      map[int64]domain.League
	matches      map[int64]domain.Match
	outcomes     map[int64][]domain.Outcome
	picks        map[int64][]domain.Pick
	results      map[int64]domain.Result
	ledger       map[int64][]domain.LedgerEntry
	failPicks    bool
}

func newStore() *fakeStore {
	return &fakeStore{
		leagues:  map[int64]domain.League{},
		matches:  map[int64]domain.Match{},
		outcomes: map[int64][]domain.Outcome{},
		picks:    map[int64][]domain.Pick{},
		results:  map[int64]domain.Result{},
		ledger:   map[int64][]domain.LedgerEntry{},
	}
}

func (s *fakeStore) GetLeague(_ context.Context, id int64) (domain.League, error) {
	l, ok := s.leagues[id]
	if !ok {
		return l, domain.ErrNotFound
	}
	return l, nil
}

func (s *fakeStore) GetMatch(_ context.Context, id int64) (domain.Match, error) {
	m, ok := s.matches[id]
	if !ok {
		return m, domain.ErrNotFound
	}
	return m, nil
}

func (s *fakeStore) UpdateMatch(_ context.Context, m domain.Match) error {
	old, ok := s.matches[m.ID]
	if !ok {
		return domain.ErrNotFound
	}
	old.Kickoff, old.LockAt, old.State, old.MessageID = m.Kickoff, m.LockAt, m.State, m.MessageID
	s.matches[m.ID] = old
	return nil
}

func (s *fakeStore) SetOutcomes(_ context.Context, id int64, os []domain.Outcome) error {
	s.outcomes[id] = append([]domain.Outcome(nil), os...)
	return nil
}

func (s *fakeStore) ListOutcomes(_ context.Context, id int64) ([]domain.Outcome, error) {
	return s.outcomes[id], nil
}

func (s *fakeStore) UpsertPick(_ context.Context, p domain.Pick) error {
	if s.failPicks {
		return errors.New("disk on fire")
	}
	ps := s.picks[p.MatchID]
	for i := range ps {
		if ps[i].UserID == p.UserID {
			ps[i] = p
			return nil
		}
	}
	s.picks[p.MatchID] = append(ps, p)
	return nil
}

func (s *fakeStore) ListPicks(_ context.Context, id int64) ([]domain.Pick, error) {
	return append([]domain.Pick(nil), s.picks[id]...), nil
}

func (s *fakeStore) GetResult(_ context.Context, id int64) (domain.Result, error) {
	r, ok := s.results[id]
	if !ok {
		return r, domain.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) LedgerForMatch(_ context.Context, id int64) ([]domain.LedgerEntry, error) {
	return s.ledger[id], nil
}

type posted struct {
	channel, message string
	view             View
}

type fakePoster struct {
	posts     []posted
	edits     []posted
	announces []posted // view unused; message holds text
}

func (p *fakePoster) Post(_ context.Context, ch string, v View) (string, error) {
	id := fmt.Sprintf("msg%d", len(p.posts)+1)
	p.posts = append(p.posts, posted{ch, id, v})
	return id, nil
}

func (p *fakePoster) Edit(_ context.Context, ch, msg string, v View) error {
	p.edits = append(p.edits, posted{ch, msg, v})
	return nil
}

func (p *fakePoster) Announce(_ context.Context, ch, text string) error {
	p.announces = append(p.announces, posted{channel: ch, message: text})
	return nil
}

// ---- fixtures ----

const (
	soccerLeague = 1
	nflLeague    = 2
)

func fixture(t *testing.T) (*Service, *fakeStore, *fakePoster, *fakeClock) {
	t.Helper()
	st := newStore()
	st.leagues[soccerLeague] = domain.League{ID: soccerLeague, Name: "World Cup", ChannelID: "chan-wc", AllowDraw: true}
	st.leagues[nflLeague] = domain.League{ID: nflLeague, Name: "NFL", ChannelID: "chan-nfl"}
	st.matches[7] = domain.Match{
		ID: 7, LeagueID: soccerLeague, Competition: "FIFA World Cup 2026",
		Home: domain.Team{Name: "Spain", Abbrev: "ESP"}, Away: domain.Team{Name: "Portugal", Abbrev: "POR"},
		Kickoff: kickoff, LockAt: kickoff.Add(-domain.LockLead), State: domain.StateOpen, MessageID: "m7",
	}
	st.outcomes[7] = []domain.Outcome{
		{MatchID: 7, Key: domain.OutcomeHome, Label: "Spain", Emoji: FlagEmoji("ESP")},
		{MatchID: 7, Key: domain.OutcomeDraw, Label: "Draw"},
		{MatchID: 7, Key: domain.OutcomeAway, Label: "Portugal", Emoji: FlagEmoji("POR")},
	}
	st.picks[7] = []domain.Pick{
		{MatchID: 7, UserID: "111", Outcome: domain.OutcomeHome},
		{MatchID: 7, UserID: "222", Outcome: domain.OutcomeHome},
		{MatchID: 7, UserID: "333", Outcome: domain.OutcomeHome},
		{MatchID: 7, UserID: "444", Outcome: domain.OutcomeAway},
	}
	st.matches[8] = domain.Match{
		ID: 8, LeagueID: nflLeague, Competition: "NFL 2026",
		Home: domain.Team{Name: "Dallas Cowboys", Abbrev: "DAL"}, Away: domain.Team{Name: "New York Giants", Abbrev: "NYG"},
		Kickoff: kickoff, State: domain.StateScheduled,
	}
	clock := &fakeClock{now: kickoff.Add(-time.Hour)}
	poster := &fakePoster{}
	return New(st, clock, poster), st, poster, clock
}

func setState(st *fakeStore, id int64, state domain.MatchState) {
	m := st.matches[id]
	m.State = state
	st.matches[id] = m
}

func joined(v View) string { return v.Title + "\n" + strings.Join(v.Lines, "\n") }

// ---- tests ----

func TestRender(t *testing.T) {
	users := []string{"111", "222", "333", "444"}
	cases := []struct {
		name         string
		state        domain.MatchState
		setup        func(st *fakeStore)
		enabled      bool
		want         []string
		revealsUsers bool
	}{
		{name: "scheduled", state: domain.StateScheduled,
			want: []string{"Spain vs Portugal", "FIFA World Cup 2026", "<t:1791738000:F>", "<t:1791738000:R>", "4 picks in", "Match #7"}},
		{name: "open", state: domain.StateOpen, enabled: true,
			want: []string{"4 picks in", "Match #7"}},
		{name: "postponed", state: domain.StatePostponed,
			want: []string{"Postponed. Picks are kept. No points until a new time is set.", "Match #7"}},
		{name: "void", state: domain.StateVoid,
			want: []string{"Voided. No points.", "Match #7"}},
		{name: "locked", state: domain.StateLocked, revealsUsers: true,
			want: []string{"Picks are locked.", "Spain (3): <@111>, <@222>, <@333>", "Draw (0)", "Portugal (1): <@444>"}},
		{name: "live", state: domain.StateLive, revealsUsers: true,
			want: []string{"Live now", "Portugal (1): <@444>"}},
		{name: "final underdog", state: domain.StateFinal, revealsUsers: true,
			setup: func(st *fakeStore) {
				st.results[7] = domain.Result{MatchID: 7, Winner: domain.OutcomeAway}
				st.ledger[7] = []domain.LedgerEntry{
					{UserID: "444", MatchID: 7, Points: 1, Reason: domain.ReasonCorrect},
					{UserID: "444", MatchID: 7, Points: 1, Reason: domain.ReasonUnderdog},
				}
			},
			want: []string{"Winner: " + FlagEmoji("POR") + " Portugal", "Right (1 of 4): <@444> +2", "Underdog bonus applied"}},
		{name: "final resettled", state: domain.StateFinal, revealsUsers: true,
			setup: func(st *fakeStore) {
				st.results[7] = domain.Result{MatchID: 7, Winner: domain.OutcomeHome}
				st.ledger[7] = []domain.LedgerEntry{
					{UserID: "444", Points: 1, Reason: domain.ReasonCorrect},
					{UserID: "444", Points: 1, Reason: domain.ReasonUnderdog},
					{UserID: "444", Points: -2, Reason: domain.ReasonReversal},
					{UserID: "111", Points: 1, Reason: domain.ReasonCorrect},
					{UserID: "222", Points: 1, Reason: domain.ReasonCorrect},
					{UserID: "333", Points: 1, Reason: domain.ReasonCorrect},
				}
			},
			want: []string{"Winner: " + FlagEmoji("ESP") + " Spain", "Right (3 of 4): <@111> +1, <@222> +1, <@333> +1"}},
		{name: "final nobody right", state: domain.StateFinal,
			setup: func(st *fakeStore) { st.results[7] = domain.Result{MatchID: 7, Winner: domain.OutcomeDraw} },
			want:  []string{"Winner: Draw", "Nobody got it right."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, st, _, _ := fixture(t)
			setState(st, 7, c.state)
			if c.setup != nil {
				c.setup(st)
			}
			v, err := svc.Render(context.Background(), 7)
			if err != nil {
				t.Fatal(err)
			}
			text := joined(v)
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Errorf("missing %q in:\n%s", w, text)
				}
			}
			if !c.revealsUsers {
				for _, u := range users {
					if strings.Contains(text, u) {
						t.Errorf("view leaks user %s:\n%s", u, text)
					}
				}
				for _, b := range v.Buttons {
					if strings.Contains(b.ID+b.Label, "111") {
						t.Errorf("button leaks user: %+v", b)
					}
				}
			}
			if c.name == "final resettled" && strings.Contains(text, "Underdog") {
				t.Errorf("reversed underdog bonus still shown:\n%s", text)
			}
			if len(v.Buttons) != 3 {
				t.Fatalf("buttons = %d, want 3", len(v.Buttons))
			}
			for _, b := range v.Buttons {
				if b.Disabled == c.enabled {
					t.Errorf("button %s disabled=%v in state %s", b.ID, b.Disabled, c.state)
				}
			}
			if v.Buttons[0].ID != "pick:7:home" || v.Buttons[0].Label != "Spain" || v.Buttons[0].Emoji != FlagEmoji("ESP") {
				t.Errorf("first button = %+v", v.Buttons[0])
			}
		})
	}
}

func TestRenderTitleNoDraw(t *testing.T) {
	svc, _, _, _ := fixture(t)
	v, err := svc.Render(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "New York Giants at Dallas Cowboys" {
		t.Errorf("title = %q", v.Title)
	}
}

func TestOpen(t *testing.T) {
	cases := []struct {
		name     string
		league   int64
		wantKeys []domain.OutcomeKey
		channel  string
	}{
		{"with draw", soccerLeague, []domain.OutcomeKey{domain.OutcomeHome, domain.OutcomeDraw, domain.OutcomeAway}, "chan-wc"},
		{"without draw", nflLeague, []domain.OutcomeKey{domain.OutcomeHome, domain.OutcomeAway}, "chan-nfl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, st, poster, _ := fixture(t)
			m := st.matches[8]
			m.LeagueID = c.league
			m.Home = domain.Team{Name: "Mexico", Abbrev: "MEX"}
			m.Away = domain.Team{Name: "Canada", Abbrev: "CAN"}
			st.matches[8] = m

			if err := svc.Open(context.Background(), 8); err != nil {
				t.Fatal(err)
			}
			got := st.outcomes[8]
			if len(got) != len(c.wantKeys) {
				t.Fatalf("outcomes = %+v", got)
			}
			for i, k := range c.wantKeys {
				if got[i].Key != k {
					t.Errorf("outcome %d key = %s, want %s", i, got[i].Key, k)
				}
			}
			if got[0].Label != "Mexico" || got[0].Emoji != "🇲🇽" {
				t.Errorf("home outcome = %+v", got[0])
			}
			if last := got[len(got)-1]; last.Label != "Canada" || last.Emoji != "🇨🇦" {
				t.Errorf("away outcome = %+v", last)
			}
			if len(poster.posts) != 1 || poster.posts[0].channel != c.channel {
				t.Fatalf("posts = %+v", poster.posts)
			}
			for _, b := range poster.posts[0].view.Buttons {
				if b.Disabled {
					t.Errorf("posted button disabled: %+v", b)
				}
			}
			stored := st.matches[8]
			if stored.State != domain.StateOpen || stored.MessageID != "msg1" {
				t.Errorf("stored match = %+v", stored)
			}
			if !stored.LockAt.Equal(kickoff.Add(-domain.LockLead)) {
				t.Errorf("LockAt = %v", stored.LockAt)
			}
		})
	}
}

func TestOpenAlreadyPosted(t *testing.T) {
	svc, st, poster, _ := fixture(t)
	setState(st, 7, domain.StatePostponed)
	if err := svc.Open(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if len(poster.posts) != 0 || len(poster.edits) != 1 || poster.edits[0].message != "m7" {
		t.Errorf("posts=%v edits=%v", poster.posts, poster.edits)
	}
	if st.matches[7].State != domain.StateOpen {
		t.Errorf("state = %s", st.matches[7].State)
	}
}

func TestRefreshUnposted(t *testing.T) {
	svc, _, poster, _ := fixture(t)
	if err := svc.Refresh(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	if len(poster.edits) != 0 {
		t.Errorf("edited an unposted card: %+v", poster.edits)
	}
}

func TestHandlePick(t *testing.T) {
	cases := []struct {
		name      string
		user      string
		button    string
		now       time.Time
		wantReply string
		wantPick  domain.OutcomeKey // "" means no pick stored for user
		wantEdits int
	}{
		{"new pick", "555", "pick:7:draw", kickoff.Add(-time.Hour), "Locked in: Draw", domain.OutcomeDraw, 1},
		{"same pick again", "111", "pick:7:home", kickoff.Add(-time.Hour), "Locked in: Spain", domain.OutcomeHome, 1},
		{"change pick", "111", "pick:7:away", kickoff.Add(-time.Hour), "Changed to: Portugal", domain.OutcomeAway, 1},
		{"after lock", "555", "pick:7:home", kickoff.Add(-30 * time.Second), ReplyLocked, "", 0},
		{"unknown button", "555", "vote:7:home", kickoff.Add(-time.Hour), ReplyUnknownButton, "", 0},
		{"unknown outcome", "555", "pick:7:tie", kickoff.Add(-time.Hour), ReplyUnknownOutcome, "", 0},
		{"unknown match", "555", "pick:99:home", kickoff.Add(-time.Hour), ReplyUnknownMatch, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, st, poster, clock := fixture(t)
			clock.now = c.now
			reply, err := svc.HandlePick(context.Background(), c.user, c.button)
			if err != nil {
				t.Fatal(err)
			}
			if reply != c.wantReply {
				t.Errorf("reply = %q, want %q", reply, c.wantReply)
			}
			var got domain.OutcomeKey
			for _, p := range st.picks[7] {
				if p.UserID == c.user {
					got = p.Outcome
				}
			}
			if got != c.wantPick {
				t.Errorf("stored pick = %q, want %q", got, c.wantPick)
			}
			if len(poster.edits) != c.wantEdits {
				t.Fatalf("edits = %d, want %d", len(poster.edits), c.wantEdits)
			}
			if c.wantEdits > 0 {
				text := joined(poster.edits[0].view)
				if strings.Contains(text, c.user) {
					t.Errorf("refreshed open card leaks user:\n%s", text)
				}
			}
		})
	}
}

func TestHandlePickStoreError(t *testing.T) {
	svc, st, _, _ := fixture(t)
	st.failPicks = true
	if _, err := svc.HandlePick(context.Background(), "555", "pick:7:home"); err == nil {
		t.Error("want error on store failure")
	}
}

func TestAnnounceMove(t *testing.T) {
	cases := []struct {
		name   string
		picks  []domain.Pick
		want   []string
		absent []string
	}{
		{"with pickers", []domain.Pick{{MatchID: 7, UserID: "222"}, {MatchID: 7, UserID: "111"}},
			[]string{"<@111> <@222> Spain vs Portugal", "<t:1791738000:F>", "1 minute before kickoff"}, nil},
		{"no pickers", nil,
			[]string{"Spain vs Portugal", "<t:1791738000:F>", "open to change"}, []string{"<@"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, st, poster, _ := fixture(t)
			st.picks[7] = c.picks
			if err := svc.AnnounceMove(context.Background(), 7); err != nil {
				t.Fatal(err)
			}
			if len(poster.announces) != 1 || poster.announces[0].channel != "chan-wc" {
				t.Fatalf("announces = %+v", poster.announces)
			}
			text := poster.announces[0].message
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Errorf("missing %q in %q", w, text)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(text, a) {
					t.Errorf("unexpected %q in %q", a, text)
				}
			}
		})
	}
}

func TestButtonID(t *testing.T) {
	for _, k := range []domain.OutcomeKey{domain.OutcomeHome, domain.OutcomeAway, domain.OutcomeDraw} {
		id := ButtonID(42, k)
		m, key, ok := ParseButtonID(id)
		if !ok || m != 42 || key != k {
			t.Errorf("round trip %q -> %d %q %v", id, m, key, ok)
		}
	}
	if got := ButtonID(7, domain.OutcomeHome); got != "pick:7:home" {
		t.Errorf("ButtonID = %q", got)
	}
	for _, bad := range []string{"", "pick:", "pick:7", "pick:7:", "pick::home", "pick:x:home", "pick:-1:home", "pick:0:home", "vote:7:home", "pick:7:home:extra", "PICK:7:home"} {
		if _, _, ok := ParseButtonID(bad); ok {
			t.Errorf("ParseButtonID(%q) ok", bad)
		}
	}
}

func TestFlagEmoji(t *testing.T) {
	cases := map[string]string{
		"ESP": "🇪🇸",
		"USA": "🇺🇸",
		"ger": "🇩🇪",
		"KSA": "🇸🇦",
		"CUW": "🇨🇼",
		"ENG": "🏴󠁧󠁢󠁥󠁮󠁧󠁿",
		"SCO": "🏴󠁧󠁢󠁳󠁣󠁴󠁿",
		"WAL": "🏴󠁧󠁢󠁷󠁬󠁳󠁿",
		"DAL": "",
		"":    "",
	}
	for in, want := range cases {
		if got := FlagEmoji(in); got != want {
			t.Errorf("FlagEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}
