package main

import (
	"fmt"
	"strings"
)

// Environment variable names and defaults.
const (
	envToken          = "DISCORD_TOKEN"
	envGuildID        = "GUILD_ID"
	envAdminRoleID    = "ADMIN_ROLE_ID"
	envAdminChannelID = "ADMIN_CHANNEL_ID"
	envDBPath         = "DB_PATH"
	envTZ             = "PICKEM_TZ"

	defaultDBPath = "pickem.db"
	defaultTZ     = "America/New_York"
)

// config is the process configuration, read from the environment.
type config struct {
	Token          string
	GuildID        string
	AdminRoleID    string
	AdminChannelID string
	DBPath         string
	TZ             string
}

// loadConfig reads the configuration through getenv. It reports every
// missing required variable at once, and rejects IDs that are not numeric
// Discord snowflakes.
func loadConfig(getenv func(string) string) (config, error) {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	c := config{
		Token:          get(envToken),
		GuildID:        get(envGuildID),
		AdminRoleID:    get(envAdminRoleID),
		AdminChannelID: get(envAdminChannelID),
		DBPath:         get(envDBPath),
		TZ:             get(envTZ),
	}
	if c.DBPath == "" {
		c.DBPath = defaultDBPath
	}
	if c.TZ == "" {
		c.TZ = defaultTZ
	}

	var missing []string
	for _, r := range []struct{ name, val string }{
		{envToken, c.Token},
		{envGuildID, c.GuildID},
		{envAdminRoleID, c.AdminRoleID},
		{envAdminChannelID, c.AdminChannelID},
	} {
		if r.val == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	for _, r := range []struct{ name, val string }{
		{envGuildID, c.GuildID},
		{envAdminRoleID, c.AdminRoleID},
		{envAdminChannelID, c.AdminChannelID},
	} {
		if !isSnowflake(r.val) {
			return c, fmt.Errorf("%s must be a numeric Discord ID (enable Developer Mode and use Copy ID), got %q", r.name, r.val)
		}
	}
	return c, nil
}

func isSnowflake(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sqliteDSN is the modernc.org/sqlite data source for a database file.
func sqliteDSN(path string) string {
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}
