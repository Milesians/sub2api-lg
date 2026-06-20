package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/entrypoints"
	"sub2api-origin-lg/backend/internal/probe"
	"sub2api-origin-lg/backend/internal/store"
)

type Server struct {
	cfg         *config.Config
	store       *store.Store
	cache       *entrypoints.Cache
	admin       *adminclient.Client
	serverProbe *probe.ServerProbe
	diag        *probe.DiagHandlers
	staticDir   string
}

const (
	reportRetention       = 72 * time.Hour
	reportCleanupInterval = time.Hour
)

func New(cfg *config.Config, db *store.Store, cache *entrypoints.Cache, serverProbe *probe.ServerProbe) *Server {
	server := &Server{
		cfg:         cfg,
		store:       db,
		cache:       cache,
		admin:       adminclient.New(cfg),
		serverProbe: serverProbe,
		diag:        probe.NewDiagHandlers(cfg),
		staticDir:   "frontend/dist",
	}
	server.cleanupExpiredReports(context.Background())
	server.startReportCleanup()
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	handler := http.Handler(mux)
	handler = s.withPrefix(handler)
	handler = s.withCORS(handler)
	return handler
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/embed":
		s.serveIndex(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/report/"):
		s.serveReport(w, r)
	case r.Method == http.MethodGet && (path == "/" || path == "/index.html"):
		http.Redirect(w, r, s.redirectPath(r, "/embed"), http.StatusFound)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/assets/"):
		s.serveAsset(w, r)
	case r.Method == http.MethodGet && s.publicPathHasPrefix(path, "/assets/"):
		s.servePublicPathAsset(w, r)
	case r.Method == http.MethodPost && path == "/api/bootstrap":
		s.bootstrap(w, r)
	case r.Method == http.MethodGet && path == "/api/entrypoints":
		s.requireSession(s.entrypoints)(w, r)
	case r.Method == http.MethodGet && path == "/api/route":
		s.requireSession(s.routeDiagnostics)(w, r)
	case r.Method == http.MethodPost && path == "/api/reports":
		s.requireSession(s.createReport)(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/reports/"):
		s.getReport(w, r)
	case r.Method == http.MethodGet && path == s.cfg.Probe.Paths.Ping:
		s.diag.Ping(w, r)
	case r.Method == http.MethodGet && path == s.cfg.Probe.Paths.Blob:
		s.diag.Blob(w, r)
	case r.Method == http.MethodPost && path == s.cfg.Probe.Paths.Upload:
		s.diag.Upload(w, r)
	case r.Method == http.MethodGet && path == s.cfg.Probe.Paths.Stream:
		s.diag.Stream(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) withPrefix(next http.Handler) http.Handler {
	if s.cfg.App.RouterPrefix == "/" {
		return next
	}
	prefix := strings.TrimRight(s.cfg.App.RouterPrefix, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		if clone.URL.Path == "" {
			clone.URL.Path = "/"
		}
		next.ServeHTTP(w, clone)
	})
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := stripRouterPrefix(r.URL.Path, s.cfg.App.RouterPrefix)
		if strings.HasPrefix(path, "/diag/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With")
			w.Header().Set("Access-Control-Expose-Headers", "Server-Timing, X-Request-Id, Content-Length")
			w.Header().Set("Timing-Allow-Origin", "*")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if strings.HasPrefix(path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if !s.originAllowed(r, origin) {
					http.Error(w, "origin not allowed", http.StatusForbidden)
					return
				}
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(r *http.Request, origin string) bool {
	if strings.EqualFold(strings.TrimRight(origin, "/"), requestOrigin(r, s.cfg)) {
		return true
	}
	for _, allowed := range s.cfg.Security.AllowedParentOrigins {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  string `json:"user_id"`
		Token   string `json:"token"`
		Theme   string `json:"theme"`
		Lang    string `json:"lang"`
		UIMode  string `json:"ui_mode"`
		SrcHost string `json:"src_host"`
		SrcURL  string `json:"src_url"`
	}
	if err := decodeJSON(r, &req, 1<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.Token = strings.TrimSpace(req.Token)
	req.SrcHost = normalizeSrcHost(req.SrcHost)
	req.SrcURL = strings.TrimSpace(req.SrcURL)
	if req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	if req.Token == "" {
		http.Error(w, "token is required", http.StatusUnauthorized)
		return
	}
	if !s.srcHostAllowed(req.SrcHost) {
		http.Error(w, "src_host not allowed", http.StatusForbidden)
		return
	}
	if !srcURLMatchesHost(req.SrcURL, req.SrcHost) {
		http.Error(w, "src_url does not match src_host", http.StatusForbidden)
		return
	}

	if strings.TrimSpace(s.cfg.Sub2API.AdminBaseURL) == "" {
		http.Error(w, "sub2api user verification is not configured", http.StatusServiceUnavailable)
		return
	}
	user, err := s.admin.VerifyUser(r.Context(), req.Token)
	if err != nil {
		http.Error(w, "token verification failed", http.StatusUnauthorized)
		return
	}
	if err := validateVerifiedUser(req.UserID, user); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	sessionID := "sess_" + randomToken(18)
	sessionToken := "diag_" + randomToken(32)
	now := time.Now()
	session := store.Session{
		ID:        sessionID,
		TokenHash: store.TokenHash(sessionToken),
		UserID:    user.ID,
		Username:  user.Username,
		SrcHost:   req.SrcHost,
		SrcURL:    req.SrcURL,
		Theme:     req.Theme,
		Lang:      req.Lang,
		CreatedAt: now,
		ExpiresAt: now.Add(s.cfg.Security.SessionTTL),
	}
	if err := s.store.CreateSession(r.Context(), session); err != nil {
		http.Error(w, "create session failed", http.StatusInternalServerError)
		return
	}
	snapshot, _ := s.cache.Get(r.Context(), false)
	writeJSON(w, map[string]any{
		"session_id":    sessionID,
		"session_token": sessionToken,
		"user":          user,
		"app": map[string]any{
			"public_path":   s.cfg.App.PublicPath,
			"iframe_origin": requestOrigin(r, s.cfg),
			"theme":         req.Theme,
			"lang":          req.Lang,
		},
		"probe":             s.cfg.Probe,
		"entrypoint_count":  snapshotCount(snapshot),
		"entrypoints":       snapshotEntrypoints(snapshot),
		"entrypoint_source": snapshotSource(snapshot),
	})
}

func (s *Server) entrypoints(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("refresh") == "true"
	snapshot, err := s.cache.Get(r.Context(), refresh)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, snapshot)
}

func (s *Server) routeDiagnostics(w http.ResponseWriter, r *http.Request) {
	endpointID := strings.TrimSpace(r.URL.Query().Get("endpoint_id"))
	if endpointID == "" {
		http.Error(w, "endpoint_id is required", http.StatusBadRequest)
		return
	}
	snapshot, err := s.cache.Get(r.Context(), false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	endpoint := findEndpointByID(snapshot, endpointID)
	if endpoint == nil {
		http.NotFound(w, r)
		return
	}

	timeoutMS := s.cfg.Probe.ServerTimeoutMS
	if timeoutMS < 8000 {
		timeoutMS = 8000
	}
	if timeoutMS > 20000 {
		timeoutMS = 20000
	}
	ctx, cancel := probe.ContextWithTimeout(r.Context(), timeoutMS)
	defer cancel()
	info := probe.RouteDiagnostics(ctx, endpoint.Host)

	if s.serverProbe != nil {
		probeCtx, probeCancel := probe.ContextWithTimeout(r.Context(), s.cfg.Probe.ServerTimeoutMS)
		result := s.serverProbe.ProbePing(probeCtx, endpoint.LGBaseURL)
		probeCancel()
		if result.Enabled {
			info.Server = &result
		}
	}
	writeJSON(w, info)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	var payload map[string]any
	if err := decodeJSON(r, &payload, 8<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	probe.AttachServerResults(r.Context(), s.serverProbe, payload)

	reportID := "rpt_" + time.Now().Format("20060102_") + randomToken(12)
	now := time.Now()
	s.cleanupExpiredReports(r.Context())
	payload["report_id"] = reportID
	payload["created_at"] = now.Format(time.RFC3339)
	sanitizeReportPayload(payload)

	summary := rawObject(payload["summary"])
	payloadJSON, _ := json.Marshal(payload)
	if len(summary) == 0 {
		summary = json.RawMessage(`{}`)
	}
	report := store.Report{
		ID:          reportID,
		SessionID:   session.ID,
		UserID:      session.UserID,
		SummaryJSON: summary,
		PayloadJSON: payloadJSON,
		CreatedAt:   now,
	}
	if err := s.store.CreateReport(r.Context(), report); err != nil {
		http.Error(w, "create report failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"report_id": reportID,
		"share_url": publicMountURL(r, s.cfg) + "/report/" + url.PathEscape(reportID),
	})
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/reports/")
	report, err := s.findReport(r.Context(), id)
	if err != nil {
		http.Error(w, "get report failed", http.StatusInternalServerError)
		return
	}
	if report == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, report.PayloadJSON)
}

func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		session, err := s.store.FindSessionByToken(r.Context(), token)
		if err != nil {
			http.Error(w, "session lookup failed", http.StatusInternalServerError)
			return
		}
		if session == nil {
			http.Error(w, "invalid or expired session", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, session)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	html, err := s.frontendIndexHTML()
	if err != nil {
		http.Error(w, "frontend is not built", http.StatusInternalServerError)
		return
	}
	s.writeFrontendHTML(w, html)
}

func (s *Server) serveReport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/report/")
	id = strings.SplitN(id, "/", 2)[0]
	report, err := s.findReport(r.Context(), id)
	if err != nil {
		http.Error(w, "get report failed", http.StatusInternalServerError)
		return
	}
	if report == nil {
		http.NotFound(w, r)
		return
	}
	indexPath := filepath.Join(s.staticDir, "index.html")
	html, err := os.ReadFile(indexPath)
	if err != nil {
		http.Error(w, "frontend is not built", http.StatusInternalServerError)
		return
	}
	payload := strings.ReplaceAll(string(report.PayloadJSON), "</script", "<\\/script")
	injected := strings.Replace(s.rewriteFrontendAssetPaths(string(html)), "</head>", "<script>window.__SUB2API_LG_REPORT__="+payload+"</script></head>", 1)
	s.writeFrontendHTML(w, []byte(injected))
}

func (s *Server) frontendIndexHTML() ([]byte, error) {
	indexPath := filepath.Join(s.staticDir, "index.html")
	html, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	return []byte(s.rewriteFrontendAssetPaths(string(html))), nil
}

func (s *Server) writeFrontendHTML(w http.ResponseWriter, html []byte) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+s.frameAncestors())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(html)
}

func (s *Server) rewriteFrontendAssetPaths(html string) string {
	prefix := strings.TrimRight(s.cfg.App.PublicPath, "/")
	if prefix == "" {
		prefix = "/"
	}
	assetPath := strings.TrimRight(prefix, "/") + "/assets/"
	if prefix == "/" {
		assetPath = "/assets/"
	}
	html = strings.ReplaceAll(html, `src="./assets/`, `src="`+assetPath)
	return strings.ReplaceAll(html, `href="./assets/`, `href="`+assetPath)
}

func (s *Server) findReport(ctx context.Context, id string) (*store.Report, error) {
	report, err := s.store.GetReport(ctx, id)
	if err != nil || report == nil {
		return report, err
	}
	if time.Since(report.CreatedAt) > reportRetention {
		s.cleanupExpiredReports(ctx)
		return nil, nil
	}
	return report, nil
}

func (s *Server) startReportCleanup() {
	go func() {
		ticker := time.NewTicker(reportCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanupExpiredReports(context.Background())
		}
	}()
}

func (s *Server) cleanupExpiredReports(ctx context.Context) {
	_ = s.store.DeleteReportsBefore(ctx, time.Now().Add(-reportRetention))
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if strings.HasPrefix(clean, "..") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.staticDir, clean)
	if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if ext := filepath.Ext(full); ext != "" {
		w.Header().Set("Content-Type", mime.TypeByExtension(ext))
	}
	http.ServeFile(w, r, full)
}

func (s *Server) servePublicPathAsset(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimRight(s.cfg.App.PublicPath, "/")
	clone := r.Clone(r.Context())
	clone.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	s.serveAsset(w, clone)
}

func (s *Server) publicPathHasPrefix(pathValue string, suffix string) bool {
	prefix := strings.TrimRight(s.cfg.App.PublicPath, "/")
	if prefix == "" || prefix == "/" {
		return false
	}
	return strings.HasPrefix(pathValue, prefix+suffix)
}

func (s *Server) frameAncestors() string {
	if len(s.cfg.Security.AllowedParentOrigins) == 0 {
		return "'self'"
	}
	return strings.Join(s.cfg.Security.AllowedParentOrigins, " ")
}

func (s *Server) srcHostAllowed(host string) bool {
	if len(s.cfg.Security.AllowedSrcHosts) == 0 {
		return true
	}
	host = normalizeSrcHost(host)
	for _, allowed := range s.cfg.Security.AllowedSrcHosts {
		if strings.EqualFold(host, normalizeSrcHost(allowed)) {
			return true
		}
	}
	return false
}

func srcURLMatchesHost(srcURL, srcHost string) bool {
	if srcURL == "" || srcHost == "" {
		return true
	}
	parsed, err := url.Parse(srcURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), normalizeSrcHost(srcHost))
}

func normalizeSrcHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err == nil && parsed.Hostname() != "" {
			return parsed.Hostname()
		}
	}
	parsed, err := url.Parse("https://" + value)
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return value
}

func validateVerifiedUser(expectedUserID string, user *adminclient.User) error {
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return errors.New("token verification returned no user id")
	}
	if strings.TrimSpace(user.ID) != strings.TrimSpace(expectedUserID) {
		return errors.New("user_id does not match token")
	}
	return nil
}

func decodeJSON(r *http.Request, dst any, limit int64) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, limit))
	decoder.UseNumber()
	return decoder.Decode(dst)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	switch typed := value.(type) {
	case json.RawMessage:
		_, _ = w.Write(typed)
	case []byte:
		_, _ = w.Write(typed)
	default:
		_ = json.NewEncoder(w).Encode(value)
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().String()))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func bearerToken(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func requestOrigin(r *http.Request, cfg *config.Config) string {
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	if cfg.App.TrustForwardedHeaders {
		if forwardedProto := r.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
			proto = strings.Split(forwardedProto, ",")[0]
		}
	}
	return proto + "://" + r.Host
}

func publicMountURL(r *http.Request, cfg *config.Config) string {
	if cfg.App.PublicURL != "" {
		return strings.TrimRight(cfg.App.PublicURL, "/")
	}
	base := requestOrigin(r, cfg)
	if cfg.App.TrustForwardedHeaders {
		if prefix := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/"); prefix != "" {
			return base + prefix
		}
	}
	return base + cfg.App.PublicPath
}

