package domain

import (
	"context"
	"time"
)

// Phase is the provider's view of a game, reduced to what the bot acts on.
type Phase string

const (
	PhasePre       Phase = "pre"
	PhaseLive      Phase = "live"
	PhaseFinal     Phase = "final"
	PhasePostponed Phase = "postponed"
	PhaseCanceled  Phase = "canceled"
	// PhaseUnknown is any status the provider code does not recognise. The
	// scheduler alerts an admin instead of guessing.
	PhaseUnknown Phase = "unknown"
)

// MatchStatus is a point-in-time reading of one game.
type MatchStatus struct {
	Phase   Phase
	RawName string    // provider's own status name, for logs and alerts
	Kickoff time.Time // UTC; may differ from an earlier reading if rescheduled
	// Winner is set only when Phase is PhaseFinal. A finished game with no
	// winning side is OutcomeDraw.
	Winner OutcomeKey
}

// ProviderMatch is one game as listed on a provider's schedule.
type ProviderMatch struct {
	EventID     string
	Competition string // e.g. "2026-27 English Premier League"
	Home        Team
	Away        Team
	Status      MatchStatus
}

// Provider reads schedules and results from a sports data source.
// Implementations must be safe for concurrent use.
type Provider interface {
	// Schedule lists every game in the league on the given calendar day.
	// leaguePath is League.Path. Implementations must query by explicit
	// date: undated provider endpoints have returned stale data.
	Schedule(ctx context.Context, leaguePath string, day time.Time) ([]ProviderMatch, error)

	// Status returns the current reading for one game. day is the game's
	// last known kickoff day. ErrNotFound means the event is not listed on
	// that day, which can mean it was moved.
	Status(ctx context.Context, leaguePath, eventID string, day time.Time) (MatchStatus, error)
}
