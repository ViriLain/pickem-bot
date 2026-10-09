# pickem-bot: build contract

A Discord bot for a friend group. Before each match it posts a card with one
button per outcome. People pick a side. No money: points and a leaderboard.

## Rules
- One pick per user per match, changeable until lock.
- Picks are hidden until lock: the card shows only how many people picked.
  At lock it lists who took which side.
- Lock is 1 minute before kickoff (`domain.LockLead`). The server rejects
  late clicks whatever the buttons show.
- Settle when the provider says the game is completed, never on a timer.
- 1 point for a correct pick. +1 underdog bonus when fewer than a third of
  pickers chose the winner and at least 4 people picked.
- Postponed: no points, picks kept, card says postponed. When a new kickoff
  is known (from the provider or `/match move`) the match reopens, the bot
  posts in the channel mentioning everyone who already picked, and it locks
  1 minute before the new kickoff.
- Canceled: void, points reversed.
- Soccer league/group matches have a Draw outcome (`League.AllowDraw`).
- The ledger is append-only. Corrections are reversing entries.

## Layout
| Package | Role | Third-party imports |
| --- | --- | --- |
| `internal/domain` | Shared types and the `Provider`, `Store`, `Clock` interfaces. FROZEN. | none |
| `internal/game` | Pure rules: `CanPick`, `Settle`, `Void`, `Postpone`, `Move`. FROZEN. | none |
| `internal/espn` | `domain.Provider` over ESPN's scoreboard API | none |
| `internal/store` | `domain.Store` over `database/sql`, SQLite dialect, applies `migrations.FS` | tests only: `modernc.org/sqlite` |
| `internal/card` | Card view model, pick handling, move announcement. No Discord types. | none |
| `internal/commands` | Slash command logic and specs. No Discord types. | none |
| `internal/discord` | Thin discordgo adapter for card and commands (later wave) | `github.com/bwmarrin/discordgo` |
| `internal/scheduler` | sync / open / lock / watch / settle loop (later wave) | none |
| `cmd/pickem` | wiring (later wave) | sqlite driver |

Discord and SQLite libraries cannot be downloaded in the build sandbox, so
all logic lives in packages that need only the standard library. Only the
adapter, the wiring and the store's tests touch third-party code.

## Contracts between packages

`internal/card` (exported names are fixed; add helpers freely):
```go
type Button struct { ID, Label, Emoji string; Disabled bool }
type View struct { Title string; Lines []string; Buttons []Button }
// Poster is implemented by the Discord adapter.
type Poster interface {
    Post(ctx context.Context, channelID string, v View) (messageID string, err error)
    Edit(ctx context.Context, channelID, messageID string, v View) error
    Announce(ctx context.Context, channelID, text string) error // text may hold <@userID> mentions
}
func New(store domain.Store, clock domain.Clock, poster Poster) *Service
func (s *Service) Render(ctx context.Context, matchID int64) (View, error)
func (s *Service) Open(ctx context.Context, matchID int64) error     // post card, save MessageID, state -> open
func (s *Service) Refresh(ctx context.Context, matchID int64) error  // re-render and edit the posted card
func (s *Service) HandlePick(ctx context.Context, userID, buttonID string) (reply string, err error) // reply is shown privately
func (s *Service) AnnounceMove(ctx context.Context, matchID int64) error // mention existing pickers with the new kickoff
func ButtonID(matchID int64, key domain.OutcomeKey) string            // "pick:<matchID>:<key>"
func ParseButtonID(id string) (matchID int64, key domain.OutcomeKey, ok bool)
func FlagEmoji(abbrev string) string // "ESP" -> flag emoji, "" when unknown
```
The card shows `Match #<id>` so admins can name a match in commands. Show
kickoff as a Discord timestamp tag `<t:UNIX:F>` so each viewer sees local time.
Labels: national teams get `FlagEmoji`; outcomes come from `Store.ListOutcomes`.

