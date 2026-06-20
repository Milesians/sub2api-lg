package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/entrypoints"
	"sub2api-origin-lg/backend/internal/netinfo"
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
	handler := New(cfg, db, cache).Handler()
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

func TestFrontendAssetPathsUsePublicPath(t *testing.T) {
	cfg := config.Default()
	cfg.App.PublicPath = "/lg"
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: cfg}
	html := `<script type="module" src="./assets/index.js"></script><link href="./assets/index.css">`
	got := server.rewriteFrontendAssetPaths(html)

	if strings.Contains(got, "./assets/") {
		t.Fatalf("asset path should not stay relative: %s", got)
	}
	for _, want := range []string{`src="/lg/assets/index.js"`, `href="/lg/assets/index.css"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten html missing %q: %s", want, got)
		}
	}
}

func TestPublicPathAssetsAreServedWithoutRouterPrefix(t *testing.T) {
	cfg := config.Default()
	cfg.App.PublicPath = "/lg"
	cfg.App.RouterPrefix = "/"
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	staticDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "index.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := (&Server{cfg: cfg, staticDir: staticDir}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/lg/assets/index.js", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("asset status = %d, body = %s", res.Code, res.Body.String())
	}
	if body := res.Body.String(); body != "console.log('ok')" {
		t.Fatalf("asset body = %q", body)
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

func TestBuildReportsKeepsURLsOutOfCustomerReport(t *testing.T) {
	now := time.Now()
	session := &store.Session{ID: "sess_1", UserID: "user_1"}
	snapshot := &entrypoints.Snapshot{
		Entrypoints: []entrypoints.EntryPoint{{
			ID:        "ep_1",
			Name:      "默认入口",
			RawValue:  "https://raw.example.com",
			BaseURL:   "https://api.example.com",
			LGBaseURL: "https://api.example.com/lg",
			Host:      "api.example.com",
			Source:    "admin_default",
		}},
	}
	req := customerReportRequest{
		RunID: "run_1",
		ClientEnv: map[string]string{
			"browser":  "Chrome",
			"timezone": "Asia/Shanghai",
			"language": "zh-CN",
		},
		Samples: []customerSample{{
			EndpointPublicID:      "ep_1",
			Kind:                  "origin_ping",
			RequestID:             "req_1",
			OK:                    true,
			DurationMS:            floatPtr(120),
			TTFBMS:                floatPtr(60),
			TimingDetailAvailable: true,
		}},
	}
	customerReport, internalReport, _, _ := buildReports("rpt_1", session, req, snapshot, nil, now)
	customerJSON, err := json.Marshal(customerReport)
	if err != nil {
		t.Fatal(err)
	}
	customerText := string(customerJSON)
	for _, forbidden := range []string{"base_url", "lg_base_url", "raw_value", "api.example.com"} {
		if strings.Contains(customerText, forbidden) {
			t.Fatalf("customer_report contains %q: %s", forbidden, customerText)
		}
	}
	internalJSON, err := json.Marshal(internalReport)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(internalJSON), "https://api.example.com/lg") {
		t.Fatalf("internal_report should keep lg_base_url: %s", string(internalJSON))
	}
}

func TestBuildReportsKeepsCustomEndpointURLAdminOnly(t *testing.T) {
	now := time.Now()
	session := &store.Session{ID: "sess_1", UserID: "user_1"}
	req := customerReportRequest{
		RunID:          "run_1",
		EndpointLabels: map[string]string{"custom_abc": "自定义入口"},
		EndpointNetInfo: map[string]endpointInfo{"custom_abc": {
			OriginPeer: netinfo.IPInfo{IP: "203.0.113.10", ASN: "64496", ASName: "TEST-NET"},
			DNSRecords: []netinfo.IPInfo{{IP: "203.0.113.20", ASN: "64497", ASName: "TEST-DNS"}},
		}},
		Samples: []customerSample{{
			EndpointPublicID:      "custom_abc",
			Kind:                  "origin_ping",
			RequestID:             "req_1",
			OK:                    true,
			DurationMS:            floatPtr(90),
			TTFBMS:                floatPtr(40),
			TimingDetailAvailable: true,
		}},
	}
	customEntrypoints := []entrypoints.EntryPoint{{
		ID:        "custom_abc",
		Name:      "自定义入口",
		BaseURL:   "https://custom.example.com",
		LGBaseURL: "https://custom.example.com/lg",
		Source:    "customer_custom",
	}}

	customerReport, internalReport, _, _ := buildReports("rpt_1", session, req, nil, customEntrypoints, now)
	customerJSON, err := json.Marshal(customerReport)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(customerJSON), "custom.example.com") {
		t.Fatalf("customer_report should hide custom endpoint URL: %s", string(customerJSON))
	}
	if !strings.Contains(string(customerJSON), "203.0.113.10") || !strings.Contains(string(customerJSON), "TEST-DNS") {
		t.Fatalf("customer_report should include safe netinfo: %s", string(customerJSON))
	}
	internalJSON, err := json.Marshal(internalReport)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(internalJSON), "https://custom.example.com/lg") {
		t.Fatalf("internal_report should keep custom lg_base_url: %s", string(internalJSON))
	}
}

func TestCustomerCustomEntrypointsNormalizeAndFilter(t *testing.T) {
	cfg := config.Default()
	cfg.App.PublicPath = "/lg"
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: cfg}

	items := server.customerCustomEntrypoints([]customerCustomEndpoint{
		{EndpointPublicID: "custom_ok", DisplayName: "Edge A", ProbeBaseURL: "https://custom.example.com/base"},
		{EndpointPublicID: "custom_private", DisplayName: "Local", ProbeBaseURL: "https://127.0.0.1/lg"},
		{EndpointPublicID: "not_custom", DisplayName: "Bad ID", ProbeBaseURL: "https://bad.example.com/lg"},
	})
	if len(items) != 1 {
		t.Fatalf("custom endpoints = %d, want 1: %#v", len(items), items)
	}
	if items[0].LGBaseURL != "https://custom.example.com/base/lg" {
		t.Fatalf("lg_base_url = %q", items[0].LGBaseURL)
	}
	if items[0].BaseURL != "https://custom.example.com/base" {
		t.Fatalf("base_url = %q", items[0].BaseURL)
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
	admin := adminclient.New(cfg)
	cache := entrypoints.NewCache(cfg, admin)
	_ = New(cfg, db, cache)

	report, err := db.GetReport(context.Background(), "rpt_old")
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

func TestCustomerAPIRequiresSub2APICredential(t *testing.T) {
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
	okBody := []byte(`{"user_id":"123","token":"valid-token","src_host":"sub2api.example.com","src_url":"https://sub2api.example.com/custom/network-diagnose"}`)
	res := postBootstrap(handler, okBody)
	if res.Code != http.StatusOK {
		t.Fatalf("valid bootstrap status = %d, body = %s", res.Code, res.Body.String())
	}
	var boot struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/customer/netinfo/resolve", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+boot.SessionToken)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("missing sub2api credential status = %d, want 401", res.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/customer/netinfo/resolve", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+boot.SessionToken)
	req.Header.Set("X-Sub2API-Credential", "valid-token")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("valid sub2api credential status = %d, body = %s", res.Code, res.Body.String())
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
	return New(cfg, db, cache).Handler()
}

func postBootstrap(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func floatPtr(value float64) *float64 {
	return &value
}
