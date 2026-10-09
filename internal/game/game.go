// Package game holds the pick'em rules as pure functions. It does no I/O:
// callers load state from the Store, call these, and write the results back.
package game

import (
	"sort"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

// UnderdogMinPickers is the fewest pickers a match needs before the underdog
// bonus can apply.
const UnderdogMinPickers = 4

// LockTime returns when picks close for a given kickoff.
func LockTime(kickoff time.Time) time.Time { return kickoff.Add(-domain.LockLead) }

// CanPick reports whether a pick is accepted for m at time now.
func CanPick(now time.Time, m domain.Match) bool {
	return m.State == domain.StateOpen && now.Before(m.LockAt)
}

// IsUnderdog reports whether the winning side earns the bonus: fewer than a
// third of pickers chose it and at least UnderdogMinPickers people picked.
func IsUnderdog(winnerPicks, totalPicks int) bool {
	return totalPicks >= UnderdogMinPickers && winnerPicks > 0 && winnerPicks*3 < totalPicks
}

// Score returns the ledger entries a fresh settlement awards.
func Score(m domain.Match, picks []domain.Pick, winner domain.OutcomeKey, now time.Time) []domain.LedgerEntry {
	var winners []string
	for _, p := range picks {
		if p.Outcome == winner && winner != domain.OutcomeNone {
			winners = append(winners, p.UserID)
		}
	}
	sort.Strings(winners)
	bonus := IsUnderdog(len(winners), len(picks))
	var out []domain.LedgerEntry
	for _, u := range winners {
		out = append(out, entry(m, u, 1, domain.ReasonCorrect, now))
		if bonus {
			out = append(out, entry(m, u, 1, domain.ReasonUnderdog, now))
		}
	}
	return out
}

// Reverse returns entries that cancel the net effect of prior, which must be
// every ledger entry already written for one match.
func Reverse(m domain.Match, prior []domain.LedgerEntry, now time.Time) []domain.LedgerEntry {
	net := map[string]int{}
	for _, e := range prior {
		net[e.UserID] += e.Points
	}
	users := make([]string, 0, len(net))
	for u, n := range net {
		if n != 0 {
			users = append(users, u)
		}
	}
	sort.Strings(users)
	var out []domain.LedgerEntry
	for _, u := range users {
		out = append(out, entry(m, u, -net[u], domain.ReasonReversal, now))
	}
	return out
}

// Settle returns the entries to append when m is settled with winner. prior
// is every entry already written for the match; anything it awarded is
// reversed first, so settling twice nets to the latest result.
func Settle(m domain.Match, picks []domain.Pick, winner domain.OutcomeKey, prior []domain.LedgerEntry, now time.Time) []domain.LedgerEntry {
	return append(Reverse(m, prior, now), Score(m, picks, winner, now)...)
}

// Void returns the entries to append when m is voided.
func Void(m domain.Match, prior []domain.LedgerEntry, now time.Time) []domain.LedgerEntry {
	return Reverse(m, prior, now)
}

// Postpone puts m on hold. Picks are kept and nothing is scored.
func Postpone(m domain.Match) domain.Match {
	m.State = domain.StatePostponed
	return m
}

// Move gives m a new kickoff, reopens picks and sets the new lock time.
func Move(m domain.Match, newKickoff time.Time) domain.Match {
	m.Kickoff = newKickoff.UTC()
	m.LockAt = LockTime(m.Kickoff)
	m.State = domain.StateOpen
	return m
}

func entry(m domain.Match, user string, pts int, reason string, now time.Time) domain.LedgerEntry {
	return domain.LedgerEntry{
		GuildID: m.GuildID, UserID: user, MatchID: m.ID, Competition: m.Competition,
		Points: pts, Reason: reason, CreatedAt: now,
	}
}
