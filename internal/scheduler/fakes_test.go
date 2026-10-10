package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/internal/game"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// fakeStore mirrors the real store's behaviour for the methods the
// scheduler calls.
type fakeStore struct {
	mu       sync.Mutex
	leagues  map[int64]domain.League
	matches  map[int64]domain.Match
	outcomes map[int64][]domain.Outcome
	picks    map[int64][]domain.Pick
	results  map[int64]domain.Result
	ledger   []domain.LedgerEntry
	nextID   int64
}

func newStore() *fakeStore {
	return &fakeStore{
		leagues: map[int64]domain.League{}, matches: map[int64]domain.Match{},
		outcomes: map[int64][]domain.Outcome{}, picks: map[int64][]domain.Pick{},
		results: map[int64]domain.Result{},
	}
}

func (s *fakeStore) AddLeague(_ context.Context, l domain.League) (domain.League, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	l.ID = s.nextID
	s.leagues[l.ID] = l
	return l, nil
}

func (s *fakeStore) RemoveLeague(_ context.Context, _ string, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.leagues, id)
	return nil
}

func (s *fakeStore) ListLeagues(_ context.Context, guildID string) ([]domain.League, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.League
	for _, l := range s.leagues {
		if guildID == "" || l.GuildID == guildID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) GetLeague(_ context.Context, id int64) (domain.League, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leagues[id]
	if !ok {
		return l, domain.ErrNotFound
	}
	return l, nil
}

func (s *fakeStore) UpsertMatch(_ context.Context, m domain.Match) (domain.Match, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, old := range s.matches {
		if old.LeagueID == m.LeagueID && old.ProviderEventID == m.ProviderEventID {
			old.Competition, old.Home, old.Away, old.Kickoff = m.Competition, m.Home, m.Away, m.Kickoff.UTC()
			s.matches[id] = old
			return old, false, nil
		}
	}
	s.nextID++
	m.ID = s.nextID
	m.Kickoff = m.Kickoff.UTC()
	if m.LockAt.IsZero() {
		m.LockAt = game.LockTime(m.Kickoff)
	}
	if m.State == "" {
		m.State = domain.StateScheduled
	}
	s.matches[m.ID] = m
	return m, true, nil
}

func (s *fakeStore) UpdateMatch(_ context.Context, m domain.Match) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.matches[m.ID]
	if !ok {
		return domain.ErrNotFound
	}
	old.Kickoff, old.LockAt, old.State, old.MessageID = m.Kickoff, m.LockAt, m.State, m.MessageID
	s.matches[m.ID] = old
	return nil
}

func (s *fakeStore) GetMatch(_ context.Context, id int64) (domain.Match, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.matches[id]
	if !ok {
		return m, domain.ErrNotFound
	}
	return m, nil
}

func (s *fakeStore) ListMatchesByState(_ context.Context, states ...domain.MatchState) ([]domain.Match, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[domain.MatchState]bool{}
	for _, st := range states {
		want[st] = true
	}
	var out []domain.Match
	for _, m := range s.matches {
		if want[m.State] {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Kickoff.Equal(out[j].Kickoff) {
			return out[i].Kickoff.Before(out[j].Kickoff)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *fakeStore) SetOutcomes(_ context.Context, id int64, o []domain.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[id] = o
	return nil
}

func (s *fakeStore) ListOutcomes(_ context.Context, id int64) ([]domain.Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcomes[id], nil
}

func (s *fakeStore) UpsertPick(_ context.Context, p domain.Pick) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Pick(nil), s.picks[id]...), nil
}

func (s *fakeStore) ListUserOpenPicks(context.Context, string, string) ([]domain.Pick, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeStore) SaveResult(_ context.Context, r domain.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[r.MatchID] = r
	return nil
}

func (s *fakeStore) GetResult(_ context.Context, id int64) (domain.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[id]
	if !ok {
		return r, domain.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) AppendLedger(_ context.Context, entries []domain.LedgerEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		s.nextID++
		e.ID = s.nextID
		s.ledger = append(s.ledger, e)
	}
	return nil
}

func (s *fakeStore) LedgerForMatch(_ context.Context, id int64) ([]domain.LedgerEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.LedgerEntry
	for _, e := range s.ledger {
		if e.MatchID == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *fakeStore) Leaderboard(context.Context, string, string) ([]domain.Standing, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeStore) Close() error { return nil }

// points sums the ledger per user.
func (s *fakeStore) points() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, e := range s.ledger {
		out[e.UserID] += e.Points
	}
	for u, p := range out {
		if p == 0 {
			delete(out, u)
		}
	}
	return out
}

func (s *fakeStore) match(id int64) domain.Match {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.matches[id]
}

func (s *fakeStore) put(m domain.Match) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.matches[m.ID] = m
}

