package probe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub2api-origin-lg/backend/internal/config"
)

func TestPingSetsOriginPeerIP(t *testing.T) {
	handler := NewDiagHandlers(config.Default())
	req := httptest.NewRequest(http.MethodGet, "/diag/ping", nil)
	req.RemoteAddr = "203.0.113.10:443"
	res := httptest.NewRecorder()

	handler.Ping(res, req)

	if got := res.Header().Get("X-Origin-Peer-IP"); got != "203.0.113.10" {
		t.Fatalf("X-Origin-Peer-IP = %q, want 203.0.113.10", got)
	}
	if got := res.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Origin-Peer-IP") {
		t.Fatalf("Access-Control-Expose-Headers = %q, want X-Origin-Peer-IP", got)
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
