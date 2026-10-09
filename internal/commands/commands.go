// Package commands holds slash command logic and specs. It has no Discord
// types: an adapter registers Specs with Discord and calls Dispatch.
package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/internal/game"
)

// MoveLayout is the format /match move accepts, in the Handlers' location.
const MoveLayout = "2006-01-02 15:04"

// DefaultOpenLead is how long before kickoff cards of a new league are posted.
const DefaultOpenLead = 24 * time.Hour

// Caller identifies who ran a command and where.
type Caller struct {
	GuildID, UserID string
	IsAdmin         bool
}

// Reply is what the adapter sends back. Ephemeral replies are private.
type Reply struct {
	Text      string
	Ephemeral bool
}

// Cards is implemented by card.Service.
type Cards interface {
	Refresh(ctx context.Context, matchID int64) error
	AnnounceMove(ctx context.Context, matchID int64) error
}

// Syncer is implemented by the scheduler.
type Syncer interface {
	SyncNow(ctx context.Context) error
}

// Handlers runs slash commands.
type Handlers struct {
	store  domain.Store
	clock  domain.Clock
	cards  Cards
	syncer Syncer
	loc    *time.Location
	specs  map[string]Spec
	run    map[string]func(context.Context, Caller, args) Reply
}

// New returns Handlers. syncer may be nil; loc nil means UTC.
func New(store domain.Store, clock domain.Clock, cards Cards, syncer Syncer, loc *time.Location) *Handlers {
	if loc == nil {
		loc = time.UTC
	}
	h := &Handlers{store: store, clock: clock, cards: cards, syncer: syncer, loc: loc, specs: flatten(Specs())}
	h.run = map[string]func(context.Context, Caller, args) Reply{
		"picks":         h.picks,
		"leaderboard":   h.leaderboard,
		"league add":    h.leagueAdd,
		"league remove": h.leagueRemove,
		"match settle":  h.matchSettle,
		"match void":    h.matchVoid,
		"match reopen":  h.matchReopen,
		"match move":    h.matchMove,
		"points adjust": h.pointsAdjust,
		"sync":          h.sync,
	}
	return h
}

const adminRefusal = "You need the admin role for that."

// Dispatch runs one command. path is "picks", "leaderboard", "league add",
// "match settle", ... ; args maps option name to its value as a string
// (user and channel options arrive as IDs).
func (h *Handlers) Dispatch(ctx context.Context, c Caller, path string, a map[string]string) Reply {
	path = strings.Join(strings.Fields(path), " ")
	spec, ok := h.specs[path]
	fn, ok2 := h.run[path]
	if !ok || !ok2 {
		return private("Unknown command.")
	}
	if spec.Admin && !c.IsAdmin {
		return private(adminRefusal)
	}
	parsed, err := parseArgs(spec, a)
	if err != nil {
		return private(err.Error())
	}
	return fn(ctx, c, parsed)
}

// args holds validated option values.
type args struct {
	str  map[string]string
	ints map[string]int64
	bool map[string]bool
}

func parseArgs(s Spec, raw map[string]string) (args, error) {
	a := args{str: map[string]string{}, ints: map[string]int64{}, bool: map[string]bool{}}
	for _, o := range s.Options {
		v := strings.TrimSpace(raw[o.Name])
		if v == "" {
			if o.Required {
				return a, fmt.Errorf("Missing `%s`: %s.", o.Name, o.Description)
			}
			continue
		}
		switch o.Type {
		case TypeInteger:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return a, fmt.Errorf("`%s` must be a whole number, got %q.", o.Name, v)
			}
			a.ints[o.Name] = n
		case TypeBoolean:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return a, fmt.Errorf("`%s` must be true or false, got %q.", o.Name, v)
			}
			a.bool[o.Name] = b
		}
		a.str[o.Name] = v
	}
	return a, nil
}

func private(text string) Reply { return Reply{Text: text, Ephemeral: true} }

func public(text string) Reply { return Reply{Text: text} }

func failed(what string, err error) Reply {
	return private(fmt.Sprintf("Could not %s: %v", what, err))
}

