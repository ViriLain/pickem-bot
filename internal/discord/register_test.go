package discord

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/ViriLain/pickem-bot/internal/card"
	"github.com/ViriLain/pickem-bot/internal/commands"
)

var commandName = regexp.MustCompile(`^[-_a-z0-9]{1,32}$`)

func checkOptions(t *testing.T, where string, opts []*discordgo.ApplicationCommandOption) {
	t.Helper()
	if len(opts) > 25 {
		t.Errorf("%s: %d options, max 25", where, len(opts))
	}
	seenOptional := false
	for _, o := range opts {
		name := where + " " + o.Name
		if !commandName.MatchString(o.Name) {
			t.Errorf("%s: bad option name", name)
		}
		if n := len([]rune(o.Description)); n < 1 || n > 100 {
			t.Errorf("%s: description length %d", name, n)
		}
		if o.Type == discordgo.ApplicationCommandOptionSubCommand {
			checkOptions(t, name, o.Options)
			continue
		}
		if o.Required && seenOptional {
			t.Errorf("%s: required option after an optional one", name)
		}
		if !o.Required {
			seenOptional = true
		}
	}
}

func TestBuildCommandsValid(t *testing.T) {
	cmds, err := buildCommands(commands.Specs())
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != len(commands.Specs()) {
		t.Fatalf("got %d commands", len(cmds))
	}
	for _, c := range cmds {
		if !commandName.MatchString(c.Name) {
			t.Errorf("bad command name %q", c.Name)
		}
		if n := len([]rune(c.Description)); n < 1 || n > 100 {
			t.Errorf("/%s: description length %d", c.Name, n)
		}
		if c.DefaultMemberPermissions != nil {
			t.Errorf("/%s: default member permissions set", c.Name)
		}
		checkOptions(t, "/"+c.Name, c.Options)
	}
}

func findCmd(cmds []*discordgo.ApplicationCommand, name string) *discordgo.ApplicationCommand {
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func findOpt(opts []*discordgo.ApplicationCommandOption, name string) *discordgo.ApplicationCommandOption {
	for _, o := range opts {
		if o.Name == name {
			return o
		}
	}
	return nil
}

func TestBuildCommandsShape(t *testing.T) {
	cmds, err := buildCommands(commands.Specs())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		cmd, sub, opt string
		typ           discordgo.ApplicationCommandOptionType
		required      bool
		choices       int
	}{
		{"leaderboard", "", "competition", discordgo.ApplicationCommandOptionString, false, 0},
		{"league", "add", "path", discordgo.ApplicationCommandOptionString, true, 0},
		{"league", "add", "channel", discordgo.ApplicationCommandOptionChannel, true, 0},
		{"league", "add", "allow_draw", discordgo.ApplicationCommandOptionBoolean, false, 0},
		{"match", "settle", "match", discordgo.ApplicationCommandOptionInteger, true, 0},
		{"match", "settle", "winner", discordgo.ApplicationCommandOptionString, true, 3},
		{"points", "adjust", "user", discordgo.ApplicationCommandOptionUser, true, 0},
		{"points", "adjust", "n", discordgo.ApplicationCommandOptionInteger, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.cmd+" "+tt.sub+" "+tt.opt, func(t *testing.T) {
			c := findCmd(cmds, tt.cmd)
			if c == nil {
				t.Fatalf("no /%s", tt.cmd)
			}
			opts := c.Options
			if tt.sub != "" {
				s := findOpt(opts, tt.sub)
				if s == nil || s.Type != discordgo.ApplicationCommandOptionSubCommand {
					t.Fatalf("no subcommand %s", tt.sub)
				}
				opts = s.Options
			}
			o := findOpt(opts, tt.opt)
			if o == nil {
				t.Fatalf("no option %s", tt.opt)
			}
			if o.Type != tt.typ || o.Required != tt.required || len(o.Choices) != tt.choices {
				t.Errorf("got type %v required %v choices %d", o.Type, o.Required, len(o.Choices))
			}
		})
	}
}

