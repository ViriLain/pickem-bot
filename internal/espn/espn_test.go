package espn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ViriLain/pickem-bot/internal/domain"
)

// server serves files[date] from testdata for each dates= value and records
// every request. Unlisted dates get an empty scoreboard.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newServer(t *testing.T, files map[string]string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Clone(context.Background()))
		s.mu.Unlock()
		f, ok := files[r.URL.Query().Get("dates")]
		if !ok {
			_, _ = w.Write([]byte(`{"leagues":[],"events":[]}`))
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) checkRequests(t *testing.T) {
	t.Helper()
	for _, r := range s.reqs {
		d := r.URL.Query().Get("dates")
		if len(d) != 8 {
			t.Errorf("request %s lacks a valid dates= parameter", r.URL)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("request %s has no User-Agent", r.URL)
		}
	}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

func TestSchedule(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		date     string
		day      time.Time
		wantComp string
		wantID   string
		home     string
		away     string
		phase    domain.Phase
		winner   domain.OutcomeKey
		raw      string
		kickoff  time.Time
	}{
		{"nfl final winner away", "nfl_final.json", "20261008", day(2026, 10, 8), "NFL 2026", "401872980", "DAL", "TB",
			domain.PhaseFinal, domain.OutcomeAway, "STATUS_FINAL", time.Date(2026, 10, 9, 0, 15, 0, 0, time.UTC)},
		{"nfl scheduled no winner field", "nfl_scheduled.json", "20261011", day(2026, 10, 11), "NFL 2026", "401872981", "GB", "DET",
			domain.PhasePre, domain.OutcomeNone, "STATUS_SCHEDULED", time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC)},
		{"mlb live", "mlb_live.json", "20261009", day(2026, 10, 9), "MLB 2026", "401800001", "LAD", "NYM",
			domain.PhaseLive, domain.OutcomeNone, "STATUS_IN_PROGRESS", time.Date(2026, 10, 9, 23, 8, 0, 0, time.UTC)},
		{"soccer scheduled winner false", "soccer_scheduled.json", "20261010", day(2026, 10, 10), "2026-27 English Premier League", "740001", "ARS", "CHE",
			domain.PhasePre, domain.OutcomeNone, "STATUS_SCHEDULED", time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)},
		{"soccer draw", "soccer_draw.json", "20261010", day(2026, 10, 10), "2026-27 English Premier League", "740002", "LIV", "EVE",
			domain.PhaseFinal, domain.OutcomeDraw, "STATUS_FULL_TIME", time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)},
		{"soccer aet home winner, RFC3339 date", "soccer_aet.json", "20260719", day(2026, 7, 19), "WC 2026", "760001", "ESP", "ARG",
			domain.PhaseFinal, domain.OutcomeHome, "STATUS_FINAL_AET", time.Date(2026, 7, 19, 19, 0, 0, 0, time.UTC)},
		{"postponed", "postponed.json", "20261010", day(2026, 10, 10), "2026-27 English Premier League", "740003", "FUL", "BRE",
			domain.PhasePostponed, domain.OutcomeNone, "STATUS_POSTPONED", time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)},
		{"unknown status", "unknown_status.json", "20261010", day(2026, 10, 10), "2026-27 English Premier League", "740004", "WOL", "BHA",
			domain.PhaseUnknown, domain.OutcomeNone, "STATUS_SUSPENDED", time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, map[string]string{tc.date: tc.file})
			c := New(srv.Client(), srv.URL)
			got, err := c.Schedule(context.Background(), "x/league", tc.day)
			if err != nil {
				t.Fatal(err)
			}
			srv.checkRequests(t)
			if len(srv.reqs) != 1 || srv.reqs[0].URL.Path != "/x/league/scoreboard" || srv.reqs[0].URL.Query().Get("dates") != tc.date {
				t.Fatalf("unexpected requests: %v", srv.reqs)
			}
			if len(got) != 1 {
				t.Fatalf("got %d matches", len(got))
			}
			m := got[0]
			if m.EventID != tc.wantID || m.Competition != tc.wantComp || m.Home.Abbrev != tc.home || m.Away.Abbrev != tc.away {
				t.Errorf("match = %+v", m)
			}
			s := m.Status
			if s.Phase != tc.phase || s.Winner != tc.winner || s.RawName != tc.raw || !s.Kickoff.Equal(tc.kickoff) {
				t.Errorf("status = %+v", s)
			}
			if m.Home.Name == "" || m.Home.LogoURL == "" {
				t.Errorf("team fields not mapped: %+v", m.Home)
			}
		})
	}
}

