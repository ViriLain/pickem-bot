package discord

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ViriLain/pickem-bot/internal/card"
)

func TestCardContent(t *testing.T) {
	tests := []struct {
		name string
		v    card.View
		want string
	}{
		{"title and lines", card.View{Title: "Spain vs Italy", Lines: []string{"Group B", "3 picks in", "Match #7"}},
			"**Spain vs Italy**\nGroup B\n3 picks in\nMatch #7"},
		{"no lines", card.View{Title: "A at B"}, "**A at B**"},
		{"no title", card.View{Lines: []string{"x", "y"}}, "x\ny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cardContent(tt.v); got != tt.want {
				t.Errorf("cardContent = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCardContentTruncates(t *testing.T) {
	v := card.View{Title: "T", Lines: []string{strings.Repeat("é", 3000)}}
	got := cardContent(v)
	if n := utf8.RuneCountInString(got); n != maxContent {
		t.Fatalf("length %d, want %d", n, maxContent)
	}
	if !strings.HasSuffix(got, "...") || !utf8.ValidString(got) {
		t.Errorf("bad truncation: %q", got[len(got)-10:])
	}
}

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"empty", "  ", 10, nil},
		{"fits", "hello", 10, []string{"hello"}},
		{"exact", "0123456789", 10, []string{"0123456789"}},
		{"at space", "<@1> <@2> <@3>", 9, []string{"<@1> <@2>", "<@3>"}},
		{"prefers newline", "aa bb\ncc dd", 9, []string{"aa bb", "cc dd"}},
		{"hard cut", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitMessage(tt.text, tt.limit)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitMessage(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
			}
			for _, p := range got {
				if utf8.RuneCountInString(p) > tt.limit {
					t.Errorf("piece %q over limit", p)
				}
			}
		})
	}
}

func buttons(n int) []card.Button {
	out := make([]card.Button, n)
	for i := range out {
		out[i] = card.Button{ID: string(rune('a' + i))}
	}
	return out
}

func TestButtonRows(t *testing.T) {
	tests := []struct {
		name  string
		n     int
		sizes []int
	}{
		{"none", 0, nil},
		{"two", 2, []int{2}},
		{"three", 3, []int{3}},
		{"five", 5, []int{5}},
		{"six", 6, []int{5, 1}},
		{"eleven", 11, []int{5, 5, 1}},
		{"too many", 30, []int{5, 5, 5, 5, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := buttonRows(buttons(tt.n))
			var sizes []int
			next := 0
			for _, r := range rows {
				sizes = append(sizes, len(r))
				for _, b := range r {
					if b.ID != string(rune('a'+next)) {
						t.Errorf("order broken at %d: got %q", next, b.ID)
					}
					next++
				}
			}
			if !reflect.DeepEqual(sizes, tt.sizes) {
				t.Errorf("row sizes = %v, want %v", sizes, tt.sizes)
			}
		})
	}
}

func TestFlattenCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		opts     []optionNode
		wantPath string
		wantArgs map[string]string
	}{
		{"plain", "picks", nil, "picks", map[string]string{}},
		{"top-level option", "leaderboard", []optionNode{{Name: "competition", Value: "NFL 2026"}},
			"leaderboard", map[string]string{"competition": "NFL 2026"}},
		{"subcommand", "match", []optionNode{{Name: "settle", Sub: true, Options: []optionNode{
			{Name: "match", Value: float64(42)}, {Name: "winner", Value: "draw"},
		}}}, "match settle", map[string]string{"match": "42", "winner": "draw"}},
		{"subcommand without options", "league", []optionNode{{Name: "remove", Sub: true}}, "league remove", map[string]string{}},
		{"group", "a", []optionNode{{Name: "b", Sub: true, Options: []optionNode{{Name: "c", Sub: true, Options: []optionNode{
			{Name: "on", Value: true},
		}}}}}, "a b c", map[string]string{"on": "true"}},
		{"ids and bools", "league", []optionNode{{Name: "add", Sub: true, Options: []optionNode{
			{Name: "path", Value: "football/nfl"}, {Name: "channel", Value: "123456789012345678"}, {Name: "allow_draw", Value: false},
		}}}, "league add", map[string]string{"path": "football/nfl", "channel": "123456789012345678", "allow_draw": "false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, args := flattenCommand(tt.cmd, tt.opts)
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
			if !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("args = %v, want %v", args, tt.wantArgs)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"x", "x"},
		{true, "true"},
		{false, "false"},
		{float64(-3), "-3"},
		{float64(9007199254740991), "9007199254740991"},
		{1.5, "1.5"},
		{7, "7"},
		{int64(8), "8"},
		{json.Number("12"), "12"},
	}
	for _, tt := range tests {
		if got := formatValue(tt.in); got != tt.want {
			t.Errorf("formatValue(%#v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHasRole(t *testing.T) {
	tests := []struct {
		roles []string
		id    string
		want  bool
	}{
		{nil, "1", false},
		{[]string{"2", "1"}, "1", true},
		{[]string{"2"}, "1", false},
		{[]string{""}, "", false},
	}
	for _, tt := range tests {
		if got := hasRole(tt.roles, tt.id); got != tt.want {
			t.Errorf("hasRole(%v, %q) = %v, want %v", tt.roles, tt.id, got, tt.want)
		}
	}
}

func TestPickReply(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		reply string
		err   error
		want  string
	}{
		{"Locked in: Spain", nil, "Locked in: Spain"},
		{"Locked in: Spain", boom, "Locked in: Spain"},
		{"", boom, genericFailure},
		{"", nil, "Done."},
	}
	for _, tt := range tests {
		if got := pickReply(tt.reply, tt.err); got != tt.want {
			t.Errorf("pickReply(%q, %v) = %q, want %q", tt.reply, tt.err, got, tt.want)
		}
	}
}

func TestRouting(t *testing.T) {
	if deferEphemeral("leaderboard") {
		t.Error("leaderboard should be public")
	}
	for _, p := range []string{"picks", "league add", "match settle", "sync"} {
		if !deferEphemeral(p) {
			t.Errorf("%s should be ephemeral", p)
		}
	}
	if commandTimeout("sync") <= commandTimeout("picks") {
		t.Error("sync should get a longer timeout")
	}
	if !isPickButton(card.ButtonID(3, "home")) || isPickButton("other:1") {
		t.Error("isPickButton wrong")
	}
	if got := replyText(" "); got != "Done." {
		t.Errorf("replyText blank = %q", got)
	}
}

func TestOptionChoices(t *testing.T) {
	got := optionChoices("match settle", "winner")
	want := []choice{{"Home", "home"}, {"Away", "away"}, {"Draw", "draw"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("winner choices = %v", got)
	}
	if optionChoices("match settle", "match") != nil || optionChoices("leaderboard", "winner") != nil {
		t.Error("unexpected choices")
	}
}
