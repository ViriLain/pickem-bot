package domain

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a requested row or event does not exist.
var ErrNotFound = errors.New("not found")

// Store is the bot's persistence. Implementations must be safe for
// concurrent use. All times are stored and returned in UTC.
type Store interface {
	// AddLeague inserts a league and returns it with ID set.
	AddLeague(ctx context.Context, l League) (League, error)
	// RemoveLeague deletes a league subscription. Its matches are kept.
	RemoveLeague(ctx context.Context, guildID string, leagueID int64) error
	// ListLeagues returns a guild's leagues; an empty guildID returns all.
	ListLeagues(ctx context.Context, guildID string) ([]League, error)
	// GetLeague returns one league or ErrNotFound.
	GetLeague(ctx context.Context, leagueID int64) (League, error)

	// UpsertMatch inserts a match, or updates teams, competition and kickoff
	// of the existing row with the same LeagueID and ProviderEventID. It
	// never changes State, LockAt or MessageID of an existing row. It returns
	// the stored match and whether it was newly created.
	UpsertMatch(ctx context.Context, m Match) (stored Match, created bool, err error)
	// UpdateMatch writes Kickoff, LockAt, State and MessageID for m.ID.
	UpdateMatch(ctx context.Context, m Match) error
	// GetMatch returns one match or ErrNotFound.
	GetMatch(ctx context.Context, matchID int64) (Match, error)
	// ListMatchesByState returns matches in any of the given states, ordered
	// by kickoff.
	ListMatchesByState(ctx context.Context, states ...MatchState) ([]Match, error)

	// SetOutcomes replaces the outcomes of a match.
	SetOutcomes(ctx context.Context, matchID int64, outcomes []Outcome) error
	// ListOutcomes returns a match's outcomes in the order home, draw, away.
	ListOutcomes(ctx context.Context, matchID int64) ([]Outcome, error)

	// UpsertPick stores a pick, replacing the user's earlier pick for the
	// match. It does not check lock time; callers use game.CanPick first.
	UpsertPick(ctx context.Context, p Pick) error
	// ListPicks returns every pick for a match.
	ListPicks(ctx context.Context, matchID int64) ([]Pick, error)
	// ListUserOpenPicks returns a user's picks on matches not yet final or void.
	ListUserOpenPicks(ctx context.Context, guildID, userID string) ([]Pick, error)

	// SaveResult stores or replaces a match's result.
	SaveResult(ctx context.Context, r Result) error
	// GetResult returns a match's result or ErrNotFound.
	GetResult(ctx context.Context, matchID int64) (Result, error)

	// AppendLedger adds entries atomically.
	AppendLedger(ctx context.Context, entries []LedgerEntry) error
	// LedgerForMatch returns every entry written for a match, oldest first.
	LedgerForMatch(ctx context.Context, matchID int64) ([]LedgerEntry, error)
	// Leaderboard sums the ledger per user, highest first. An empty
	// competition returns the all-time board.
	Leaderboard(ctx context.Context, guildID, competition string) ([]Standing, error)

	Close() error
}
