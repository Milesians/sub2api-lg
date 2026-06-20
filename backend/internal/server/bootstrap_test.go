package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/entrypoints"
	"sub2api-origin-lg/backend/internal/probe"
	"sub2api-origin-lg/backend/internal/store"
)

func TestValidateVerifiedUser(t *testing.T) {
	if err := validateVerifiedUser("123", &adminclient.User{ID: "123"}); err != nil {
		t.Fatalf("matching user should pass: %v", err)
	}
	if err := validateVerifiedUser("123", &adminclient.User{ID: "456"}); err == nil {
		t.Fatal("mismatched user id should fail")
	}
	if err := validateVerifiedUser("123", &adminclient.User{}); err == nil {
		t.Fatal("empty verified user id should fail")
	}
}

func TestSrcURLMatchesHost(t *testing.T) {
	if !srcURLMatchesHost("https://sub2api.example.com/custom/network-diagnose", "sub2api.example.com") {
		t.Fatal("matching src_url and src_host should pass")
	}
	if !srcURLMatchesHost("https://sub2api.example.com/custom/network-diagnose", "https://sub2api.example.com") {
		t.Fatal("origin src_host should pass")
	}
	if srcURLMatchesHost("https://evil.example.com/custom/network-diagnose", "sub2api.example.com") {
		t.Fatal("mismatched src_url and src_host should fail")
	}
}

func TestRootRedirectPreservesRouterPrefix(t *testing.T) {
	cfg := config.Default()
	cfg.App.RouterPrefix = "/lg"
	cfg.Storage.DSN = filepath.Join(t.TempDir(), "test.db")
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.Storage.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	admin := adminclient.New(cfg)
	cache := entrypoints.NewCache(cfg, admin)
	handler := New(cfg, db, cache, probe.NewServerProbe(cfg)).Handler()
	req := httptest.NewRequest(http.MethodGet, "/lg/?user_id=123&token=valid-token", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", res.Code)
	}
	if location := res.Header().Get("Location"); location != "/lg/embed?user_id=123&token=valid-token" {
		t.Fatalf("redirect location = %q, want /lg/embed?user_id=123&token=valid-token", location)
	}
}

func TestReportPayloadSanitization(t *testing.T) {
	payload := map[string]any{
		"session_id": "sess_123",
		"user_id":    "user_123",
		"username":   "demo",
		"token":      "secret",
		"iframe_context": map[string]any{
			"user_id":  "user_123",
			"username": "demo",
			"token":    "secret",
			"src_host": "sub2api.example.com",
		},
	}
	sanitizeReportPayload(payload)
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, forbidden := range []string{"sess_123", "user_123", "demo", "secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized report still contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "sub2api.example.com") {
		t.Fatalf("safe iframe context was removed: %s", text)
	}
}

func TestExpiredReportIsNotReturned(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.DSN = filepath.Join(t.TempDir(), "test.db")
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.Storage.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	admin := adminclient.New(cfg)
	cache := entrypoints.NewCache(cfg, admin)
	server := New(cfg, db, cache, probe.NewServerProbe(cfg))

	err = db.CreateReport(context.Background(), store.Report{
		ID:          "rpt_old",
		SessionID:   "sess_123",
		UserID:      "user_123",
		SummaryJSON: json.RawMessage(`{}`),
		PayloadJSON: json.RawMessage(`{"report_id":"rpt_old"}`),
		CreatedAt:   time.Now().Add(-reportRetention - time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := server.findReport(context.Background(), "rpt_old")
	if err != nil {
		t.Fatal(err)
	}
	if report != nil {
		t.Fatal("expired report should not be returned")
	}
}

func TestBootstrapVerifiesSub2APITokenAndUserID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"123","username":"demo"}}`))
	}))
	defer upstream.Close()

	handler := testServer(t, upstream.URL)
	okBody := []byte(`{"user_id":"123","token":"valid-token","src_host":"https://sub2api.example.com","src_url":"https://sub2api.example.com/custom/network-diagnose"}`)
	res := postBootstrap(handler, okBody)
	if res.Code != http.StatusOK {
		t.Fatalf("valid bootstrap status = %d, body = %s", res.Code, res.Body.String())
	}

	badToken := []byte(`{"user_id":"123","token":"bad-token","src_host":"sub2api.example.com","src_url":"https://sub2api.example.com/custom/network-diagnose"}`)
	res = postBootstrap(handler, badToken)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", res.Code)
	}

	mismatchedUser := []byte(`{"user_id":"456","token":"valid-token","src_host":"sub2api.example.com","src_url":"https://sub2api.example.com/custom/network-diagnose"}`)
	res = postBootstrap(handler, mismatchedUser)
	if res.Code != http.StatusForbidden {
		t.Fatalf("mismatched user status = %d, want 403", res.Code)
	}
}

func testServer(t *testing.T, adminBaseURL string) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.App.Env = "development"
	cfg.Sub2API.AdminBaseURL = adminBaseURL
	cfg.Security.AllowedSrcHosts = []string{"sub2api.example.com"}
	cfg.Storage.DSN = filepath.Join(t.TempDir(), "test.db")
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.Storage.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	admin := adminclient.New(cfg)
	cache := entrypoints.NewCache(cfg, admin)
	return New(cfg, db, cache, probe.NewServerProbe(cfg)).Handler()
}

func postBootstrap(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}
