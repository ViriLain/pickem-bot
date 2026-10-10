package discord

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ViriLain/pickem-bot/internal/card"
)

// This file holds the adapter logic that needs no Discord types, so it can
// be tested without the discordgo module.

// Discord limits.
const (
	maxContent    = 2000 // characters per message
	maxRowButtons = 5    // buttons per action row
	maxRows       = 5    // action rows per message
)

// Timeouts per interaction.
const (
	interactionTimeout = 15 * time.Second
	syncTimeout        = 2 * time.Minute // /sync reads every league's schedule
	editTimeout        = 10 * time.Second
)

// genericFailure is shown when something broke and there is no better reply.
const genericFailure = "Something went wrong."

// pickPrefix starts the custom ID of every pick button (see card.ButtonID).
const pickPrefix = "pick:"

// cardContent renders a card as message text: a bold title, then one line
// per entry.
func cardContent(v card.View) string {
	parts := make([]string, 0, len(v.Lines)+1)
	if v.Title != "" {
		parts = append(parts, "**"+v.Title+"**")
	}
	parts = append(parts, v.Lines...)
	return truncate(strings.Join(parts, "\n"), maxContent)
}

// truncate cuts s to at most limit characters, marking the cut with "...".
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit <= 3 {
		return string([]rune(s)[:limit])
	}
	return string([]rune(s)[:limit-3]) + "..."
}

// splitMessage breaks text into pieces of at most limit characters, cutting
// at the last newline, else the last space, before the limit so mentions
// stay whole. Empty text gives no pieces.
func splitMessage(text string, limit int) []string {
	var out []string
	r := []rune(strings.TrimSpace(text))
	for len(r) > limit {
		cut := lastIndex(r[:limit+1], '\n')
		if cut <= 0 {
			cut = lastIndex(r[:limit+1], ' ')
		}
		if cut <= 0 {
			cut = limit
		}
		if piece := strings.TrimSpace(string(r[:cut])); piece != "" {
			out = append(out, piece)
		}
		r = []rune(strings.TrimSpace(string(r[cut:])))
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

func lastIndex(r []rune, c rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// buttonRows groups buttons into action rows of at most maxRowButtons,
// keeping order. Buttons beyond what a message can hold are dropped.
func buttonRows(buttons []card.Button) [][]card.Button {
	var rows [][]card.Button
	for start := 0; start < len(buttons) && len(rows) < maxRows; start += maxRowButtons {
		end := start + maxRowButtons
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, buttons[start:end])
	}
	return rows
}

// optionNode is a Discord-free copy of an interaction option tree.
type optionNode struct {
	Name    string
	Sub     bool // a subcommand or subcommand group: its name is part of the path
	Value   any  // set when Sub is false
	Options []optionNode
}

// flattenCommand turns a command name and its option tree into the path
// commands.Dispatch expects ("match settle") and the option values as
// strings.
func flattenCommand(name string, opts []optionNode) (path string, args map[string]string) {
	parts := []string{name}
	args = map[string]string{}
	var walk func([]optionNode)
	walk = func(nodes []optionNode) {
		for _, n := range nodes {
			if n.Sub {
				parts = append(parts, n.Name)
				walk(n.Options)
				continue
			}
			args[n.Name] = formatValue(n.Value)
		}
	}
	walk(opts)
	return strings.Join(parts, " "), args
}

// formatValue renders an option value the way Dispatch parses it. Discord
// sends integers as JSON numbers, which decode to float64; user and channel
// options arrive as their ID strings.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// hasRole reports whether roles contains roleID.
func hasRole(roles []string, roleID string) bool {
	if roleID == "" {
		return false
	}
	for _, r := range roles {
		if r == roleID {
			return true
		}
	}
	return false
}

// deferEphemeral reports whether a command's reply should be private.
// Only the leaderboard is posted for everyone.
func deferEphemeral(path string) bool { return path != "leaderboard" }

// commandTimeout is how long a command may run.
func commandTimeout(path string) time.Duration {
	if path == "sync" {
		return syncTimeout
	}
	return interactionTimeout
}

// isPickButton reports whether a component custom ID is a pick button.
func isPickButton(customID string) bool { return strings.HasPrefix(customID, pickPrefix) }

// pickReply is the private reply to a pick click.
func pickReply(reply string, err error) string {
	if reply == "" {
		if err != nil {
			return genericFailure
		}
		return "Done."
	}
	return reply
}

// replyText makes command reply text safe to send.
func replyText(text string) string {
	if strings.TrimSpace(text) == "" {
		return "Done."
	}
	return truncate(text, maxContent)
}

// choice is one fixed value of a slash command option.
type choice struct{ Name, Value string }

// optionChoices returns the fixed choices for an option, if any. path is the
// command path the option belongs to.
func optionChoices(path, option string) []choice {
	if path == "match settle" && option == "winner" {
		return []choice{{"Home", "home"}, {"Away", "away"}, {"Draw", "draw"}}
	}
	return nil
}
