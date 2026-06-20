package probe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-origin-lg/backend/internal/config"
)

type DiagHandlers struct {
	cfg      *config.Config
	recorder EventRecorder
}

type EventRecorder func(context.Context, DiagEvent) error

type DiagEvent struct {
	ID                   string
	RunID                string
	RequestID            string
	EndpointPublicID     string
	InternalEntryPointID string
	Kind                 string
	SafeSummaryJSON      []byte
	InternalJSON         []byte
	CreatedAt            time.Time
}

const diagExposeHeaders = "Server-Timing, X-Request-Id, Content-Length"

func NewDiagHandlers(cfg *config.Config) *DiagHandlers {
	return &DiagHandlers{cfg: cfg}
}

func (h *DiagHandlers) SetEventRecorder(recorder EventRecorder) {
	h.recorder = recorder
}

func (h *DiagHandlers) Meta(w http.ResponseWriter, r *http.Request) {
	requestID := requestID()
	h.jsonHeaders(w, requestID)
	h.record(r, requestID, "meta", map[string]any{
		"ok":          true,
		"request_id":  requestID,
		"public_path": h.cfg.App.PublicPath,
	}, map[string]any{
		"router_prefix": h.cfg.App.RouterPrefix,
		"host":          r.Host,
		"proto":         r.Proto,
	})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":            true,
		"service":       "sub2api-origin-lg",
		"server_time":   time.Now().Format(time.RFC3339),
		"request_id":    requestID,
		"public_path":   h.cfg.App.PublicPath,
		"router_prefix": h.cfg.App.RouterPrefix,
	})
}

func (h *DiagHandlers) Ping(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	requestID := requestID()
	h.jsonHeaders(w, requestID)
	w.Header().Set("Server-Timing", "app;dur=1")
	h.record(r, requestID, "ping", map[string]any{
		"ok":         true,
		"request_id": requestID,
	}, map[string]any{
		"origin_peer_ip_masked": maskIP(h.originPeerIP(r)),
		"forwarded_hop_count":   hopCount(r.Header.Get("X-Forwarded-For")),
		"x_real_ip_present":     r.Header.Get("X-Real-IP") != "",
		"host":                  r.Host,
		"proto":                 r.Proto,
	})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"service":     "sub2api-origin-lg",
		"server_time": started.Format(time.RFC3339),
		"request_id":  requestID,
		"public_path": h.cfg.App.PublicPath,
	})
}

func (h *DiagHandlers) Blob(w http.ResponseWriter, r *http.Request) {
	sizeName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("size")))
	if sizeName == "" {
		sizeName = "64k"
	}
	size, ok := allowedBlobSize(sizeName, h.cfg.Probe.MaxBlobSize, h.cfg.Probe.Allow5MBlob)
	if !ok {
		http.Error(w, "unsupported blob size", http.StatusBadRequest)
		return
	}
	requestID := requestID()
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", diagExposeHeaders)
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Content-Length", strconv.Itoa(size))
	h.record(r, requestID, "blob", map[string]any{
		"ok":         true,
		"request_id": requestID,
		"size":       sizeName,
	}, map[string]any{
		"origin_peer_ip_masked": maskIP(h.originPeerIP(r)),
		"bytes":                 size,
		"host":                  r.Host,
		"proto":                 r.Proto,
	})

	chunk := []byte("sub2api-origin-lg-diagnostic-payload\n")
	written := 0
	for written < size {
		remaining := size - written
		if remaining < len(chunk) {
			_, _ = w.Write(chunk[:remaining])
			break
		}
		_, _ = w.Write(chunk)
		written += len(chunk)
	}
}

func (h *DiagHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	sizeName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("size")))
	if sizeName == "" {
		sizeName = "64k"
	}
	size, ok := allowedBlobSize(sizeName, h.cfg.Probe.MaxBlobSize, h.cfg.Probe.Allow5MBlob)
	if !ok {
		http.Error(w, "unsupported upload size", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(size)+1)
	received, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		http.Error(w, "read upload failed", http.StatusBadRequest)
		return
	}
	requestID := requestID()
	h.jsonHeaders(w, requestID)
	h.record(r, requestID, "upload", map[string]any{
		"ok":         true,
		"request_id": requestID,
		"size":       sizeName,
	}, map[string]any{
		"origin_peer_ip_masked": maskIP(h.originPeerIP(r)),
		"bytes":                 received,
		"expected":              size,
		"host":                  r.Host,
		"proto":                 r.Proto,
	})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"request_id":  requestID,
		"size":        sizeName,
		"bytes":       received,
		"expected":    size,
		"size_match":  received == int64(size),
		"server_time": time.Now().Format(time.RFC3339),
	})
}

func (h *DiagHandlers) Stream(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	events := boundedInt(query.Get("events"), 20, 1, 100)
	intervalMS := boundedInt(query.Get("interval_ms"), 200, 10, 5000)
	bytes := boundedInt(query.Get("bytes"), 32, 0, 2048)

	requestID := requestID()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", diagExposeHeaders)
	w.Header().Set("X-Request-Id", requestID)
	h.record(r, requestID, "stream", map[string]any{
		"ok":         true,
		"request_id": requestID,
	}, map[string]any{
		"origin_peer_ip_masked": maskIP(h.originPeerIP(r)),
		"events":                events,
		"interval_ms":           intervalMS,
		"bytes":                 bytes,
		"host":                  r.Host,
		"proto":                 r.Proto,
	})

	flusher, _ := w.(http.Flusher)
	writeSSE(w, "hello", map[string]any{
		"request_id":  requestID,
		"server_time": time.Now().Format(time.RFC3339),
	})
	if flusher != nil {
		flusher.Flush()
	}

	ticker := time.NewTicker(time.Duration(intervalMS) * time.Millisecond)
	defer ticker.Stop()
	for i := 1; i <= events; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			writeSSE(w, "tick", map[string]any{
				"seq":         i,
				"server_time": time.Now().Format(time.RFC3339),
				"padding":     strings.Repeat("x", bytes),
			})
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	writeSSE(w, "done", map[string]any{"ok": true, "events": events})
	if flusher != nil {
		flusher.Flush()
	}
}

