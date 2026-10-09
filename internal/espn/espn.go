// Package espn implements domain.Provider over ESPN's unofficial scoreboard
// API (no key). It uses only the standard library.
package espn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

const (
	defaultBaseURL = "https://site.api.espn.com/apis/site/v2/sports"
	userAgent      = "pickem-bot/1.0 (+discord friend-group pick'em)"
	maxBodyBytes   = 8 << 20 // 8 MiB; a day's scoreboard is far smaller
)

// Client reads ESPN's scoreboard API. It is safe for concurrent use.
type Client struct {
	client  *http.Client
	baseURL string
}

var _ domain.Provider = (*Client)(nil)

// New returns a Client. A nil client uses http.DefaultClient; an empty
// baseURL uses the real ESPN endpoint.
func New(client *http.Client, baseURL string) *Client {
	if client == nil {
		client = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{client: client, baseURL: strings.TrimRight(baseURL, "/")}
}

// Schedule lists every event the scoreboard returns for the date `day`,
// formatted as YYYYMMDD in UTC, exactly as passed.
//
// Caveat: ESPN buckets games by US Eastern date, not UTC. An event at
// 2026-10-09T00:15Z is listed under dates=20261008. Schedule does not
// compensate: callers wanting full coverage must query adjacent days.
func (c *Client) Schedule(ctx context.Context, leaguePath string, day time.Time) ([]domain.ProviderMatch, error) {
	return c.fetch(ctx, leaguePath, day)
}

// Status returns the current reading of one event. It looks on `day` first
// and then the day before and the day after (see the Schedule caveat about
// Eastern-date bucketing). If none lists the event it returns
// domain.ErrNotFound. A transport or parse error on any day is returned as is.
func (c *Client) Status(ctx context.Context, leaguePath, eventID string, day time.Time) (domain.MatchStatus, error) {
	for _, offset := range []int{0, -1, 1} {
		ms, err := c.fetch(ctx, leaguePath, day.AddDate(0, 0, offset))
		if err != nil {
			return domain.MatchStatus{}, err
		}
		for _, m := range ms {
			if m.EventID == eventID {
				return m.Status, nil
			}
		}
	}
	return domain.MatchStatus{}, fmt.Errorf("espn: event %s in %s near %s: %w",
		eventID, leaguePath, day.UTC().Format("2006-01-02"), domain.ErrNotFound)
}

type scoreboard struct {
	Leagues []struct {
		Name         string `json:"name"`
		Abbreviation string `json:"abbreviation"`
		Season       struct {
			Year        int    `json:"year"`
			DisplayName string `json:"displayName"`
		} `json:"season"`
	} `json:"leagues"`
	Events []event `json:"events"`
}

type event struct {
	ID           string `json:"id"`
	Date         string `json:"date"`
	Competitions []struct {
		Competitors []competitor `json:"competitors"`
	} `json:"competitions"`
	Status struct {
		Type struct {
			Name      string `json:"name"`
			State     string `json:"state"`
			Completed bool   `json:"completed"`
		} `json:"type"`
	} `json:"status"`
}

type competitor struct {
	HomeAway string `json:"homeAway"`
	Winner   *bool  `json:"winner"` // absent before the game in NFL
	Team     struct {
		DisplayName  string `json:"displayName"`
		Abbreviation string `json:"abbreviation"`
		Logo         string `json:"logo"`
	} `json:"team"`
}

func (c *Client) fetch(ctx context.Context, leaguePath string, day time.Time) ([]domain.ProviderMatch, error) {
	leaguePath = strings.Trim(leaguePath, "/")
	if leaguePath == "" || strings.ContainsAny(leaguePath, "?#\\") || strings.Contains(leaguePath, "..") {
		return nil, fmt.Errorf("espn: invalid league path %q", leaguePath)
	}
	u := c.baseURL + "/" + leaguePath + "/scoreboard?dates=" + url.QueryEscape(day.UTC().Format("20060102"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("espn: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("espn: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("espn: read %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("espn: GET %s: status %d: %s", u, resp.StatusCode, strings.TrimSpace(snippet))
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("espn: GET %s: response exceeds %d bytes", u, maxBodyBytes)
	}
	var sb scoreboard
	if err := json.Unmarshal(body, &sb); err != nil {
		return nil, fmt.Errorf("espn: decode %s: %w", u, err)
	}
	label := competitionLabel(sb)
	out := make([]domain.ProviderMatch, 0, len(sb.Events))
	for _, ev := range sb.Events {
		m, err := convert(ev, label)
		if err != nil {
			return nil, fmt.Errorf("espn: event %q: %w", ev.ID, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// competitionLabel is deterministic: if leagues[0].season.displayName is set
// and already contains the league's name or abbreviation (case-insensitive),
// it is used alone ("2026-27 English Premier League"). Otherwise the label is
// "<abbreviation> <season.year>" ("NFL 2026"), dropping any missing part
// (abbreviation falls back to name). Empty when the response has no league.
func competitionLabel(sb scoreboard) string {
	if len(sb.Leagues) == 0 {
		return ""
	}
	l := sb.Leagues[0]
	dn := strings.TrimSpace(l.Season.DisplayName)
	lower := strings.ToLower(dn)
	for _, n := range []string{l.Name, l.Abbreviation} {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" && strings.Contains(lower, n) {
			return dn
		}
	}
	abbr := strings.TrimSpace(l.Abbreviation)
	if abbr == "" {
		abbr = strings.TrimSpace(l.Name)
	}
	switch {
	case l.Season.Year != 0 && abbr != "":
		return fmt.Sprintf("%s %d", abbr, l.Season.Year)
	case abbr != "":
		return abbr
	case l.Season.Year != 0:
		return fmt.Sprint(l.Season.Year)
	}
	return dn
}

func convert(ev event, label string) (domain.ProviderMatch, error) {
	if ev.ID == "" {
		return domain.ProviderMatch{}, errors.New("missing id")
	}
	if len(ev.Competitions) == 0 {
		return domain.ProviderMatch{}, errors.New("no competitions")
	}
	kickoff, err := parseDate(ev.Date)
	if err != nil {
		return domain.ProviderMatch{}, err
	}
	var home, away *competitor
	comps := ev.Competitions[0].Competitors
	for i := range comps {
		switch comps[i].HomeAway {
		case "home":
			home = &comps[i]
		case "away":
			away = &comps[i]
		}
	}
	if home == nil || away == nil {
		return domain.ProviderMatch{}, errors.New("need one home and one away competitor")
	}
	st := ev.Status.Type
	status := domain.MatchStatus{
		Phase:   phase(st.Name, st.State, st.Completed),
		RawName: st.Name,
		Kickoff: kickoff,
	}
	if status.Phase == domain.PhaseFinal {
		status.Winner = winner(home, away)
	}
	return domain.ProviderMatch{
		EventID:     ev.ID,
		Competition: label,
		Home:        team(home),
		Away:        team(away),
		Status:      status,
	}, nil
}

func team(c *competitor) domain.Team {
	return domain.Team{Name: c.Team.DisplayName, Abbrev: c.Team.Abbreviation, LogoURL: c.Team.Logo}
}

// winner reads the winner flags; call only for completed games. Neither
// flagged (soccer draw) or both flagged is a draw.
func winner(home, away *competitor) domain.OutcomeKey {
	h := home.Winner != nil && *home.Winner
	a := away.Winner != nil && *away.Winner
	switch {
	case h && !a:
		return domain.OutcomeHome
	case a && !h:
		return domain.OutcomeAway
	}
	return domain.OutcomeDraw
}

var knownStatus = map[string]bool{
	"STATUS_SCHEDULED":   true,
	"STATUS_IN_PROGRESS": true,
	"STATUS_HALFTIME":    true,
	"STATUS_DELAYED":     true,
	"STATUS_FINAL":       true,
	"STATUS_FINAL_AET":   true,
	"STATUS_FINAL_PEN":   true,
	"STATUS_FULL_TIME":   true,
	"STATUS_POSTPONED":   true,
	"STATUS_CANCELED":    true,
}

// phase maps ESPN status to a domain.Phase. A status name outside the known
// list is PhaseUnknown whatever the state says. For known names, postponed
// and canceled win; otherwise state decides: pre -> Pre, in -> Live,
// post+completed -> Final. Any other combination (for example post without
// completed) is PhaseUnknown rather than a guess.
func phase(name, state string, completed bool) domain.Phase {
	if !knownStatus[name] {
		return domain.PhaseUnknown
	}
	switch name {
	case "STATUS_POSTPONED":
		return domain.PhasePostponed
	case "STATUS_CANCELED":
		return domain.PhaseCanceled
	}
	switch {
	case state == "pre":
		return domain.PhasePre
	case state == "in":
		return domain.PhaseLive
	case state == "post" && completed:
		return domain.PhaseFinal
	}
	return domain.PhaseUnknown
}

// parseDate accepts ESPN's "2006-01-02T15:04Z" (no seconds) and RFC3339.
func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04Z", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable date %q", s)
}
