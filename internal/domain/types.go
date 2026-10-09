// Package domain holds the shared types and interfaces every other package
// codes against. It has no dependencies beyond the standard library and does
// no I/O. Changing anything here affects every package: agree the change
// first, do not edit it from inside another work package.
package domain

import "time"

// LockLead is how long before kickoff picks close.
const LockLead = time.Minute

// MatchState is where a match sits in its lifecycle.
//
//	scheduled -> open -> locked -> live -> final
//	any state before final -> postponed -> open (when a new kickoff is set)
//	any state -> void (canceled, or voided by an admin)
type MatchState string

const (
	StateScheduled MatchState = "scheduled" // known from the provider, card not posted yet
	StateOpen      MatchState = "open"      // card posted, picks accepted
	StateLocked    MatchState = "locked"    // picks closed and revealed, not kicked off yet
	StateLive      MatchState = "live"      // in progress
	StateFinal     MatchState = "final"     // settled, points awarded
	StatePostponed MatchState = "postponed" // on hold, picks kept, no points
	StateVoid      MatchState = "void"      // canceled, no points
)

// OutcomeKey names one side of a match. Picks and results refer to outcomes
// by key, so a match's outcomes are unique per key.
type OutcomeKey string

const (
	OutcomeHome OutcomeKey = "home"
	OutcomeAway OutcomeKey = "away"
	OutcomeDraw OutcomeKey = "draw"
	OutcomeNone OutcomeKey = "" // no winner known
)

// League is one provider competition a guild has subscribed a channel to.
type League struct {
	ID        int64
	GuildID   string
	Provider  string        // "espn"
	Path      string        // provider path, e.g. "soccer/fifa.world", "football/nfl"
	Name      string        // display name, e.g. "NFL"
	ChannelID string        // where cards are posted
	OpenLead  time.Duration // how long before kickoff the card is posted
	AllowDraw bool          // true adds a Draw outcome to every match
}

// Team is one competitor as the provider describes it.
type Team struct {
	Name    string // "Spain", "Dallas Cowboys"
	Abbrev  string // "ESP", "DAL"
	LogoURL string
}

// Match is one game people pick on.
type Match struct {
	ID              int64
	GuildID         string
	LeagueID        int64
	ProviderEventID string
	Competition     string // leaderboard bucket, e.g. "NFL 2026"
	Home            Team
	Away            Team
	Kickoff         time.Time // UTC
	LockAt          time.Time // UTC, normally Kickoff - LockLead
	State           MatchState
	MessageID       string // Discord message holding the card; empty until posted
}

// Outcome is one button on a match card.
type Outcome struct {
	MatchID int64
	Key     OutcomeKey
	Label   string // button text, e.g. "Spain"
	Emoji   string // unicode emoji or empty
}

// Pick is one user's choice for one match. There is at most one per match
// and user; changing a pick replaces it.
type Pick struct {
	MatchID   int64
	UserID    string
	Outcome   OutcomeKey
	UpdatedAt time.Time
}

// Result records how a match was settled.
type Result struct {
	MatchID   int64
	Winner    OutcomeKey
	SettledBy string // "auto" or the admin's user ID
	SettledAt time.Time
}

// Ledger reasons written by the game rules.
const (
	ReasonCorrect  = "correct"
	ReasonUnderdog = "underdog"
	ReasonReversal = "reversal"
	ReasonAdjust   = "adjust"
)

// LedgerEntry is one change to a user's points. The ledger is append-only:
// a correction is a new entry with negative points, never an edit.
type LedgerEntry struct {
	ID          int64
	GuildID     string
	UserID      string
	MatchID     int64 // 0 for a manual adjustment
	Competition string
	Points      int
	Reason      string // one of the Reason constants
	Note        string // free text for manual adjustments
	CreatedAt   time.Time
}

// Standing is one row of a leaderboard.
type Standing struct {
	UserID string
	Points int
}