// saved builds the reply after a data change, keeping the success visible
// when the card could not be updated.
func saved(text string, cardErr error) Reply {
	if cardErr != nil {
		return private(text + fmt.Sprintf("\nThe change was saved, but the card could not be updated: %v", cardErr))
	}
	return private(text)
}

// loadMatch returns a match of the caller's guild; other guilds' matches are
// reported as not found.
func (h *Handlers) loadMatch(ctx context.Context, c Caller, id int64) (domain.Match, *Reply) {
	m, err := h.store.GetMatch(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && m.GuildID != c.GuildID) {
		r := private(fmt.Sprintf("Match #%d not found.", id))
		return m, &r
	}
	if err != nil {
		r := failed("load the match", err)
		return m, &r
	}
	return m, nil
}

func matchName(m domain.Match) string {
	return fmt.Sprintf("Match #%d (%s vs %s)", m.ID, m.Home.Name, m.Away.Name)
}

func outcomeLabel(outs []domain.Outcome, key domain.OutcomeKey) string {
	for _, o := range outs {
		if o.Key == key {
			if o.Label != "" {
				return o.Label
			}
			break
		}
	}
	if key == domain.OutcomeDraw {
		return "Draw"
	}
	return string(key)
}

func (h *Handlers) picks(ctx context.Context, c Caller, _ args) Reply {
	ps, err := h.store.ListUserOpenPicks(ctx, c.GuildID, c.UserID)
	if err != nil {
		return failed("load your picks", err)
	}
	type line struct {
		kickoff time.Time
		text    string
	}
	var lines []line
	for _, p := range ps {
		m, err := h.store.GetMatch(ctx, p.MatchID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && m.GuildID != c.GuildID) {
			continue
		}
		if err != nil {
			return failed("load your picks", err)
		}
		outs, err := h.store.ListOutcomes(ctx, m.ID)
		if err != nil {
			return failed("load your picks", err)
		}
		lines = append(lines, line{m.Kickoff, fmt.Sprintf("%s vs %s: %s (kickoff <t:%d:R>)",
			m.Home.Name, m.Away.Name, outcomeLabel(outs, p.Outcome), m.Kickoff.Unix())})
	}
	if len(lines) == 0 {
		return private("You have no open picks.")
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].kickoff.Before(lines[j].kickoff) })
	var b strings.Builder
	b.WriteString("Your open picks:")
	for _, l := range lines {
		b.WriteString("\n" + l.text)
	}
	return private(b.String())
}

func (h *Handlers) leaderboard(ctx context.Context, c Caller, a args) Reply {
	comp := a.str["competition"]
	rows, err := h.store.Leaderboard(ctx, c.GuildID, comp)
	if err != nil {
		return failed("load the leaderboard", err)
	}
	title := "Leaderboard (all time)"
	if comp != "" {
		title = "Leaderboard: " + comp
	}
	if len(rows) == 0 {
		return public(title + "\nNo points yet.")
	}
	var b strings.Builder
	b.WriteString(title)
	for _, l := range FormatStandings(rows) {
		b.WriteString("\n" + l)
	}
	return public(b.String())
}

// FormatStandings numbers standings (already sorted highest first) with
// shared ranks for ties: 1, 1, 3.
func FormatStandings(rows []domain.Standing) []string {
	out := make([]string, 0, len(rows))
	rank := 0
	for i, r := range rows {
		if i == 0 || r.Points != rows[i-1].Points {
			rank = i + 1
		}
		unit := "points"
		if r.Points == 1 || r.Points == -1 {
			unit = "point"
		}
		out = append(out, fmt.Sprintf("%d. <@%s> %d %s", rank, r.UserID, r.Points, unit))
	}
	return out
}