func stripRouterPrefix(path, prefix string) string {
	if prefix == "/" {
		return path
	}
	trimmed := strings.TrimRight(prefix, "/")
	out := strings.TrimPrefix(path, trimmed)
	if out == "" {
		return "/"
	}
	return out
}

func (s *Server) mountedPath(path string) string {
	if s.cfg.App.RouterPrefix == "/" {
		return path
	}
	return strings.TrimRight(s.cfg.App.RouterPrefix, "/") + path
}

func (s *Server) redirectPath(r *http.Request, path string) string {
	target := s.mountedPath(path)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return target
}

func snapshotCount(snapshot *entrypoints.Snapshot) int {
	if snapshot == nil {
		return 0
	}
	return snapshot.EntryPointCount
}

func snapshotEntrypoints(snapshot *entrypoints.Snapshot) []entrypoints.EntryPoint {
	if snapshot == nil {
		return []entrypoints.EntryPoint{}
	}
	return snapshot.Entrypoints
}

func snapshotSource(snapshot *entrypoints.Snapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.Source
}

func findEndpointByID(snapshot *entrypoints.Snapshot, id string) *entrypoints.EntryPoint {
	if snapshot == nil {
		return nil
	}
	for i := range snapshot.Entrypoints {
		if snapshot.Entrypoints[i].ID == id {
			return &snapshot.Entrypoints[i]
		}
	}
	return nil
}

func sanitizeReportPayload(payload map[string]any) {
	delete(payload, "session_id")
	delete(payload, "user_id")
	delete(payload, "username")
	delete(payload, "token")
	if raw, ok := payload["iframe_context"].(map[string]any); ok {
		delete(raw, "user_id")
		delete(raw, "username")
		delete(raw, "token")
	}
}

func rawObject(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return b
}

type sessionContextKey struct{}

func sessionFromContext(ctx context.Context) *store.Session {
	session, _ := ctx.Value(sessionContextKey{}).(*store.Session)
	return session
}
