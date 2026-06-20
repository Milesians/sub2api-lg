package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesLegacyReportsBeforeLevelIndex(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE reports (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  user_id TEXT,
  summary_json TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at DATETIME NOT NULL
);
INSERT INTO reports (id, session_id, user_id, summary_json, payload_json, created_at)
VALUES ('rpt_old', 'sess_old', 'user_old', '{"level":"good"}', '{"summary":{"level":"good"}}', ?);
`, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	report, err := store.GetReport(context.Background(), "rpt_old")
	if err != nil {
		t.Fatal(err)
	}
	if report == nil {
		t.Fatal("legacy report missing after migration")
	}
	if report.Level == "" {
		t.Fatal("level column was not added")
	}
}
