-- Times are INTEGER unix seconds, UTC. Durations are INTEGER seconds.

CREATE TABLE leagues (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id      TEXT    NOT NULL,
    provider      TEXT    NOT NULL,
    path          TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    channel_id    TEXT    NOT NULL,
    open_lead_sec INTEGER NOT NULL DEFAULT 86400,
    allow_draw    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (guild_id, provider, path)
);

CREATE TABLE matches (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id          TEXT    NOT NULL,
    league_id         INTEGER NOT NULL,
    provider_event_id TEXT    NOT NULL,
    competition       TEXT    NOT NULL,
    home_name         TEXT    NOT NULL,
    home_abbrev       TEXT    NOT NULL,
    home_logo         TEXT    NOT NULL DEFAULT '',
    away_name         TEXT    NOT NULL,
    away_abbrev       TEXT    NOT NULL,
    away_logo         TEXT    NOT NULL DEFAULT '',
    kickoff           INTEGER NOT NULL,
    lock_at           INTEGER NOT NULL,
    state             TEXT    NOT NULL DEFAULT 'scheduled',
    message_id        TEXT    NOT NULL DEFAULT '',
    UNIQUE (league_id, provider_event_id)
);
CREATE INDEX matches_state_kickoff ON matches (state, kickoff);

CREATE TABLE outcomes (
    guild_id TEXT    NOT NULL,
    match_id INTEGER NOT NULL REFERENCES matches (id),
    key      TEXT    NOT NULL,
    label    TEXT    NOT NULL,
    emoji    TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (match_id, key)
);

CREATE TABLE picks (
    guild_id   TEXT    NOT NULL,
    match_id   INTEGER NOT NULL REFERENCES matches (id),
    user_id    TEXT    NOT NULL,
    outcome    TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (match_id, user_id)
);
CREATE INDEX picks_user ON picks (guild_id, user_id);

CREATE TABLE results (
    guild_id   TEXT    NOT NULL,
    match_id   INTEGER PRIMARY KEY REFERENCES matches (id),
    winner     TEXT    NOT NULL,
    settled_by TEXT    NOT NULL,
    settled_at INTEGER NOT NULL
);

CREATE TABLE ledger (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id    TEXT    NOT NULL,
    user_id     TEXT    NOT NULL,
    match_id    INTEGER NOT NULL DEFAULT 0,
    competition TEXT    NOT NULL DEFAULT '',
    points      INTEGER NOT NULL,
    reason      TEXT    NOT NULL,
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE INDEX ledger_board ON ledger (guild_id, competition, user_id);
CREATE INDEX ledger_match ON ledger (match_id);
