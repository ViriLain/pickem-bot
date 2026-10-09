// Package card builds the match card view model, handles pick button clicks
// and announces kickoff moves. It knows nothing about Discord: an adapter
// implements Poster and turns a View into a message.
package card

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ViriLain/pickem-bot/internal/domain"
	"github.com/ViriLain/pickem-bot/internal/game"
)

// Button is one pick button on a card.
type Button struct {
	ID, Label, Emoji string
	Disabled         bool
}

// View is a rendered card.
type View struct {
	Title   string
	Lines   []string
	Buttons []Button
}

// Poster is implemented by the Discord adapter.
type Poster interface {
	Post(ctx context.Context, channelID string, v View) (messageID string, err error)
	Edit(ctx context.Context, channelID, messageID string, v View) error
	Announce(ctx context.Context, channelID, text string) error // text may hold <@userID> mentions
}

// Service renders cards and handles picks.
type Service struct {
	store  domain.Store
	clock  domain.Clock
	poster Poster
}

// Replies shown privately to the user who clicked.
const (
	ReplyLocked         = "Picks are locked for this match."
	ReplyUnknownButton  = "That button isn't recognised. Try the latest card."
	ReplyUnknownMatch   = "That match no longer exists."
	ReplyUnknownOutcome = "That isn't an option for this match."
)

// New returns a Service.
func New(store domain.Store, clock domain.Clock, poster Poster) *Service {
	return &Service{store: store, clock: clock, poster: poster}
}

const buttonPrefix = "pick:"

// ButtonID returns the custom ID for a pick button: "pick:<matchID>:<key>".
func ButtonID(matchID int64, key domain.OutcomeKey) string {
	return buttonPrefix + strconv.FormatInt(matchID, 10) + ":" + string(key)
}

// ParseButtonID reverses ButtonID.
func ParseButtonID(id string) (matchID int64, key domain.OutcomeKey, ok bool) {
	rest, found := strings.CutPrefix(id, buttonPrefix)
	if !found {
		return 0, "", false
	}
	idPart, keyPart, found := strings.Cut(rest, ":")
	if !found || keyPart == "" || strings.Contains(keyPart, ":") {
		return 0, "", false
	}
	n, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || n <= 0 {
		return 0, "", false
	}
	return n, domain.OutcomeKey(keyPart), true
}

// Title returns "Home vs Away" when the league allows a draw (soccer
// convention), otherwise "Away at Home".
func Title(m domain.Match, l domain.League) string {
	if l.AllowDraw {
		return m.Home.Name + " vs " + m.Away.Name
	}
	return m.Away.Name + " at " + m.Home.Name
}

func mention(userID string) string { return "<@" + userID + ">" }

func tag(unix int64, style string) string { return fmt.Sprintf("<t:%d:%s>", unix, style) }

func outcomeName(o domain.Outcome) string {
	if o.Emoji == "" {
		return o.Label
	}
	return o.Emoji + " " + o.Label
}

func pluralPicks(n int) string {
	if n == 1 {
		return "1 pick in"
	}
	return fmt.Sprintf("%d picks in", n)
}

// Render builds the card for a match in its current state.
func (s *Service) Render(ctx context.Context, matchID int64) (View, error) {
	m, err := s.store.GetMatch(ctx, matchID)
	if err != nil {
		return View{}, fmt.Errorf("get match %d: %w", matchID, err)
	}
	l, err := s.store.GetLeague(ctx, m.LeagueID)
	if err != nil {
		return View{}, fmt.Errorf("get league %d: %w", m.LeagueID, err)
	}
	return s.render(ctx, m, l)
}

func (s *Service) render(ctx context.Context, m domain.Match, l domain.League) (View, error) {
	outcomes, err := s.store.ListOutcomes(ctx, m.ID)
	if err != nil {
		return View{}, fmt.Errorf("list outcomes %d: %w", m.ID, err)
	}
	picks, err := s.store.ListPicks(ctx, m.ID)
	if err != nil {
		return View{}, fmt.Errorf("list picks %d: %w", m.ID, err)
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].UserID < picks[j].UserID })

	v := View{Title: Title(m, l)}
	comp := m.Competition
	if comp == "" {
		comp = l.Name
	}
	if comp != "" {
		v.Lines = append(v.Lines, comp)
	}
	ko := m.Kickoff.Unix()
	v.Lines = append(v.Lines, "Kickoff: "+tag(ko, "F")+" ("+tag(ko, "R")+")")

	open := m.State == domain.StateOpen
	for _, o := range outcomes {
		v.Buttons = append(v.Buttons, Button{
			ID: ButtonID(m.ID, o.Key), Label: o.Label, Emoji: o.Emoji, Disabled: !open,
		})
	}

	switch m.State {
	case domain.StateLocked, domain.StateLive:
		if m.State == domain.StateLive {
			v.Lines = append(v.Lines, "Live now. Picks are locked.")
		} else {
			v.Lines = append(v.Lines, "Picks are locked.")
		}
		v.Lines = append(v.Lines, revealLines(outcomes, picks)...)
	case domain.StateFinal:
		lines, err := s.finalLines(ctx, m, outcomes, picks)
		if err != nil {
			return View{}, err
		}
		v.Lines = append(v.Lines, lines...)
	case domain.StatePostponed:
		v.Lines = append(v.Lines, "Postponed. Picks are kept. No points until a new time is set.", pluralPicks(len(picks)))
	case domain.StateVoid:
		v.Lines = append(v.Lines, "Voided. No points.")
	default: // scheduled, open, anything unexpected: never reveal picks
		v.Lines = append(v.Lines, pluralPicks(len(picks)))
	}
	v.Lines = append(v.Lines, fmt.Sprintf("Match #%d", m.ID))
	return v, nil
}

