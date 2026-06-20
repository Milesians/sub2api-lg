package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Session struct {
	ID        string
	TokenHash string
	UserID    string
	Username  string
	SrcHost   string
	SrcURL    string
	Theme     string
	Lang      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type Report struct {
	ID          string          `json:"report_id"`
	SessionID   string          `json:"session_id"`
	UserID      string          `json:"user_id,omitempty"`
	SummaryJSON json.RawMessage `json:"summary"`
	PayloadJSON json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
}

func Open(dsn string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL,
  user_id TEXT,
  username TEXT,
  src_host TEXT,
  src_url TEXT,
  theme TEXT,
  lang TEXT,
  created_at DATETIME NOT NULL,
  expires_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS endpoint_cache (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  public_path TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  fetched_at DATETIME NOT NULL,
  expires_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS reports (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  user_id TEXT,
  summary_json TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_reports_user_id ON reports(user_id);
CREATE INDEX IF NOT EXISTS idx_reports_created_at ON reports(created_at);
`)
	return err
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, token_hash, user_id, username, src_host, src_url, theme, lang, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.TokenHash, session.UserID, session.Username, session.SrcHost, session.SrcURL,
		session.Theme, session.Lang, session.CreatedAt, session.ExpiresAt,
	)
	return err
}

func (s *Store) FindSessionByToken(ctx context.Context, token string) (*Session, error) {
	hash := TokenHash(token)
	row := s.db.QueryRowContext(ctx, `
SELECT id, token_hash, user_id, username, src_host, src_url, theme, lang, created_at, expires_at
FROM sessions WHERE token_hash = ?`, hash)
	var session Session
	if err := row.Scan(&session.ID, &session.TokenHash, &session.UserID, &session.Username, &session.SrcHost, &session.SrcURL, &session.Theme, &session.Lang, &session.CreatedAt, &session.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if time.Now().After(session.ExpiresAt) {
		return nil, nil
	}
	return &session, nil
}

func (s *Store) CreateReport(ctx context.Context, report Report) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO reports (id, session_id, user_id, summary_json, payload_json, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		report.ID, report.SessionID, report.UserID, string(report.SummaryJSON), string(report.PayloadJSON), report.CreatedAt,
	)
	return err
}

func (s *Store) GetReport(ctx context.Context, id string) (*Report, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, session_id, user_id, summary_json, payload_json, created_at
FROM reports WHERE id = ?`, id)
	var report Report
	var summary string
	var payload string
	if err := row.Scan(&report.ID, &report.SessionID, &report.UserID, &summary, &payload, &report.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	report.SummaryJSON = json.RawMessage(summary)
	report.PayloadJSON = json.RawMessage(payload)
	return &report, nil
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func RandomID(prefix string, raw []byte) string {
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}