`internal/commands`:
```go
type Caller struct { GuildID, UserID string; IsAdmin bool }
type Reply struct { Text string; Ephemeral bool }
type Cards interface {  // implemented by card.Service
    Refresh(ctx context.Context, matchID int64) error
    AnnounceMove(ctx context.Context, matchID int64) error
}
type Syncer interface { SyncNow(ctx context.Context) error } // implemented by the scheduler
type Option struct { Name, Description, Type string; Required bool } // Type: "string" | "integer" | "boolean" | "user" | "channel"
type Spec struct { Name, Description string; Admin bool; Options []Option; Sub []Spec }
func New(store domain.Store, clock domain.Clock, cards Cards, syncer Syncer, loc *time.Location) *Handlers
func Specs() []Spec
// Dispatch runs one command. path is "picks", "leaderboard", "league add",
// "match settle", ... ; args maps option name to its value as a string
// (user and channel options arrive as IDs).
func (h *Handlers) Dispatch(ctx context.Context, c Caller, path string, args map[string]string) Reply
```
Matches are named by numeric ID. `/match move` parses `2006-01-02 15:04` in
`loc`. Admin commands from a non-admin get an ephemeral refusal. Handlers
hold no rules of their own: they call `game` and write through `Store`.
After settle, void, reopen or move they call `Cards.Refresh`; after move
they also call `Cards.AnnounceMove`. Errors become a short Reply, never a panic.

`internal/store`:
```go
func New(db *sql.DB) *Store            // implements domain.Store
func (s *Store) Migrate(ctx context.Context) error // applies migrations.FS in name order, once each
```
The package itself must not import a driver. Its tests open
`sql.Open("sqlite", ":memory:")` with `_ "modernc.org/sqlite"`.

`internal/espn`:
```go
func New(client *http.Client, baseURL string) *Client // implements domain.Provider; "" baseURL = the real one
```

## Rules for every work package
- Edit only the directory you own. Never edit `go.mod`, `go.sum`,
  `internal/domain`, `internal/game`, `migrations`, `docs` or another
  package. If you need a change outside your directory, stop and say so in
  your final report.
- There is no network for the Go toolchain. Do not run `go get` or
  `go mod tidy`. Import only what the Layout table allows for your package.
- Other agents are writing sibling packages in this same working tree right
  now. Build, vet and test only your own package
  (`go vet ./internal/<pkg>/ && go test ./internal/<pkg>/`).
- For anything you need from a sibling package, use the interfaces above or
  declare a small one in your own package. Do not import sibling packages
  other than `internal/domain` and `internal/game`.
- Tests: table-driven, no network, time from a fake `domain.Clock`, fakes for
  `domain.Store` and other interfaces written inside your own test files.
- Run `gofmt -l` on your directory; it must print nothing.
- Do not run any git command.
- Finish with: files written, exported API (signatures), the exact test
  command and its output, and anything you could not do or had to assume.

## ESPN scoreboard API (unofficial, no key)
`GET https://site.api.espn.com/apis/site/v2/sports/{League.Path}/scoreboard?dates=YYYYMMDD`

Always send `dates`. Undated URLs have returned stale data.

Shape (fields the bot uses):
```
events[].id                                   string
events[].date                                 "2026-10-09T00:15Z" (UTC, minutes, no seconds)
events[].season.slug / leagues[0].season.displayName   competition label
events[].competitions[0].competitors[]        two entries
    .homeAway                                 "home" | "away"
    .winner                                   bool; ABSENT before the game in NFL, false for both in soccer
    .score                                    string
    .team.displayName / .abbreviation / .logo
events[].status.type.name                     e.g. STATUS_SCHEDULED
events[].status.type.state                    "pre" | "in" | "post"
events[].status.type.completed                bool
```
Status names seen live: `STATUS_SCHEDULED`, `STATUS_IN_PROGRESS`,
`STATUS_FINAL`, `STATUS_FINAL_AET`. Expected but unseen: `STATUS_FINAL_PEN`,
`STATUS_FULL_TIME`, `STATUS_HALFTIME`, `STATUS_POSTPONED`, `STATUS_CANCELED`,
`STATUS_DELAYED`. Anything unrecognised maps to `domain.PhaseUnknown`.
Read `winner` only when `completed` is true; completed with no winner is
`domain.OutcomeDraw`.

## Commands
| Command | Who | Does |
| --- | --- | --- |
| `/picks` | Everyone | Your open picks, ephemeral |
| `/leaderboard [competition]` | Everyone | Standings |
| `/league add <espn path> <channel> [name] [allow_draw]` | Admin | Subscribe a channel |
| `/league remove <league>` | Admin | Stop posting a league |
| `/match settle <match> <winner>` | Admin | Settle by hand or override |
| `/match void <match>` | Admin | Cancel and reverse points |
| `/match reopen <match>` | Admin | Unlock picks by hand |
| `/match move <match> <date time>` | Admin | New kickoff for a postponed match |
| `/points adjust <user> <n> <reason>` | Admin | Manual ledger entry |
| `/sync` | Admin | Pull schedules now |

Admin = member has the configured admin role ID.
