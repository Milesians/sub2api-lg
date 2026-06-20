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
	ID           string
	TokenHash    string
	UserID       string
	Username     string
	SessionType  string
	ScopesJSON   string
	ParentOrigin string
	SrcHost      string
	SrcURL       string
	TicketID     string
	Theme        string
	Lang         string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type Report struct {
	ID                 string          `json:"report_id"`
	SessionID          string          `json:"session_id"`
	UserID             string          `json:"user_id,omitempty"`
	SummaryJSON        json.RawMessage `json:"summary"`
	PayloadJSON        json.RawMessage `json:"payload"`
	CustomerReportJSON json.RawMessage `json:"customer_report"`
	InternalReportJSON json.RawMessage `json:"internal_report"`
	SupportSummaryJSON json.RawMessage `json:"support_summary"`
	ShareTokenHash     string          `json:"-"`
	ShareEnabled       bool            `json:"share_enabled"`
	Level              string          `json:"level"`
	Score              int             `json:"score"`
	ProblemCodesJSON   json.RawMessage `json:"problem_codes"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	CustomerExpiresAt  time.Time       `json:"customer_expires_at"`
	InternalExpiresAt  time.Time       `json:"internal_expires_at"`
}

type ReportFilter struct {
	Page             int
	PageSize         int
	UserID           string
	ReportID         string
	Level            string
	EndpointPublicID string
	ProblemCode      string
	From             time.Time
	To               time.Time
}

type ReportListResult struct {
	Items    []Report
	Total    int
	Page     int
	PageSize int
}

type DiagEvent struct {
	ID                   string
	RunID                string
	RequestID            string
	EndpointPublicID     string
	InternalEntryPointID string
	Kind                 string
	SafeSummaryJSON      json.RawMessage
	InternalJSON         json.RawMessage
	CreatedAt            time.Time
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
  session_type TEXT NOT NULL DEFAULT 'customer',
  scopes_json TEXT NOT NULL DEFAULT '[]',
  parent_origin TEXT NOT NULL DEFAULT '',
  src_host TEXT,
  src_url TEXT,
  ticket_id TEXT NOT NULL DEFAULT '',
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
  customer_report_json TEXT NOT NULL DEFAULT '{}',
  internal_report_json TEXT NOT NULL DEFAULT '{}',
  support_summary_json TEXT NOT NULL DEFAULT '{}',
  share_token_hash TEXT NOT NULL DEFAULT '',
  share_enabled INTEGER NOT NULL DEFAULT 1,
  level TEXT NOT NULL DEFAULT '',
  score INTEGER NOT NULL DEFAULT 0,
  problem_codes_json TEXT NOT NULL DEFAULT '[]',
  updated_at DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z',
  customer_expires_at DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z',
  internal_expires_at DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z',
  created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_reports_user_id ON reports(user_id);
CREATE INDEX IF NOT EXISTS idx_reports_created_at ON reports(created_at);

CREATE TABLE IF NOT EXISTS diag_events (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  endpoint_public_id TEXT NOT NULL,
  internal_entrypoint_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  safe_summary_json TEXT NOT NULL,
  internal_json TEXT NOT NULL,
  created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_diag_events_run ON diag_events(run_id);
CREATE INDEX IF NOT EXISTS idx_diag_events_request ON diag_events(request_id);

CREATE TABLE IF NOT EXISTS admin_audit_logs (
  id TEXT PRIMARY KEY,
  admin_user_id TEXT NOT NULL,
  action TEXT NOT NULL,
  report_id TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL DEFAULT '',
  ip_masked TEXT NOT NULL DEFAULT '',
  user_agent_hash TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL
);
`)
	if err != nil {
		return err
	}
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{"sessions", "session_type", "TEXT NOT NULL DEFAULT 'customer'"},
		{"sessions", "scopes_json", "TEXT NOT NULL DEFAULT '[]'"},
		{"sessions", "parent_origin", "TEXT NOT NULL DEFAULT ''"},
		{"sessions", "ticket_id", "TEXT NOT NULL DEFAULT ''"},
		{"reports", "customer_report_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"reports", "internal_report_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"reports", "support_summary_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"reports", "share_token_hash", "TEXT NOT NULL DEFAULT ''"},
		{"reports", "share_enabled", "INTEGER NOT NULL DEFAULT 1"},
		{"reports", "level", "TEXT NOT NULL DEFAULT ''"},
		{"reports", "score", "INTEGER NOT NULL DEFAULT 0"},
		{"reports", "problem_codes_json", "TEXT NOT NULL DEFAULT '[]'"},
		{"reports", "updated_at", "DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{"reports", "customer_expires_at", "DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{"reports", "internal_expires_at", "DATETIME NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
	} {
		if err := s.ensureColumn(ctx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_reports_level_created ON reports(level, created_at);

UPDATE reports
SET customer_report_json = CASE WHEN summary_json <> '' THEN summary_json ELSE payload_json END
WHERE customer_report_json = '{}';
UPDATE reports
SET internal_report_json = payload_json
WHERE internal_report_json = '{}';
UPDATE reports
SET support_summary_json = CASE WHEN summary_json <> '' THEN summary_json ELSE '{}' END
WHERE support_summary_json = '{}';
UPDATE reports
SET level = 'legacy'
WHERE level = '';
UPDATE reports
SET updated_at = created_at
WHERE updated_at = '1970-01-01T00:00:00Z';
UPDATE reports
SET customer_expires_at = COALESCE(datetime(created_at, '+72 hours'), created_at)
WHERE customer_expires_at = '1970-01-01T00:00:00Z';
UPDATE reports
SET internal_expires_at = COALESCE(datetime(created_at, '+72 hours'), created_at)
WHERE internal_expires_at = '1970-01-01T00:00:00Z';
`)
	return err
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	if session.SessionType == "" {
		session.SessionType = "customer"
	}
	if session.ScopesJSON == "" {
		session.ScopesJSON = "[]"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, token_hash, user_id, username, session_type, scopes_json, parent_origin, src_host, src_url, ticket_id, theme, lang, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.TokenHash, session.UserID, session.Username, session.SessionType, session.ScopesJSON, session.ParentOrigin, session.SrcHost, session.SrcURL, session.TicketID,
		session.Theme, session.Lang, session.CreatedAt, session.ExpiresAt,
	)
	return err
}

func (s *Store) FindSessionByToken(ctx context.Context, token string) (*Session, error) {
	hash := TokenHash(token)
	row := s.db.QueryRowContext(ctx, `
SELECT id, token_hash, user_id, username, session_type, scopes_json, parent_origin, src_host, src_url, ticket_id, theme, lang, created_at, expires_at
FROM sessions WHERE token_hash = ?`, hash)
	var session Session
	if err := row.Scan(&session.ID, &session.TokenHash, &session.UserID, &session.Username, &session.SessionType, &session.ScopesJSON, &session.ParentOrigin, &session.SrcHost, &session.SrcURL, &session.TicketID, &session.Theme, &session.Lang, &session.CreatedAt, &session.ExpiresAt); err != nil {
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
	normalizeReport(&report)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO reports (
  id, session_id, user_id, summary_json, payload_json,
  customer_report_json, internal_report_json, support_summary_json,
  share_token_hash, share_enabled, level, score, problem_codes_json,
  created_at, updated_at, customer_expires_at, internal_expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		report.ID, report.SessionID, report.UserID, string(report.SummaryJSON), string(report.PayloadJSON),
		string(report.CustomerReportJSON), string(report.InternalReportJSON), string(report.SupportSummaryJSON),
		report.ShareTokenHash, boolInt(report.ShareEnabled), report.Level, report.Score, string(report.ProblemCodesJSON),
		report.CreatedAt, report.UpdatedAt, report.CustomerExpiresAt, report.InternalExpiresAt,
	)
	return err
}

func (s *Store) DeleteReportsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE created_at < ?`, cutoff)
	return err
}

func (s *Store) GetReport(ctx context.Context, id string) (*Report, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, session_id, user_id, summary_json, payload_json,
       customer_report_json, internal_report_json, support_summary_json,
       share_token_hash, share_enabled, level, score, problem_codes_json,
       created_at, updated_at, customer_expires_at, internal_expires_at
FROM reports WHERE id = ?`, id)
	var report Report
	var summary string
	var payload string
	var customerReport string
	var internalReport string
	var supportSummary string
	var problemCodes string
	var shareEnabled int
	if err := row.Scan(&report.ID, &report.SessionID, &report.UserID, &summary, &payload, &customerReport, &internalReport, &supportSummary, &report.ShareTokenHash, &shareEnabled, &report.Level, &report.Score, &problemCodes, &report.CreatedAt, &report.UpdatedAt, &report.CustomerExpiresAt, &report.InternalExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	report.SummaryJSON = json.RawMessage(summary)
	report.PayloadJSON = json.RawMessage(payload)
	report.CustomerReportJSON = json.RawMessage(customerReport)
	report.InternalReportJSON = json.RawMessage(internalReport)
	report.SupportSummaryJSON = json.RawMessage(supportSummary)
	report.ProblemCodesJSON = json.RawMessage(problemCodes)
	report.ShareEnabled = shareEnabled != 0
	return &report, nil
}

func (s *Store) ListReports(ctx context.Context, filter ReportFilter) (*ReportListResult, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 || filter.PageSize > 100 {
		filter.PageSize = 20
	}
	where := []string{"1=1"}
	args := []any{}
	if filter.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, filter.UserID)
	}
	if filter.ReportID != "" {
		where = append(where, "id = ?")
		args = append(args, filter.ReportID)
	}
	if filter.Level != "" {
		where = append(where, "level = ?")
		args = append(args, filter.Level)
	}
	if filter.EndpointPublicID != "" {
		where = append(where, "internal_report_json LIKE ?")
		args = append(args, "%"+filter.EndpointPublicID+"%")
	}
	if filter.ProblemCode != "" {
		where = append(where, "problem_codes_json LIKE ?")
		args = append(args, "%"+filter.ProblemCode+"%")
	}
	if !filter.From.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, filter.From)
	}
	if !filter.To.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, filter.To)
	}
	whereSQL := joinWhere(where)

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reports WHERE "+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, user_id, summary_json, payload_json,
       customer_report_json, internal_report_json, support_summary_json,
       share_token_hash, share_enabled, level, score, problem_codes_json,
       created_at, updated_at, customer_expires_at, internal_expires_at
FROM reports
WHERE `+whereSQL+`
ORDER BY created_at DESC
LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []Report{}
	for rows.Next() {
		var report Report
		var summary, payload, customerReport, internalReport, supportSummary, problemCodes string
		var shareEnabled int
		if err := rows.Scan(&report.ID, &report.SessionID, &report.UserID, &summary, &payload, &customerReport, &internalReport, &supportSummary, &report.ShareTokenHash, &shareEnabled, &report.Level, &report.Score, &problemCodes, &report.CreatedAt, &report.UpdatedAt, &report.CustomerExpiresAt, &report.InternalExpiresAt); err != nil {
			return nil, err
		}
		report.SummaryJSON = json.RawMessage(summary)
		report.PayloadJSON = json.RawMessage(payload)
		report.CustomerReportJSON = json.RawMessage(customerReport)
		report.InternalReportJSON = json.RawMessage(internalReport)
		report.SupportSummaryJSON = json.RawMessage(supportSummary)
		report.ProblemCodesJSON = json.RawMessage(problemCodes)
		report.ShareEnabled = shareEnabled != 0
		items = append(items, report)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &ReportListResult{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize}, nil
}

func (s *Store) CreateDiagEvent(ctx context.Context, event DiagEvent) error {
	if event.ID == "" {
		event.ID = "evt_" + TokenHash(event.RequestID + event.Kind + event.CreatedAt.String())[:18]
	}
	if event.SafeSummaryJSON == nil {
		event.SafeSummaryJSON = json.RawMessage(`{}`)
	}
	if event.InternalJSON == nil {
		event.InternalJSON = json.RawMessage(`{}`)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT OR REPLACE INTO diag_events (id, run_id, request_id, endpoint_public_id, internal_entrypoint_id, kind, safe_summary_json, internal_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.RunID, event.RequestID, event.EndpointPublicID, event.InternalEntryPointID, event.Kind,
		string(event.SafeSummaryJSON), string(event.InternalJSON), event.CreatedAt,
	)
	return err
}

func (s *Store) ListDiagEvents(ctx context.Context, runID string, requestIDs []string) ([]DiagEvent, error) {
	where := []string{"1=1"}
	args := []any{}
	if runID != "" {
		where = append(where, "run_id = ?")
		args = append(args, runID)
	}
	if len(requestIDs) > 0 {
		placeholders := make([]string, 0, len(requestIDs))
		for _, id := range requestIDs {
			if id == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) > 0 {
			where = append(where, "request_id IN ("+joinComma(placeholders)+")")
		}
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, request_id, endpoint_public_id, internal_entrypoint_id, kind, safe_summary_json, internal_json, created_at
FROM diag_events
WHERE `+joinWhere(where)+`
ORDER BY created_at ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []DiagEvent{}
	for rows.Next() {
		var event DiagEvent
		var safeSummary, internal string
		if err := rows.Scan(&event.ID, &event.RunID, &event.RequestID, &event.EndpointPublicID, &event.InternalEntryPointID, &event.Kind, &safeSummary, &internal, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.SafeSummaryJSON = json.RawMessage(safeSummary)
		event.InternalJSON = json.RawMessage(internal)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ensureColumn(ctx context.Context, table, name, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+name+" "+definition)
	return err
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func RandomID(prefix string, raw []byte) string {
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}

func normalizeReport(report *Report) {
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Now()
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = report.CreatedAt
	}
	if report.CustomerExpiresAt.IsZero() {
		report.CustomerExpiresAt = report.CreatedAt.Add(72 * time.Hour)
	}
	if report.InternalExpiresAt.IsZero() {
		report.InternalExpiresAt = report.CreatedAt.Add(72 * time.Hour)
	}
	if len(report.CustomerReportJSON) == 0 {
		if len(report.SummaryJSON) > 0 {
			report.CustomerReportJSON = report.SummaryJSON
		} else if len(report.PayloadJSON) > 0 {
			report.CustomerReportJSON = report.PayloadJSON
		} else {
			report.CustomerReportJSON = json.RawMessage(`{}`)
		}
	}
	if len(report.InternalReportJSON) == 0 {
		if len(report.PayloadJSON) > 0 {
			report.InternalReportJSON = report.PayloadJSON
		} else {
			report.InternalReportJSON = report.CustomerReportJSON
		}
	}
	if len(report.SupportSummaryJSON) == 0 {
		report.SupportSummaryJSON = json.RawMessage(`{}`)
	}
	if len(report.SummaryJSON) == 0 {
		report.SummaryJSON = report.SupportSummaryJSON
	}
	if len(report.PayloadJSON) == 0 {
		report.PayloadJSON = report.InternalReportJSON
	}
	if len(report.ProblemCodesJSON) == 0 {
		report.ProblemCodesJSON = json.RawMessage(`[]`)
	}
	if report.Level == "" {
		report.Level = "bad"
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func joinWhere(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += " AND "
		}
		out += part
	}
	return out
}

func joinComma(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ","
		}
		out += part
	}
	return out
}
