package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-origin-lg/backend/internal/config"
)

type DiagHandlers struct {
	cfg *config.Config
}

func NewDiagHandlers(cfg *config.Config) *DiagHandlers {
	return &DiagHandlers{cfg: cfg}
}

func (h *DiagHandlers) Ping(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	requestID := requestID()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Server-Timing, X-Request-Id, Content-Length")
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Server-Timing", "app;dur=1")
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
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Server-Timing, X-Request-Id, Content-Length")
	w.Header().Set("X-Request-Id", requestID())
	w.Header().Set("Content-Length", strconv.Itoa(size))

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
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Server-Timing, X-Request-Id, Content-Length")
	w.Header().Set("X-Request-Id", requestID)
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

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("X-Request-Id", requestID())

	flusher, _ := w.(http.Flusher)
	writeSSE(w, "hello", map[string]any{
		"request_id":  requestID(),
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

func writeSSE(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
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

func ContextWithTimeout(parent context.Context, ms int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, time.Duration(ms)*time.Millisecond)
}
