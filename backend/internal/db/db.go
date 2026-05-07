// Package db wraps the SQLite store. v1 uses a single file at
// ~/.frequency-bridge/data.db (overridable via FREQBRIDGE_DB_PATH).
//
// Schema lives in migrations/. Open() creates the directory and file if
// needed, applies any pending migrations, then returns the *sql.DB ready for
// the rest of the backend to use.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // register the pure-Go SQLite driver
)

// DefaultPath returns the platform default DB path:
//
//	~/.frequency-bridge/data.db
//
// honoring FREQBRIDGE_DB_PATH if set.
func DefaultPath() (string, error) {
	if p := os.Getenv("FREQBRIDGE_DB_PATH"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".frequency-bridge", "data.db"), nil
}

// Open opens (or creates) the SQLite database at path and applies migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := d.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, d); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// migrations are applied in order. Adding a migration: append to the slice
// with the next sequential Version. Each Up runs in its own transaction.
type migration struct {
	Version int
	Name    string
	Up      string
}

var migrations = []migration{
	{
		Version: 1,
		Name:    "initial",
		Up: `
CREATE TABLE devices (
    id              TEXT PRIMARY KEY,
    vendor          TEXT NOT NULL,
    model           TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    host            TEXT NOT NULL,
    serial          TEXT,
    firmware        TEXT,
    auth_secret_ref TEXT,
    enabled         INTEGER NOT NULL DEFAULT 1,
    write_enabled   INTEGER NOT NULL DEFAULT 0,
    first_seen      INTEGER NOT NULL,
    last_seen       INTEGER NOT NULL,
    metadata_json   TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_devices_vendor ON devices(vendor);

CREATE TABLE channels (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id            TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    vendor_ref           TEXT NOT NULL,
    display_name         TEXT NOT NULL,
    role_hint            TEXT,
    natural_unit         TEXT NOT NULL,
    direction            TEXT NOT NULL,
    capabilities_json    TEXT NOT NULL,
    alert_overrides_json TEXT NOT NULL DEFAULT '{}',
    sort_order           INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    UNIQUE(device_id, vendor_ref)
);
CREATE INDEX idx_channels_device ON channels(device_id);

CREATE TABLE dashboards (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    is_default  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE TABLE widgets (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    dashboard_id  INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    widget_type   TEXT NOT NULL,
    channel_id    INTEGER REFERENCES channels(id) ON DELETE CASCADE,
    grid_x        INTEGER NOT NULL,
    grid_y        INTEGER NOT NULL,
    grid_w        INTEGER NOT NULL,
    grid_h        INTEGER NOT NULL,
    settings_json TEXT NOT NULL DEFAULT '{}',
    sort_order    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_widgets_dashboard ON widgets(dashboard_id);

CREATE TABLE events (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_unix_ms              INTEGER NOT NULL,
    device_id               TEXT REFERENCES devices(id) ON DELETE SET NULL,
    channel_id              INTEGER REFERENCES channels(id) ON DELETE SET NULL,
    severity                TEXT NOT NULL,
    category                TEXT NOT NULL,
    code                    TEXT NOT NULL,
    message                 TEXT NOT NULL,
    payload_json            TEXT NOT NULL DEFAULT '{}',
    resolved_ts_unix_ms     INTEGER,
    acknowledged_ts_unix_ms INTEGER
);
CREATE INDEX idx_events_ts          ON events(ts_unix_ms DESC);
CREATE INDEX idx_events_channel_ts  ON events(channel_id, ts_unix_ms DESC);
CREATE INDEX idx_events_category_ts ON events(category, ts_unix_ms DESC);
CREATE INDEX idx_events_active      ON events(resolved_ts_unix_ms) WHERE resolved_ts_unix_ms IS NULL;

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`,
	},
}

func migrate(ctx context.Context, d *sql.DB) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
        version    INTEGER PRIMARY KEY,
        applied_at INTEGER NOT NULL
    )`); err != nil {
		return fmt.Errorf("ensure schema_version: %w", err)
	}

	var current int
	err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current)
	if err != nil {
		return fmt.Errorf("read current version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, d, m); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, d *sql.DB, m migration) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.Up); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version (version, applied_at) VALUES (?, strftime('%s','now') * 1000)`,
		m.Version,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// CurrentVersion returns the highest applied migration version.
func CurrentVersion(ctx context.Context, d *sql.DB) (int, error) {
	var v int
	err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return v, nil
}