func (h *Handlers) leagueAdd(ctx context.Context, c Caller, a args) Reply {
	path := strings.Trim(a.str["path"], "/ ")
	if path == "" {
		return private("`path` is empty.")
	}
	name := a.str["name"]
	if name == "" {
		name = path
	}
	existing, err := h.store.ListLeagues(ctx, c.GuildID)
	if err != nil {
		return failed("check existing leagues", err)
	}
	for _, l := range existing {
		if l.Provider == "espn" && strings.EqualFold(l.Path, path) {
			return private(fmt.Sprintf("%s (%s) is already subscribed as league %d in <#%s>. Remove it first to change it.",
				l.Name, l.Path, l.ID, l.ChannelID))
		}
	}
	l, err := h.store.AddLeague(ctx, domain.League{
		GuildID: c.GuildID, Provider: "espn", Path: path, Name: name,
		ChannelID: a.str["channel"], OpenLead: DefaultOpenLead, AllowDraw: a.bool["allow_draw"],
	})
	if err != nil {
		return failed("add the league", err)
	}
	draw := ""
	if l.AllowDraw {
		draw = " with a Draw option"
	}
	return private(fmt.Sprintf("Added league %d: %s (%s). Cards go to <#%s> 24h before kickoff%s.",
		l.ID, l.Name, l.Path, l.ChannelID, draw))
}

func (h *Handlers) leagueRemove(ctx context.Context, c Caller, a args) Reply {
	id := a.ints["league"]
	l, err := h.store.GetLeague(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && l.GuildID != c.GuildID) {
		return private(fmt.Sprintf("League %d not found.", id))
	}
	if err != nil {
		return failed("load the league", err)
	}
	if err := h.store.RemoveLeague(ctx, c.GuildID, id); err != nil {
		return failed("remove the league", err)
	}
	return private(fmt.Sprintf("Removed league %d: %s. Existing matches and points are kept.", l.ID, l.Name))
}

func (h *Handlers) matchSettle(ctx context.Context, c Caller, a args) Reply {
	winner := domain.OutcomeKey(strings.ToLower(a.str["winner"]))
	switch winner {
	case domain.OutcomeHome, domain.OutcomeAway, domain.OutcomeDraw:
	default:
		return private("`winner` must be home, away or draw.")
	}
	m, bad := h.loadMatch(ctx, c, a.ints["match"])
	if bad != nil {
		return *bad
	}
	if m.State == domain.StateVoid {
		return private(matchName(m) + " is void and cannot be settled.")
	}
	outs, err := h.store.ListOutcomes(ctx, m.ID)
	if err != nil {
		return failed("load outcomes", err)
	}
	has := false
	for _, o := range outs {
		has = has || o.Key == winner
	}
	if !has {
		return private(fmt.Sprintf("%s has no %s outcome.", matchName(m), winner))
	}
	picks, err := h.store.ListPicks(ctx, m.ID)
	if err != nil {
		return failed("load picks", err)
	}
	prior, err := h.store.LedgerForMatch(ctx, m.ID)
	if err != nil {
		return failed("load the ledger", err)
	}
	now := h.clock.Now()
	entries := game.Settle(m, picks, winner, prior, now)
	if len(entries) > 0 {
		if err := h.store.AppendLedger(ctx, entries); err != nil {
			return failed("write points", err)
		}
	}
	if err := h.store.SaveResult(ctx, domain.Result{MatchID: m.ID, Winner: winner, SettledBy: c.UserID, SettledAt: now}); err != nil {
		return failed("save the result", err)
	}
	m.State = domain.StateFinal
	if err := h.store.UpdateMatch(ctx, m); err != nil {
		return failed("update the match", err)
	}
	scored := 0
	for _, p := range picks {
		if p.Outcome == winner {
			scored++
		}
	}
	text := fmt.Sprintf("Settled %s: %s won. %d of %d pickers scored.",
		matchName(m), outcomeLabel(outs, winner), scored, len(picks))
	return saved(text, h.cards.Refresh(ctx, m.ID))
}