func (h *DiagHandlers) Headers(w http.ResponseWriter, r *http.Request) {
	requestID := requestID()
	h.jsonHeaders(w, requestID)
	forwardedPresent := r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != ""
	h.record(r, requestID, "headers", map[string]any{
		"ok":                        true,
		"request_id":                requestID,
		"proxy_observed":            forwardedPresent,
		"forwarded_headers_present": forwardedPresent,
		"timing_allow_origin":       true,
	}, map[string]any{
		"host":                  r.Host,
		"proto":                 r.Proto,
		"forwarded_hop_count":   hopCount(r.Header.Get("X-Forwarded-For")),
		"x_real_ip_present":     r.Header.Get("X-Real-IP") != "",
		"cf_ray_hash":           hashHeader(r.Header.Get("CF-Ray")),
		"via_present":           r.Header.Get("Via") != "",
		"remote_addr_masked":    maskIP(h.originPeerIP(r)),
		"user_agent_hash":       hashHeader(r.UserAgent()),
		"authorization_present": r.Header.Get("Authorization") != "",
		"cookie_present":        r.Header.Get("Cookie") != "",
	})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                        true,
		"request_id":                requestID,
		"proxy_observed":            forwardedPresent,
		"forwarded_headers_present": forwardedPresent,
		"timing_allow_origin":       true,
	})
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func (h *DiagHandlers) jsonHeaders(w http.ResponseWriter, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", diagExposeHeaders)
	w.Header().Set("X-Request-Id", requestID)
}

func (h *DiagHandlers) record(r *http.Request, requestID, kind string, safeSummary, internal map[string]any) {
	if h.recorder == nil || requestID == "" {
		return
	}
	query := r.URL.Query()
	runID := strings.TrimSpace(query.Get("run_id"))
	endpointPublicID := strings.TrimSpace(query.Get("endpoint_public_id"))
	internalID := strings.TrimSpace(query.Get("internal_entrypoint_id"))
	safeJSON, _ := json.Marshal(safeSummary)
	internalJSON, _ := json.Marshal(internal)
	event := DiagEvent{
		ID:                   "evt_" + requestID + "_" + kind,
		RunID:                runID,
		RequestID:            requestID,
		EndpointPublicID:     endpointPublicID,
		InternalEntryPointID: internalID,
		Kind:                 kind,
		SafeSummaryJSON:      safeJSON,
		InternalJSON:         internalJSON,
		CreatedAt:            time.Now(),
	}
	_ = h.recorder(r.Context(), event)
}

func allowedBlobSize(size, maxSize string, allow5M bool) (int, bool) {
	value, ok := parseByteSize(size)
	if !ok || value <= 0 {
		return 0, false
	}
	max, ok := parseByteSize(maxSize)
	if !ok || max <= 0 {
		max = 20 * 1024 * 1024
	}
	if allow5M && max < 5*1024*1024 {
		max = 5 * 1024 * 1024
	}
	if value > max {
		return 0, false
	}
	return value, true
}

func parseByteSize(raw string) (int, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	matchUnit := ""
	if strings.HasSuffix(raw, "kb") {
		raw = strings.TrimSuffix(raw, "kb")
		matchUnit = "k"
	} else if strings.HasSuffix(raw, "mb") {
		raw = strings.TrimSuffix(raw, "mb")
		matchUnit = "m"
	} else if strings.HasSuffix(raw, "k") || strings.HasSuffix(raw, "m") {
		matchUnit = raw[len(raw)-1:]
		raw = raw[:len(raw)-1]
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	switch matchUnit {
	case "m":
		value *= 1024 * 1024
	case "k":
		value *= 1024
	}
	return value, true
}

func boundedInt(raw string, fallback, min, max int) int {
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}

func requestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "req_" + hex.EncodeToString(b[:])
}

func directPeerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(strings.TrimSpace(remoteAddr), "[]")
}

func (h *DiagHandlers) originPeerIP(r *http.Request) string {
	direct := directPeerIP(r.RemoteAddr)
	if !h.cfg.App.TrustForwardedHeaders || !isLocalProxyPeer(direct) {
		return direct
	}
	for _, header := range []string{"X-Origin-Peer-IP", "X-Real-IP"} {
		if ip := headerIP(r.Header.Get(header)); ip != "" {
			return ip
		}
	}
	return direct
}

func headerIP(value string) string {
	if value = strings.TrimSpace(value); value == "" {
		return ""
	}
	item := strings.TrimSpace(strings.Split(value, ",")[0])
	if ip := net.ParseIP(strings.Trim(item, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

func isLocalProxyPeer(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func maskIP(value string) string {
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

func hopCount(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	count := 0
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) != "" {
			count++
		}
	}
	return count
}

func hashHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func ContextWithTimeout(parent context.Context, ms int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, time.Duration(ms)*time.Millisecond)
}