func TestBuildCommandsUnknownType(t *testing.T) {
	_, err := buildCommands([]commands.Spec{{Name: "x", Description: "x", Options: []commands.Option{{Name: "y", Description: "y", Type: "float"}}}})
	if err == nil {
		t.Fatal("want error for unknown option type")
	}
}

func TestComponentsJSON(t *testing.T) {
	comps := components([]card.Button{
		{ID: "pick:1:home", Label: "Spain", Emoji: "\U0001F1EA\U0001F1F8"},
		{ID: "pick:1:draw", Label: "Draw", Disabled: true},
	})
	raw, err := json.Marshal(comps)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Type       int `json:"type"`
		Components []struct {
			Type     int             `json:"type"`
			Style    int             `json:"style"`
			Label    string          `json:"label"`
			CustomID string          `json:"custom_id"`
			Disabled bool            `json:"disabled"`
			Emoji    *map[string]any `json:"emoji"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != 1 || len(rows[0].Components) != 2 {
		t.Fatalf("bad rows: %s", raw)
	}
	b0, b1 := rows[0].Components[0], rows[0].Components[1]
	if b0.Type != 2 || b0.Style != 1 || b0.CustomID != "pick:1:home" || b0.Emoji == nil || b0.Disabled {
		t.Errorf("bad first button: %s", raw)
	}
	if b1.Emoji != nil || !b1.Disabled {
		t.Errorf("bad second button: %s", raw)
	}
	if empty, _ := json.Marshal(components(nil)); string(empty) != "[]" {
		t.Errorf("no buttons should marshal as [], got %s", empty)
	}
}

func TestAllowedMentionsJSON(t *testing.T) {
	none, _ := json.Marshal(noPings())
	if !strings.Contains(string(none), `"parse":[]`) {
		t.Errorf("noPings = %s", none)
	}
	users, _ := json.Marshal(userPings())
	if !strings.Contains(string(users), `"parse":["users"]`) {
		t.Errorf("userPings = %s", users)
	}
}

func TestOptionNodes(t *testing.T) {
	opts := []*discordgo.ApplicationCommandInteractionDataOption{{
		Name: "move", Type: discordgo.ApplicationCommandOptionSubCommand,
		Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{Name: "match", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(5)},
			{Name: "when", Type: discordgo.ApplicationCommandOptionString, Value: "2026-10-10 20:00"},
		},
	}}
	path, args := flattenCommand("match", optionNodes(opts))
	if path != "match move" || args["match"] != "5" || args["when"] != "2026-10-10 20:00" {
		t.Errorf("got %q %v", path, args)
	}
}

func TestInteractionUserID(t *testing.T) {
	tests := []struct {
		name string
		i    *discordgo.Interaction
		want string
	}{
		{"member", &discordgo.Interaction{Member: &discordgo.Member{User: &discordgo.User{ID: "1"}}, User: &discordgo.User{ID: "2"}}, "1"},
		{"member without user", &discordgo.Interaction{Member: &discordgo.Member{}, User: &discordgo.User{ID: "2"}}, "2"},
		{"dm", &discordgo.Interaction{User: &discordgo.User{ID: "2"}}, "2"},
		{"nothing", &discordgo.Interaction{}, ""},
	}
	for _, tt := range tests {
		if got := interactionUserID(tt.i); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New("", "1", "2", "3"); err == nil {
		t.Error("want error for empty token")
	}
	b, err := New("Bot abc", "1", "2", "3")
	if err != nil {
		t.Fatal(err)
	}
	if b.session.Identify.Intents != discordgo.IntentsGuilds {
		t.Errorf("intents = %v", b.session.Identify.Intents)
	}
	if b.session.Identify.Token != "Bot abc" {
		t.Errorf("token = %q", b.session.Identify.Token)
	}
}