// revealLines lists who picked each outcome, with counts.
func revealLines(outcomes []domain.Outcome, picks []domain.Pick) []string {
	by := map[domain.OutcomeKey][]string{}
	for _, p := range picks {
		by[p.Outcome] = append(by[p.Outcome], mention(p.UserID))
	}
	var lines []string
	for _, o := range outcomes {
		users := by[o.Key]
		line := fmt.Sprintf("%s (%d)", outcomeName(o), len(users))
		if len(users) > 0 {
			line += ": " + strings.Join(users, ", ")
		}
		lines = append(lines, line)
	}
	return lines
}

func (s *Service) finalLines(ctx context.Context, m domain.Match, outcomes []domain.Outcome, picks []domain.Pick) ([]string, error) {
	res, err := s.store.GetResult(ctx, m.ID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("get result %d: %w", m.ID, err)
	}
	entries, err := s.store.LedgerForMatch(ctx, m.ID)
	if err != nil {
		return nil, fmt.Errorf("ledger for match %d: %w", m.ID, err)
	}
	net := map[string]int{}
	lastReversal := -1
	for i, e := range entries {
		net[e.UserID] += e.Points
		if e.Reason == domain.ReasonReversal {
			lastReversal = i
		}
	}
	// A re-settlement writes reversals then fresh awards, so only underdog
	// entries after the last reversal count.
	underdog := false
	for _, e := range entries[lastReversal+1:] {
		if e.Reason == domain.ReasonUnderdog && e.Points > 0 {
			underdog = true
		}
	}

	winner := "Unknown"
	for _, o := range outcomes {
		if o.Key == res.Winner {
			winner = outcomeName(o)
		}
	}
	if res.Winner == domain.OutcomeDraw && winner == "Unknown" {
		winner = "Draw"
	}
	lines := []string{"Final. Winner: " + winner}

	var right []string
	for _, p := range picks {
		if res.Winner != domain.OutcomeNone && p.Outcome == res.Winner {
			right = append(right, fmt.Sprintf("%s +%d", mention(p.UserID), net[p.UserID]))
		}
	}
	if len(right) == 0 {
		lines = append(lines, "Nobody got it right.")
	} else {
		lines = append(lines, fmt.Sprintf("Right (%d of %d): %s", len(right), len(picks), strings.Join(right, ", ")))
	}
	if underdog {
		lines = append(lines, "Underdog bonus applied: +1 extra each.")
	}
	return lines, nil
}

// Open posts the card (creating outcomes if needed), saves the message ID
// and sets the match open. An already-posted card is reopened and refreshed.
func (s *Service) Open(ctx context.Context, matchID int64) error {
	m, err := s.store.GetMatch(ctx, matchID)
	if err != nil {
		return fmt.Errorf("get match %d: %w", matchID, err)
	}
	if m.LockAt.IsZero() {
		m.LockAt = game.LockTime(m.Kickoff)
	}
	if m.MessageID != "" {
		m.State = domain.StateOpen
		if err := s.store.UpdateMatch(ctx, m); err != nil {
			return fmt.Errorf("update match %d: %w", matchID, err)
		}
		return s.Refresh(ctx, matchID)
	}
	l, err := s.store.GetLeague(ctx, m.LeagueID)
	if err != nil {
		return fmt.Errorf("get league %d: %w", m.LeagueID, err)
	}
	outcomes, err := s.store.ListOutcomes(ctx, matchID)
	if err != nil {
		return fmt.Errorf("list outcomes %d: %w", matchID, err)
	}
	if len(outcomes) == 0 {
		outcomes = []domain.Outcome{{MatchID: m.ID, Key: domain.OutcomeHome, Label: m.Home.Name, Emoji: FlagEmoji(m.Home.Abbrev)}}
		if l.AllowDraw {
			outcomes = append(outcomes, domain.Outcome{MatchID: m.ID, Key: domain.OutcomeDraw, Label: "Draw", Emoji: "🤝"})
		}
		outcomes = append(outcomes, domain.Outcome{MatchID: m.ID, Key: domain.OutcomeAway, Label: m.Away.Name, Emoji: FlagEmoji(m.Away.Abbrev)})
		if err := s.store.SetOutcomes(ctx, matchID, outcomes); err != nil {
			return fmt.Errorf("set outcomes %d: %w", matchID, err)
		}
	}
	m.State = domain.StateOpen
	v, err := s.render(ctx, m, l)
	if err != nil {
		return err
	}
	msgID, err := s.poster.Post(ctx, l.ChannelID, v)
	if err != nil {
		return fmt.Errorf("post card %d: %w", matchID, err)
	}
	m.MessageID = msgID
	if err := s.store.UpdateMatch(ctx, m); err != nil {
		return fmt.Errorf("update match %d: %w", matchID, err)
	}
	return nil
}

