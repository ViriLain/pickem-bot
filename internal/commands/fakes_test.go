package commands

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeStore struct {
	leagues  map[int64]domain.League
	matches  map[int64]domain.Match
	outcomes map[int64][]domain.Outcome
	picks    map[int64][]domain.Pick
	results  map[int64]domain.Result
	ledger   []domain.LedgerEntry
	nextID   int64
	writes   int
	failAdd  error
}

func newStore() *fakeStore {
	return &fakeStore{
		leagues: map[int64]domain.League{}, matches: map[int64]domain.Match{},
		outcomes: map[int64][]domain.Outcome{}, picks: map[int64][]domain.Pick{},
		results: map[int64]domain.Result{}, nextID: 100,
	}
}

func (s *fakeStore) AddLeague(_ context.Context, l domain.League) (domain.League, error) {
	if s.failAdd != nil {
		return l, s.failAdd
	}
	s.writes++
	s.nextID++
	l.ID = s.nextID
	s.leagues[l.ID] = l
	return l, nil
}

func (s *fakeStore) RemoveLeague(_ context.Context, guildID string, id int64) error {
	s.writes++
	if l, ok := s.leagues[id]; !ok || l.GuildID != guildID {
		return domain.ErrNotFound
	}
	delete(s.leagues, id)
	return nil
}

func (s *fakeStore) ListLeagues(_ context.Context, guildID string) ([]domain.League, error) {
	var out []domain.League
	for _, l := range s.leagues {
		if guildID == "" || l.GuildID == guildID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *fakeStore) GetLeague(_ context.Context, id int64) (domain.League, error) {
	l, ok := s.leagues[id]
	if !ok {
		return l, domain.ErrNotFound
	}
	return l, nil
}

func (s *fakeStore) UpsertMatch(context.Context, domain.Match) (domain.Match, bool, error) {
	return domain.Match{}, false, errors.New("unused")
}

func (s *fakeStore) UpdateMatch(_ context.Context, m domain.Match) error {
	s.writes++
	s.matches[m.ID] = m
	return nil
}

func (s *fakeStore) GetMatch(_ context.Context, id int64) (domain.Match, error) {
	m, ok := s.matches[id]
	if !ok {
		return m, domain.ErrNotFound
	}
	return m, nil
}

func (s *fakeStore) ListMatchesByState(context.Context, ...domain.MatchState) ([]domain.Match, error) {
	return nil, nil
}

func (s *fakeStore) SetOutcomes(_ context.Context, id int64, o []domain.Outcome) error {
	s.writes++
	s.outcomes[id] = o
	return nil
}

func (s *fakeStore) ListOutcomes(_ context.Context, id int64) ([]domain.Outcome, error) {
	return s.outcomes[id], nil
}

func (s *fakeStore) UpsertPick(context.Context, domain.Pick) error { s.writes++; return nil }

func (s *fakeStore) ListPicks(_ context.Context, id int64) ([]domain.Pick, error) {
	return s.picks[id], nil
}

func (s *fakeStore) ListUserOpenPicks(_ context.Context, guildID, userID string) ([]domain.Pick, error) {
	var out []domain.Pick
	for id, ps := range s.picks {
		m := s.matches[id]
		if m.GuildID != guildID || m.State == domain.StateFinal || m.State == domain.StateVoid {
			continue
		}
		for _, p := range ps {
			if p.UserID == userID {
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MatchID < out[j].MatchID })
	return out, nil
}

func (s *fakeStore) SaveResult(_ context.Context, r domain.Result) error {
	s.writes++
	s.results[r.MatchID] = r
	return nil
}

func (s *fakeStore) GetResult(_ context.Context, id int64) (domain.Result, error) {
	r, ok := s.results[id]
	if !ok {
		return r, domain.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) AppendLedger(_ context.Context, es []domain.LedgerEntry) error {
	s.writes++
	s.ledger = append(s.ledger, es...)
	return nil
}

func (s *fakeStore) LedgerForMatch(_ context.Context, id int64) ([]domain.LedgerEntry, error) {
	var out []domain.LedgerEntry
	for _, e := range s.ledger {
		if e.MatchID == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *fakeStore) Leaderboard(_ context.Context, guildID, comp string) ([]domain.Standing, error) {
	sum := map[string]int{}
	for _, e := range s.ledger {
		if e.GuildID == guildID && (comp == "" || e.Competition == comp) {
			sum[e.UserID] += e.Points
		}
	}
	var out []domain.Standing
	for u, p := range sum {
		out = append(out, domain.Standing{UserID: u, Points: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Points != out[j].Points {
			return out[i].Points > out[j].Points
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func (s *fakeStore) Close() error { return nil }

// net returns each user's total points in the ledger.
func (s *fakeStore) net() map[string]int {
	out := map[string]int{}
	for _, e := range s.ledger {
		out[e.UserID] += e.Points
	}
	for u, n := range out {
		if n == 0 {
			delete(out, u)
		}
	}
	return out
}

type fakeCards struct {
	calls       []string
	refreshErr  error
	announceErr error
}

func (c *fakeCards) Refresh(context.Context, int64) error {
	c.calls = append(c.calls, "refresh")
	return c.refreshErr
}

func (c *fakeCards) AnnounceMove(context.Context, int64) error {
	c.calls = append(c.calls, "announce")
	return c.announceErr
}

type fakeSyncer struct {
	err   error
	calls int
}

func (s *fakeSyncer) SyncNow(context.Context) error { s.calls++; return s.err }