func TestStatusDayFallback(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		day       time.Time
		eventID   string
		wantDates []string
		wantErr   error
		wantPhase domain.Phase
	}{
		{"found on given day", map[string]string{"20261008": "nfl_final.json"}, day(2026, 10, 8), "401872980",
			[]string{"20261008"}, nil, domain.PhaseFinal},
		{"found on day before", map[string]string{"20261008": "nfl_final.json"}, day(2026, 10, 9), "401872980",
			[]string{"20261009", "20261008"}, nil, domain.PhaseFinal},
		{"found on day after", map[string]string{"20261008": "nfl_final.json"}, day(2026, 10, 7), "401872980",
			[]string{"20261007", "20261006", "20261008"}, nil, domain.PhaseFinal},
		{"not found", map[string]string{"20261008": "nfl_final.json"}, day(2026, 10, 20), "401872980",
			[]string{"20261020", "20261019", "20261021"}, domain.ErrNotFound, ""},
		{"wrong id", map[string]string{"20261008": "nfl_final.json"}, day(2026, 10, 8), "nope",
			[]string{"20261008", "20261007", "20261009"}, domain.ErrNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, tc.files)
			c := New(srv.Client(), srv.URL)
			st, err := c.Status(context.Background(), "football/nfl", tc.eventID, tc.day)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if st.Phase != tc.wantPhase {
				t.Errorf("phase = %q", st.Phase)
			}
			srv.checkRequests(t)
			var dates []string
			for _, r := range srv.reqs {
				dates = append(dates, r.URL.Query().Get("dates"))
			}
			if strings.Join(dates, ",") != strings.Join(tc.wantDates, ",") {
				t.Errorf("queried %v, want %v", dates, tc.wantDates)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantSub string
	}{
		{"non-200", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "teapot", 418) }, "status 418"},
		{"bad json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{")) }, "decode"},
		{"too large", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"events":[],"pad":"`))
			_, _ = w.Write([]byte(strings.Repeat("a", maxBodyBytes)))
			_, _ = w.Write([]byte(`"}`))
		}, "exceeds"},
		{"bad date", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"events":[{"id":"1","date":"soon","competitions":[{"competitors":[]}]}]}`))
		}, "unparseable date"},
		{"missing sides", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"events":[{"id":"1","date":"2026-10-09T00:15Z","competitions":[{"competitors":[]}]}]}`))
		}, "home and one away"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(tc.handler)
			defer s.Close()
			_, err := New(s.Client(), s.URL).Schedule(context.Background(), "a/b", day(2026, 10, 9))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

func TestContextCanceled(t *testing.T) {
	srv := newServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(srv.Client(), srv.URL).Schedule(ctx, "a/b", day(2026, 10, 9)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(srv.reqs) != 0 {
		t.Fatal("request sent on canceled context")
	}
}

func TestInvalidLeaguePath(t *testing.T) {
	c := New(nil, "http://127.0.0.1:1")
	for _, p := range []string{"", "/", "a/../b", "a?x=1"} {
		if _, err := c.Schedule(context.Background(), p, day(2026, 10, 9)); err == nil || !strings.Contains(err.Error(), "invalid league path") {
			t.Errorf("path %q: err = %v", p, err)
		}
	}
}

func TestPhase(t *testing.T) {
	tests := []struct {
		name, state string
		completed   bool
		want        domain.Phase
	}{
		{"STATUS_SCHEDULED", "pre", false, domain.PhasePre},
		{"STATUS_IN_PROGRESS", "in", false, domain.PhaseLive},
		{"STATUS_HALFTIME", "in", false, domain.PhaseLive},
		{"STATUS_DELAYED", "pre", false, domain.PhasePre},
		{"STATUS_DELAYED", "in", false, domain.PhaseLive},
		{"STATUS_FINAL", "post", true, domain.PhaseFinal},
		{"STATUS_FINAL_PEN", "post", true, domain.PhaseFinal},
		{"STATUS_FINAL", "post", false, domain.PhaseUnknown},
		{"STATUS_POSTPONED", "pre", false, domain.PhasePostponed},
		{"STATUS_CANCELED", "post", true, domain.PhaseCanceled},
		{"STATUS_SUSPENDED", "in", false, domain.PhaseUnknown},
		{"STATUS_FINAL_NEW", "post", true, domain.PhaseUnknown},
		{"", "pre", false, domain.PhaseUnknown},
	}
	for _, tc := range tests {
		if got := phase(tc.name, tc.state, tc.completed); got != tc.want {
			t.Errorf("phase(%q,%q,%v) = %q, want %q", tc.name, tc.state, tc.completed, got, tc.want)
		}
	}
}

func TestCompetitionLabel(t *testing.T) {
	mk := func(name, abbr string, year int, dn string) scoreboard {
		var sb scoreboard
		sb.Leagues = make([]struct {
			Name         string `json:"name"`
			Abbreviation string `json:"abbreviation"`
			Season       struct {
				Year        int    `json:"year"`
				DisplayName string `json:"displayName"`
			} `json:"season"`
		}, 1)
		l := &sb.Leagues[0]
		l.Name, l.Abbreviation, l.Season.Year, l.Season.DisplayName = name, abbr, year, dn
		return sb
	}
	tests := []struct {
		name string
		sb   scoreboard
		want string
	}{
		{"displayName has league name", mk("English Premier League", "EPL", 2026, "2026-27 English Premier League"), "2026-27 English Premier League"},
		{"case-insensitive abbreviation", mk("National Football League", "NFL", 2026, "nfl 2026 season"), "nfl 2026 season"},
		{"abbr and year", mk("National Football League", "NFL", 2026, "2026"), "NFL 2026"},
		{"no abbr uses name", mk("Liga X", "", 2026, "2026"), "Liga X 2026"},
		{"no year", mk("Liga X", "LX", 0, ""), "LX"},
		{"no league", scoreboard{}, ""},
	}
	for _, tc := range tests {
		if got := competitionLabel(tc.sb); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseDate(t *testing.T) {
	want := time.Date(2026, 10, 9, 0, 15, 0, 0, time.UTC)
	for _, in := range []string{"2026-10-09T00:15Z", "2026-10-09T00:15:00Z", "2026-10-08T20:15:00-04:00"} {
		got, err := parseDate(in)
		if err != nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("parseDate(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseDate("2026-10-09"); err == nil {
		t.Error("date-only should fail")
	}
}