func (h *Handlers) matchVoid(ctx context.Context, c Caller, a args) Reply {
	m, bad := h.loadMatch(ctx, c, a.ints["match"])
	if bad != nil {
		return *bad
	}
	if m.State == domain.StateVoid {
		return private(matchName(m) + " is already void.")
	}
	prior, err := h.store.LedgerForMatch(ctx, m.ID)
	if err != nil {
		return failed("load the ledger", err)
	}
	entries := game.Void(m, prior, h.clock.Now())
	if len(entries) > 0 {
		if err := h.store.AppendLedger(ctx, entries); err != nil {
			return failed("reverse points", err)
		}
	}
	m.State = domain.StateVoid
	if err := h.store.UpdateMatch(ctx, m); err != nil {
		return failed("update the match", err)
	}
	return saved(fmt.Sprintf("Voided %s. Points reversed for %d people.", matchName(m), len(entries)),
		h.cards.Refresh(ctx, m.ID))
}

func (h *Handlers) matchReopen(ctx context.Context, c Caller, a args) Reply {
	m, bad := h.loadMatch(ctx, c, a.ints["match"])
	if bad != nil {
		return *bad
	}
	if m.State == domain.StateFinal {
		return private(matchName(m) + " is settled. Void it first.")
	}
	if m.State == domain.StateOpen {
		return private(matchName(m) + " is already open.")
	}
	now := h.clock.Now()
	if !now.Before(m.Kickoff) {
		return private(matchName(m) + " has already kicked off. Use `/match move` to set a new kickoff.")
	}
	lock := game.LockTime(m.Kickoff)
	if !now.Before(lock) {
		return private(matchName(m) + " is too close to kickoff to reopen. Use `/match move` to set a new kickoff.")
	}
	m.State = domain.StateOpen
	m.LockAt = lock
	if err := h.store.UpdateMatch(ctx, m); err != nil {
		return failed("update the match", err)
	}
	return saved(fmt.Sprintf("Reopened %s. Picks lock <t:%d:R>.", matchName(m), lock.Unix()),
		h.cards.Refresh(ctx, m.ID))
}

func (h *Handlers) matchMove(ctx context.Context, c Caller, a args) Reply {
	t, err := time.ParseInLocation(MoveLayout, a.str["when"], h.loc)
	if err != nil {
		return private(fmt.Sprintf("`when` must look like %s (time zone %s).", MoveLayout, h.loc))
	}
	m, bad := h.loadMatch(ctx, c, a.ints["match"])
	if bad != nil {
		return *bad
	}
	if m.State == domain.StateFinal {
		return private(matchName(m) + " is settled. Void it first.")
	}
	if !h.clock.Now().Before(game.LockTime(t)) {
		return private("The new kickoff must be in the future.")
	}
	m = game.Move(m, t)
	if err := h.store.UpdateMatch(ctx, m); err != nil {
		return failed("update the match", err)
	}
	text := fmt.Sprintf("Moved %s to <t:%d:F>. Picks are open until <t:%d:R>.", matchName(m), m.Kickoff.Unix(), m.LockAt.Unix())
	cardErr := h.cards.Refresh(ctx, m.ID)
	if err := h.cards.AnnounceMove(ctx, m.ID); err != nil && cardErr == nil {
		cardErr = fmt.Errorf("announcement failed: %w", err)
	}
	return saved(text, cardErr)
}

func (h *Handlers) pointsAdjust(ctx context.Context, c Caller, a args) Reply {
	n := a.ints["n"]
	if n == 0 {
		return private("`n` cannot be zero.")
	}
	const limit = 1_000_000
	if n > limit || n < -limit {
		return private("`n` is too large.")
	}
	user := strings.Trim(a.str["user"], "<@!>")
	e := domain.LedgerEntry{
		GuildID: c.GuildID, UserID: user, MatchID: 0, Competition: "",
		Points: int(n), Reason: domain.ReasonAdjust, Note: a.str["reason"], CreatedAt: h.clock.Now(),
	}
	if err := h.store.AppendLedger(ctx, []domain.LedgerEntry{e}); err != nil {
		return failed("adjust points", err)
	}
	return private(fmt.Sprintf("Gave <@%s> %+d points: %s", user, n, e.Note))
}

func (h *Handlers) sync(ctx context.Context, _ Caller, _ args) Reply {
	if h.syncer == nil {
		return private("Sync is not available yet.")
	}
	if err := h.syncer.SyncNow(ctx); err != nil {
		return failed("sync", err)
	}
	return private("Sync done.")
}
