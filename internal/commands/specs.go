package commands

// Option types accepted in Option.Type.
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeBoolean = "boolean"
	TypeUser    = "user"
	TypeChannel = "channel"
)

// Option is one argument of a command.
type Option struct {
	Name, Description, Type string
	Required                bool
}

// Spec describes one slash command, or a group of subcommands when Sub is set.
type Spec struct {
	Name, Description string
	Admin             bool
	Options           []Option
	Sub               []Spec
}

func matchOpt() Option {
	return Option{Name: "match", Description: "Match number shown on the card (Match #id)", Type: TypeInteger, Required: true}
}

// Specs lists every command the bot registers.
func Specs() []Spec {
	return []Spec{
		{Name: "picks", Description: "Show your open picks"},
		{Name: "leaderboard", Description: "Show the standings", Options: []Option{
			{Name: "competition", Description: "Competition to show; all-time when empty", Type: TypeString},
		}},
		{Name: "league", Description: "Manage league subscriptions", Admin: true, Sub: []Spec{
			{Name: "add", Description: "Post a league's matches in a channel", Admin: true, Options: []Option{
				{Name: "path", Description: "ESPN path, e.g. soccer/eng.1 or football/nfl", Type: TypeString, Required: true},
				{Name: "channel", Description: "Channel for match cards", Type: TypeChannel, Required: true},
				{Name: "name", Description: "Display name; defaults to the path", Type: TypeString},
				{Name: "allow_draw", Description: "Offer a Draw outcome", Type: TypeBoolean},
			}},
			{Name: "remove", Description: "Stop posting a league", Admin: true, Options: []Option{
				{Name: "league", Description: "League ID", Type: TypeInteger, Required: true},
			}},
		}},
		{Name: "match", Description: "Manage matches", Admin: true, Sub: []Spec{
			{Name: "settle", Description: "Settle a match by hand or override the result", Admin: true, Options: []Option{
				matchOpt(),
				{Name: "winner", Description: "home, away or draw", Type: TypeString, Required: true},
			}},
			{Name: "void", Description: "Cancel a match and reverse its points", Admin: true, Options: []Option{matchOpt()}},
			{Name: "reopen", Description: "Unlock picks by hand", Admin: true, Options: []Option{matchOpt()}},
			{Name: "move", Description: "Set a new kickoff for a postponed match", Admin: true, Options: []Option{
				matchOpt(),
				{Name: "when", Description: "New kickoff as YYYY-MM-DD HH:MM", Type: TypeString, Required: true},
			}},
		}},
		{Name: "points", Description: "Manage points", Admin: true, Sub: []Spec{
			{Name: "adjust", Description: "Add or remove points by hand", Admin: true, Options: []Option{
				{Name: "user", Description: "Member to adjust", Type: TypeUser, Required: true},
				{Name: "n", Description: "Points to add; negative to remove", Type: TypeInteger, Required: true},
				{Name: "reason", Description: "Why", Type: TypeString, Required: true},
			}},
		}},
		{Name: "sync", Description: "Pull schedules now", Admin: true},
	}
}

// flatten maps each runnable command path ("match settle") to its leaf spec.
// A leaf inherits Admin from its group.
func flatten(specs []Spec) map[string]Spec {
	out := map[string]Spec{}
	for _, s := range specs {
		if len(s.Sub) == 0 {
			out[s.Name] = s
			continue
		}
		for _, sub := range s.Sub {
			sub.Admin = sub.Admin || s.Admin
			out[s.Name+" "+sub.Name] = sub
		}
	}
	return out
}
