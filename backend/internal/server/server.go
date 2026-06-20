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
	cfg       *config.Config
	store     *store.Store
	cache     *entrypoints.Cache
	admin     *adminclient.Client
	diag      *probe.DiagHandlers
	staticDir string
}

const (
	reportRetention       = 72 * time.Hour
	reportCleanupInterval = time.Hour
)

func New(cfg *config.Config, db *store.Store, cache *entrypoints.Cache) *Server {
	diagHandlers := probe.NewDiagHandlers(cfg)
	server := &Server{
		cfg:       cfg,
		store:     db,
		cache:     cache,
		admin:     adminclient.New(cfg),
		diag:      diagHandlers,
		staticDir: "frontend/dist",
	}
	diagHandlers.SetEventRecorder(func(ctx context.Context, event probe.DiagEvent) error {
		return db.CreateDiagEvent(ctx, store.DiagEvent{
			ID:                   event.ID,
			RunID:                event.RunID,
			RequestID:            event.RequestID,
			EndpointPublicID:     event.EndpointPublicID,
			InternalEntryPointID: event.InternalEntryPointID,
			Kind:                 event.Kind,
			SafeSummaryJSON:      json.RawMessage(event.SafeSummaryJSON),
			InternalJSON:         json.RawMessage(event.InternalJSON),
			CreatedAt:            event.CreatedAt,
		})
	})
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
	case r.Method == http.MethodGet && (path == "/admin" || strings.HasPrefix(path, "/admin/")):
		if !s.adminHostAllowed(r.Host) {
			http.NotFound(w, r)
			return
		}
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
	case r.Method == http.MethodPost && path == "/api/customer/bootstrap":
		s.customerBootstrap(w, r)
	case r.Method == http.MethodPost && path == "/api/admin/bootstrap":
		s.adminBootstrap(w, r)
	case r.Method == http.MethodGet && path == "/api/entrypoints":
		s.requireSessionType("customer", s.customerEntrypoints)(w, r)
	case r.Method == http.MethodGet && path == "/api/customer/entrypoints":
		s.requireSessionType("customer", s.customerEntrypoints)(w, r)
	case r.Method == http.MethodPost && path == "/api/reports":
		s.requireSessionType("customer", s.createReport)(w, r)
	case r.Method == http.MethodPost && path == "/api/customer/reports":
		s.requireSessionType("customer", s.createReport)(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/customer/reports/"):
		s.getCustomerReport(w, r)
	case r.Method == http.MethodGet && path == "/api/admin/reports":
		s.requireSessionType("admin", s.listAdminReports)(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/admin/reports/") && strings.HasSuffix(path, "/events"):
		s.requireSessionType("admin", s.listReportEvents)(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/admin/reports/"):
		s.requireSessionType("admin", s.getAdminReport)(w, r)
	case r.Method == http.MethodGet && path == "/api/admin/entrypoints/inventory":
		s.requireSessionType("admin", s.adminEntrypointInventory)(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/reports/"):
		s.getReport(w, r)
	case r.Method == http.MethodGet && path == "/diag/meta":
		s.diag.Meta(w, r)
	case r.Method == http.MethodGet && path == s.cfg.Probe.Paths.Ping:
		s.diag.Ping(w, r)
	case r.Method == http.MethodGet && path == s.cfg.Probe.Paths.Blob:
		s.diag.Blob(w, r)
	case r.Method == http.MethodPost && path == s.cfg.Probe.Paths.Upload:
		s.diag.Upload(w, r)
	case r.Method == http.MethodGet && path == s.cfg.Probe.Paths.Stream:
		s.diag.Stream(w, r)
	case r.Method == http.MethodGet && path == "/diag/headers":
		s.diag.Headers(w, r)
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
				if !s.originAllowedForPath(path, r, origin) {
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
	return s.originAllowedAgainst(r, origin, s.cfg.Security.AllowedParentOrigins)
}

func (s *Server) originAllowedForPath(path string, r *http.Request, origin string) bool {
	if strings.HasPrefix(path, "/api/admin/") {
		return s.originAllowedAgainst(r, origin, fallbackOrigins(s.cfg.Security.AllowedAdminParentOrigins, s.cfg.Security.AllowedParentOrigins))
	}
	if strings.HasPrefix(path, "/api/customer/") {
		return s.originAllowedAgainst(r, origin, fallbackOrigins(s.cfg.Security.AllowedCustomerParentOrigins, s.cfg.Security.AllowedParentOrigins))
	}
	return s.originAllowed(r, origin)
}

func (s *Server) originAllowedAgainst(r *http.Request, origin string, allowedOrigins []string) bool {
	if strings.EqualFold(strings.TrimRight(origin, "/"), requestOrigin(r, s.cfg)) {
		return true
	}
	for _, allowed := range allowedOrigins {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	s.customerBootstrap(w, r)
}

func (s *Server) entrypoints(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.cache.Get(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, snapshot)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	s.createCustomerReport(w, r)
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
	writeJSON(w, report.CustomerReportJSON)
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

func (s *Server) requireSessionType(sessionType string, next http.HandlerFunc) http.HandlerFunc {
	return s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		session := sessionFromContext(r.Context())
		if session == nil || session.SessionType != sessionType {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	html, err := s.frontendIndexHTML()
	if err != nil {
		http.Error(w, "frontend is not built", http.StatusInternalServerError)
		return
	}
	s.writeFrontendHTML(w, r, html)
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
	if !reportShareAllowed(report, r.URL.Query().Get("share_token")) {
		http.Error(w, "report access denied", http.StatusUnauthorized)
		return
	}
	indexPath := filepath.Join(s.staticDir, "index.html")
	html, err := os.ReadFile(indexPath)
	if err != nil {
		http.Error(w, "frontend is not built", http.StatusInternalServerError)
		return
	}
	payload := strings.ReplaceAll(string(report.CustomerReportJSON), "</script", "<\\/script")
	injected := strings.Replace(s.rewriteFrontendAssetPaths(string(html)), "</head>", "<script>window.__SUB2API_LG_REPORT__="+payload+"</script></head>", 1)
	s.writeFrontendHTML(w, r, []byte(injected))
}

func (s *Server) frontendIndexHTML() ([]byte, error) {
	indexPath := filepath.Join(s.staticDir, "index.html")
	html, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	return []byte(s.rewriteFrontendAssetPaths(string(html))), nil
}

func (s *Server) writeFrontendHTML(w http.ResponseWriter, r *http.Request, html []byte) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+s.frameAncestors(r.URL.Path))
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

func (s *Server) frameAncestors(path string) string {
	origins := s.cfg.Security.AllowedParentOrigins
	if strings.HasPrefix(path, "/admin") {
		origins = fallbackOrigins(s.cfg.Security.AllowedAdminParentOrigins, origins)
	}
	if path == "/embed" {
		origins = fallbackOrigins(s.cfg.Security.AllowedCustomerParentOrigins, origins)
	}
	if len(origins) == 0 {
		return "'self'"
	}
	return strings.Join(origins, " ")
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

func (s *Server) adminHostAllowed(host string) bool {
	if len(s.cfg.Security.AllowedAdminHosts) == 0 {
		return true
	}
	host = normalizeSrcHost(host)
	for _, allowed := range s.cfg.Security.AllowedAdminHosts {
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
