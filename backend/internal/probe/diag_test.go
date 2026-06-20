package probe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub2api-origin-lg/backend/internal/config"
)

func TestPingDoesNotExposeOriginPeerIP(t *testing.T) {
	handler := NewDiagHandlers(config.Default())
	req := httptest.NewRequest(http.MethodGet, "/diag/ping", nil)
	req.RemoteAddr = "203.0.113.10:443"
	res := httptest.NewRecorder()

	handler.Ping(res, req)

	if got := res.Header().Get("X-Origin-Peer-IP"); got != "" {
		t.Fatalf("X-Origin-Peer-IP = %q, want empty", got)
	}
	if got := res.Header().Get("Access-Control-Expose-Headers"); strings.Contains(got, "X-Origin-Peer-IP") {
		t.Fatalf("Access-Control-Expose-Headers = %q, should not expose X-Origin-Peer-IP", got)
	}
}

func TestPingTrustsProxyPeerIPFromLocalProxy(t *testing.T) {
	cfg := config.Default()
	cfg.App.TrustForwardedHeaders = true
	handler := NewDiagHandlers(cfg)
	req := httptest.NewRequest(http.MethodGet, "/diag/ping", nil)
	req.RemoteAddr = "172.21.0.1:443"
	req.Header.Set("X-Real-IP", "198.51.100.23")
	res := httptest.NewRecorder()

	handler.Ping(res, req)

	if got := handler.originPeerIP(req); got != "198.51.100.23" {
		t.Fatalf("originPeerIP = %q, want 198.51.100.23", got)
	}
	if got := res.Header().Get("X-Origin-Peer-IP"); got != "" {
		t.Fatalf("X-Origin-Peer-IP = %q, want empty", got)
	}
}

func TestPingIgnoresSpoofedProxyPeerIPFromPublicPeer(t *testing.T) {
	cfg := config.Default()
	cfg.App.TrustForwardedHeaders = true
	handler := NewDiagHandlers(cfg)
	req := httptest.NewRequest(http.MethodGet, "/diag/ping", nil)
	req.RemoteAddr = "203.0.113.10:443"
	req.Header.Set("X-Real-IP", "198.51.100.23")
	res := httptest.NewRecorder()

	handler.Ping(res, req)

	if got := handler.originPeerIP(req); got != "203.0.113.10" {
		t.Fatalf("originPeerIP = %q, want 203.0.113.10", got)
	}
	if got := res.Header().Get("X-Origin-Peer-IP"); got != "" {
		t.Fatalf("X-Origin-Peer-IP = %q, want empty", got)
	}
}

func TestHeadersDisplaysOnlyOriginPeerIP(t *testing.T) {
	cfg := config.Default()
	cfg.App.TrustForwardedHeaders = true
	handler := NewDiagHandlers(cfg)
	req := httptest.NewRequest(http.MethodGet, "/diag/headers", nil)
	req.RemoteAddr = "172.21.0.1:443"
	req.Header.Set("X-Origin-Peer-IP", "10.0.0.8")
	req.Header.Set("X-Real-IP", "10.0.0.9")
	res := httptest.NewRecorder()

	handler.Headers(res, req)

	var body struct {
		OriginPeer struct {
			IP string `json:"ip"`
		} `json:"origin_peer"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OriginPeer.IP != "10.0.0.8" {
		t.Fatalf("origin_peer.ip = %q, want X-Origin-Peer-IP", body.OriginPeer.IP)
	}
}

func TestDirectPeerIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.10:443":    "203.0.113.10",
		"[2001:db8::1]:443":   "2001:db8::1",
		"192.0.2.20":          "192.0.2.20",
		"[2001:db8::2]":       "2001:db8::2",
		" 198.51.100.8:8443 ": "198.51.100.8",
	}
	for input, want := range cases {
		if got := directPeerIP(input); got != want {
			t.Fatalf("directPeerIP(%q) = %q, want %q", input, got, want)
		}
	}
}
