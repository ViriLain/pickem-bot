# pickem-bot

A Discord bot for a friend group's pick'em game: before each match it posts a card with one button per outcome, and people pick a side. There is no money, only points and a leaderboard, with schedules and results read from ESPN.

## How the game works

- The bot posts a card in the league's channel 24 hours before kickoff, one button per outcome. Soccer leagues added with `allow_draw` also get a Draw button.
- One pick per person per match. Click again to change it, until picks lock 1 minute before kickoff. Late clicks are refused even if the buttons still look active.
- Picks are hidden until lock: the card only shows how many people picked. At lock it lists who took which side.
- A match is settled when ESPN reports it completed, never on a timer.
- 1 point for a correct pick. When at least 4 people picked and fewer than a third of them chose the winner, each of them gets 1 extra point as an underdog bonus.
- Postponed: no points, picks are kept, and the card says so. When a new kickoff is known (from ESPN or `/match move`), picks reopen, the bot mentions everyone who already picked, and the match locks 1 minute before the new kickoff.
- Canceled: the match is void and any points from it are reversed.
- Points live in an append-only ledger. Corrections are reversing entries, never edits.

## Discord setup

1. Open the [Discord Developer Portal](https://discord.com/developers/applications), create an application, and add a bot under **Bot**. Copy the bot token; this is `DISCORD_TOKEN`. No privileged gateway intents are needed.
2. Under **OAuth2 > URL Generator**, select the scopes `bot` and `applications.commands`, then the bot permissions **View Channel**, **Send Messages** and **Read Message History**. Embed Links is not needed. Open the generated URL and add the bot to your server.
3. Create a role for the people allowed to run admin commands (for example "Pick'em Admin") and a channel for admin alerts. Make sure the bot can see and post in the alert channel and in every channel you will post cards to.
4. In Discord, turn on **User Settings > Advanced > Developer Mode**. Then right-click the server icon and choose **Copy Server ID** (`GUILD_ID`), right-click the role under **Server Settings > Roles** and choose **Copy Role ID** (`ADMIN_ROLE_ID`), and right-click the alert channel and choose **Copy Channel ID** (`ADMIN_CHANNEL_ID`).

Slash commands are registered for that one server each time the bot starts, so they show up immediately. Admin commands are visible to everyone; the bot refuses them for members without the admin role.

## Configuration

All configuration is read from environment variables.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `DISCORD_TOKEN` | yes | | Bot token from the Developer Portal |
| `GUILD_ID` | yes | | Server the bot runs in |
| `ADMIN_ROLE_ID` | yes | | Role whose members may run admin commands |
| `ADMIN_CHANNEL_ID` | yes | | Channel for alerts the bot cannot resolve on its own |
| `DB_PATH` | no | `pickem.db` (`/data/pickem.db` in Docker) | SQLite database file |
| `PICKEM_TZ` | no | `America/New_York` | Time zone for the date and time typed into `/match move` |

The bot exits at startup with a message naming any required variable that is missing.

## Running

With Go installed:

```sh
export DISCORD_TOKEN=... GUILD_ID=... ADMIN_ROLE_ID=... ADMIN_CHANNEL_ID=...
go run ./cmd/pickem
```

With Docker, put the variables in a file such as `.env` (one `NAME=value` per line, already in `.gitignore`) and keep the database in a named volume:

```sh
docker build -t pickem-bot .
docker run -d --name pickem --restart unless-stopped \
  -v pickem-data:/data --env-file .env pickem-bot
```

The image runs as a non-root user (uid 65532). If you bind-mount a host directory instead of a named volume, make it writable by that user, for example `sudo chown 65532:65532 ./data`.

## First steps

As a member with the admin role:

1. Subscribe a channel to a league, using the ESPN path for that league: `/league add path:football/nfl channel:#nfl-picks`. For soccer, add `allow_draw:True`, for example `/league add path:soccer/eng.1 channel:#prem-picks name:Premier League allow_draw:True`.
2. Run `/sync` to pull the schedule now instead of waiting for the next automatic sync (every 6 hours).

Cards appear 24 hours before each kickoff.

## Commands

| Command | Who | Does |
| --- | --- | --- |
| `/picks` | Everyone | Your open picks, shown only to you |
| `/leaderboard [competition]` | Everyone | Standings, all time or for one competition |
| `/league add <path> <channel> [name] [allow_draw]` | Admin | Post a league's matches in a channel |
| `/league remove <league>` | Admin | Stop posting a league (matches and points are kept) |
| `/match settle <match> <winner>` | Admin | Settle by hand, or override a result |
| `/match void <match>` | Admin | Cancel a match and reverse its points |
| `/match reopen <match>` | Admin | Unlock picks by hand |
| `/match move <match> <when>` | Admin | New kickoff for a postponed match, as `YYYY-MM-DD HH:MM` in `PICKEM_TZ` |
| `/points adjust <user> <n> <reason>` | Admin | Add or remove points by hand |
| `/sync` | Admin | Pull schedules now |

Matches are named by the number shown on the card (`Match #12`).

## ESPN data

Schedules and results come from ESPN's public scoreboard API. It is unofficial and undocumented, so it can change or go stale without notice. When the bot sees something it cannot handle, such as an unknown status or a game that started long ago with no final result, it posts in the admin channel. Admins can always settle, void, reopen or move a match by hand with the `/match` commands.

## Development

```sh
go test ./...
```

Everything except `internal/discord`, `internal/store`'s tests and `cmd/pickem` uses only the standard library. Game rules live in `internal/game`; `docs/DESIGN.md` describes the package layout and contracts.
