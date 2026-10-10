package main

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func full() map[string]string {
	return map[string]string{
		envToken:          "tok",
		envGuildID:        "111111111111111111",
		envAdminRoleID:    "222222222222222222",
		envAdminChannelID: "333333333333333333",
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(map[string]string)
		wantErr []string // substrings the error must contain; nil = no error
		check   func(t *testing.T, c config)
	}{
		{name: "defaults", edit: func(map[string]string) {}, check: func(t *testing.T, c config) {
			if c.DBPath != defaultDBPath || c.TZ != defaultTZ {
				t.Errorf("defaults not applied: %+v", c)
			}
		}},
		{name: "overrides", edit: func(m map[string]string) { m[envDBPath] = "/data/x.db"; m[envTZ] = "Europe/Madrid" },
			check: func(t *testing.T, c config) {
				if c.DBPath != "/data/x.db" || c.TZ != "Europe/Madrid" {
					t.Errorf("overrides not applied: %+v", c)
				}
			}},
		{name: "all missing", edit: func(m map[string]string) {
			for k := range m {
				delete(m, k)
			}
		}, wantErr: []string{envToken, envGuildID, envAdminRoleID, envAdminChannelID}},
		{name: "blank counts as missing", edit: func(m map[string]string) { m[envAdminRoleID] = "  " },
			wantErr: []string{"missing", envAdminRoleID}},
		{name: "non-numeric ID", edit: func(m map[string]string) { m[envAdminChannelID] = "#general" },
			wantErr: []string{envAdminChannelID, "numeric"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := full()
			tt.edit(m)
			c, err := loadConfig(env(m))
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				tt.check(t, c)
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

func TestSQLiteDSN(t *testing.T) {
	got := sqliteDSN("/data/pickem.db")
	want := "file:/data/pickem.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if got != want {
		t.Errorf("got %q", got)
	}
}