// fakeProvider serves schedules keyed by path and UTC day, and statuses
// keyed by event ID.
type fakeProvider struct {
	mu            sync.Mutex
	schedule      map[string][]domain.ProviderMatch
	scheduleErr   map[string]error
	scheduleCalls []string
	status        map[string]domain.MatchStatus
	statusErr     map[string]error
	statusCalls   map[string]int
}

func newProvider() *fakeProvider {
	return &fakeProvider{
		schedule: map[string][]domain.ProviderMatch{}, scheduleErr: map[string]error{},
		status: map[string]domain.MatchStatus{}, statusErr: map[string]error{},
		statusCalls: map[string]int{},
	}
}

func dayKey(path string, day time.Time) string { return path + "|" + day.UTC().Format("20060102") }

func (p *fakeProvider) list(path string, day time.Time, ms ...domain.ProviderMatch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.schedule[dayKey(path, day)] = ms
}

func (p *fakeProvider) setStatus(eventID string, st domain.MatchStatus, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status[eventID] = st
	p.statusErr[eventID] = err
}

func (p *fakeProvider) calls(eventID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusCalls[eventID]
}

func (p *fakeProvider) Schedule(_ context.Context, path string, day time.Time) ([]domain.ProviderMatch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := dayKey(path, day)
	p.scheduleCalls = append(p.scheduleCalls, k)
	if err := p.scheduleErr[k]; err != nil {
		return nil, err
	}
	return p.schedule[k], nil
}

func (p *fakeProvider) Status(_ context.Context, _, eventID string, _ time.Time) (domain.MatchStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCalls[eventID]++
	if err := p.statusErr[eventID]; err != nil {
		return domain.MatchStatus{}, err
	}
	st, ok := p.status[eventID]
	if !ok {
		return st, fmt.Errorf("event %s: %w", eventID, domain.ErrNotFound)
	}
	return st, nil
}

// fakeCards records calls. Open behaves like card.Service: it saves a
// message ID and sets the match open.
type fakeCards struct {
	mu    sync.Mutex
	store *fakeStore
	log   []string
}

func (c *fakeCards) record(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, s)
}

func (c *fakeCards) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.log...)
}

func (c *fakeCards) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = nil
}

func (c *fakeCards) Open(ctx context.Context, id int64) error {
	c.record(fmt.Sprintf("open:%d", id))
	m, err := c.store.GetMatch(ctx, id)
	if err != nil {
		return err
	}
	m.State = domain.StateOpen
	if m.MessageID == "" {
		m.MessageID = fmt.Sprintf("msg-%d", id)
	}
	return c.store.UpdateMatch(ctx, m)
}

func (c *fakeCards) Refresh(ctx context.Context, id int64) error {
	m, err := c.store.GetMatch(ctx, id)
	if err != nil {
		return err
	}
	c.record(fmt.Sprintf("refresh:%d:%s", id, m.State))
	return nil
}

func (c *fakeCards) AnnounceMove(_ context.Context, id int64) error {
	c.record(fmt.Sprintf("announce:%d", id))
	return nil
}

type fakeAlerter struct {
	mu    sync.Mutex
	texts []string
}

func (a *fakeAlerter) Alert(_ context.Context, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.texts = append(a.texts, text)
	return nil
}

func (a *fakeAlerter) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.texts...)
}
