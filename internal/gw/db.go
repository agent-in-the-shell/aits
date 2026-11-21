package gw

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/agent-in-the-shell/aits/internal/store"
	_ "modernc.org/sqlite" // the "sqlite" driver: pure Go, no cgo (P3)
)

// migrations are the schema, one step per user_version (P7): migrations[i]
// takes the database from version i to i+1. Steps are only ever appended.
var migrations = []string{
	// 1: events, one row per (kind, source_id); seen counts every put of it,
	// so seen-1 re-sends were deduplicated.
	`CREATE TABLE events (
		id          TEXT PRIMARY KEY,
		kind        TEXT NOT NULL,
		source_id   TEXT NOT NULL,
		payload     TEXT NOT NULL,
		put_at      TEXT NOT NULL,
		ingested_at TEXT NOT NULL,
		seen        INTEGER NOT NULL DEFAULT 1,
		UNIQUE (kind, source_id)
	) STRICT`,
}

// openDB opens dir/gw.db, creating it 0600 (P7), and migrates it to the
// current schema. A database newer than this binary is refused.
func openDB(ctx context.Context, dir string) (*sql.DB, error) {
	path := filepath.Join(dir, "gw.db")
	if err := store.Touch(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// errSchemaTooNew means the database was written by a newer aits-gw.
type errSchemaTooNew struct{ have, know int }

func (e errSchemaTooNew) Error() string {
	return fmt.Sprintf("gw.db has schema version %d; this aits-gw knows up to %d", e.have, e.know)
}

// migrate applies the steps the database lacks in one transaction, which
// takes the write lock first, so two processes never apply the same step.
func migrate(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	var have int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	if have > len(migrations) {
		return errSchemaTooNew{have, len(migrations)}
	}
	for v := have; v < len(migrations); v++ {
		if _, err := conn.ExecContext(ctx, migrations[v]); err != nil {
			return fmt.Errorf("schema step %d: %w", v+1, err)
		}
	}
	if have < len(migrations) {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// record stores e, ingested at now. A put of an id already recorded (a spool
// file read twice) changes nothing; another put of a recorded (kind,
// source_id) only counts as seen once more.
func record(ctx context.Context, db *sql.DB, e *Event, now string) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO events (id, kind, source_id, payload, put_at, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING
		ON CONFLICT (kind, source_id) DO UPDATE SET seen = seen + 1`,
		e.ID, e.Kind, e.SourceID, string(payload), e.PutAt, now)
	return err
}