// Refresh re-renders the card and edits the posted message. A match whose
// card was never posted is left alone.
func (s *Service) Refresh(ctx context.Context, matchID int64) error {
	m, err := s.store.GetMatch(ctx, matchID)
	if err != nil {
		return fmt.Errorf("get match %d: %w", matchID, err)
	}
	if m.MessageID == "" {
		return nil
	}
	l, err := s.store.GetLeague(ctx, m.LeagueID)
	if err != nil {
		return fmt.Errorf("get league %d: %w", m.LeagueID, err)
	}
	v, err := s.render(ctx, m, l)
	if err != nil {
		return err
	}
	if err := s.poster.Edit(ctx, l.ChannelID, m.MessageID, v); err != nil {
		return fmt.Errorf("edit card %d: %w", matchID, err)
	}
	return nil
}

// HandlePick records a button click. Refusals come back as a reply with a
// nil error; err is only for real failures. If the pick is saved but the
// card refresh fails, both the reply and the error are returned.
func (s *Service) HandlePick(ctx context.Context, userID, buttonID string) (reply string, err error) {
	matchID, key, ok := ParseButtonID(buttonID)
	if !ok {
		return ReplyUnknownButton, nil
	}
	m, err := s.store.GetMatch(ctx, matchID)
	if errors.Is(err, domain.ErrNotFound) {
		return ReplyUnknownMatch, nil
	}
	if err != nil {
		return "", fmt.Errorf("get match %d: %w", matchID, err)
	}
	now := s.clock.Now()
	if !game.CanPick(now, m) {
		return ReplyLocked, nil
	}
	outcomes, err := s.store.ListOutcomes(ctx, matchID)
	if err != nil {
		return "", fmt.Errorf("list outcomes %d: %w", matchID, err)
	}
	var chosen *domain.Outcome
	for i := range outcomes {
		if outcomes[i].Key == key {
			chosen = &outcomes[i]
		}
	}
	if chosen == nil {
		return ReplyUnknownOutcome, nil
	}
	picks, err := s.store.ListPicks(ctx, matchID)
	if err != nil {
		return "", fmt.Errorf("list picks %d: %w", matchID, err)
	}
	changed := false
	for _, p := range picks {
		if p.UserID == userID && p.Outcome != key {
			changed = true
		}
	}
	if err := s.store.UpsertPick(ctx, domain.Pick{MatchID: matchID, UserID: userID, Outcome: key, UpdatedAt: now}); err != nil {
		return "", fmt.Errorf("upsert pick: %w", err)
	}
	reply = "Locked in: " + chosen.Label
	if changed {
		reply = "Changed to: " + chosen.Label
	}
	if err := s.Refresh(ctx, matchID); err != nil {
		return reply, err
	}
	return reply, nil
}

// AnnounceMove posts in the league channel that the match has a new
// kickoff, mentioning everyone who already picked.
func (s *Service) AnnounceMove(ctx context.Context, matchID int64) error {
	m, err := s.store.GetMatch(ctx, matchID)
	if err != nil {
		return fmt.Errorf("get match %d: %w", matchID, err)
	}
	l, err := s.store.GetLeague(ctx, m.LeagueID)
	if err != nil {
		return fmt.Errorf("get league %d: %w", m.LeagueID, err)
	}
	picks, err := s.store.ListPicks(ctx, matchID)
	if err != nil {
		return fmt.Errorf("list picks %d: %w", matchID, err)
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].UserID < picks[j].UserID })
	var b strings.Builder
	for _, p := range picks {
		b.WriteString(mention(p.UserID))
		b.WriteString(" ")
	}
	fmt.Fprintf(&b, "%s (Match #%d) has a new kickoff: %s. Picks are open to change until 1 minute before kickoff.",
		Title(m, l), m.ID, tag(m.Kickoff.Unix(), "F"))
	if err := s.poster.Announce(ctx, l.ChannelID, b.String()); err != nil {
		return fmt.Errorf("announce move %d: %w", matchID, err)
	}
	return nil
}
