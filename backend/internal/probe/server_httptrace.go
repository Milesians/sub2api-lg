package probe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"

	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/urlx"
)

type ServerProbe struct {
	cfg    *config.Config
	client *http.Client
}

type ServerResult struct {
	Enabled    bool   `json:"enabled"`
	DNSMS      int64  `json:"dns_ms,omitempty"`
	TCPMS      int64  `json:"tcp_ms,omitempty"`
	TLSMS      int64  `json:"tls_ms,omitempty"`
	TTFBMS     int64  `json:"ttfb_ms,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Status     int    `json:"status,omitempty"`
	ErrorKind  string `json:"error_kind,omitempty"`
}

func NewServerProbe(cfg *config.Config) *ServerProbe {
	return &ServerProbe{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Probe.ServerTimeoutMS) * time.Millisecond,
		},
	}
}

func (p *ServerProbe) ProbePing(ctx context.Context, lgBaseURL string) ServerResult {
	if !p.cfg.Probe.ServerProbeEnabled {
		return ServerResult{Enabled: false}
	}
	pingURL, err := urlx.Join(lgBaseURL, p.cfg.Probe.Paths.Ping)
	if err != nil {
		return ServerResult{Enabled: true, ErrorKind: "invalid_url"}
	}

	var dnsStart, connectStart, tlsStart, wroteRequest, firstByte time.Time
	var dnsMS, tcpMS, tlsMS int64
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				dnsMS = time.Since(dnsStart).Milliseconds()
			}
		},
		ConnectStart: func(_, _ string) { connectStart = time.Now() },
		ConnectDone: func(_, _ string, _ error) {
			if !connectStart.IsZero() {
				tcpMS = time.Since(connectStart).Milliseconds()
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			if !tlsStart.IsZero() {
				tlsMS = time.Since(tlsStart).Milliseconds()
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { wroteRequest = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}

	started := time.Now()
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, pingURL, nil)
	if err != nil {
		return ServerResult{Enabled: true, ErrorKind: "request_build_failed"}
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return ServerResult{Enabled: true, ErrorKind: "network_error", DurationMS: time.Since(started).Milliseconds()}
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))

	ttfb := int64(0)
	if !firstByte.IsZero() && !wroteRequest.IsZero() {
		ttfb = firstByte.Sub(wroteRequest).Milliseconds()
	}
	return ServerResult{
		Enabled:    true,
		DNSMS:      dnsMS,
		TCPMS:      tcpMS,
		TLSMS:      tlsMS,
		TTFBMS:     ttfb,
		DurationMS: time.Since(started).Milliseconds(),
		Status:     res.StatusCode,
	}
}

func AttachServerResults(ctx context.Context, p *ServerProbe, payload map[string]any) {
	items, ok := payload["entrypoints"].([]any)
	if !ok || p == nil || !p.cfg.Probe.ServerProbeEnabled {
		return
	}
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		lgBaseURL, _ := obj["lg_base_url"].(string)
		if lgBaseURL == "" {
			continue
		}
		probeCtx, cancel := ContextWithTimeout(ctx, p.cfg.Probe.ServerTimeoutMS)
		result := p.ProbePing(probeCtx, lgBaseURL)
		cancel()
		b, _ := json.Marshal(result)
		var normalized map[string]any
		_ = json.Unmarshal(b, &normalized)
		obj["server"] = normalized
	}
}
