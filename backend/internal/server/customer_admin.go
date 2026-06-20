package server

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/entrypoints"
	"sub2api-origin-lg/backend/internal/netinfo"
	"sub2api-origin-lg/backend/internal/store"
	"sub2api-origin-lg/backend/internal/urlx"
)

type iframeBootstrapRequest struct {
	UserID      string `json:"user_id"`
	Ticket      string `json:"ticket"`
	Token       string `json:"token"`
	LegacyToken string `json:"legacy_token"`
	Theme       string `json:"theme"`
	Lang        string `json:"lang"`
	UIMode      string `json:"ui_mode"`
	SrcHost     string `json:"src_host"`
	SrcURL      string `json:"src_url"`
}

func (s *Server) customerBootstrap(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readBootstrapRequest(w, r)
	if !ok {
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	credential := strings.TrimSpace(req.Ticket)
	if credential == "" && s.cfg.Security.AllowLegacyCustomerToken {
		credential = firstNonEmpty(req.LegacyToken, req.Token)
	}
	if credential == "" {
		http.Error(w, "ticket is required", http.StatusUnauthorized)
		return
	}
	user, err := s.verifyBootstrapUser(r.Context(), credential, req.UserID)
	if err != nil {
		http.Error(w, err.Error(), statusForBootstrapError(err))
		return
	}
	sessionToken, session := s.newSession("customer", user, req, []string{
		"customer:diagnose",
		"customer:report:create",
		"customer:report:read_own",
		"customer:report:share",
	})
	if err := s.store.CreateSession(r.Context(), session); err != nil {
		http.Error(w, "create session failed", http.StatusInternalServerError)
		return
	}
	snapshot, _ := s.cache.Get(r.Context(), true)
	writeJSON(w, map[string]any{
		"session_id":    session.ID,
		"session_token": sessionToken,
		"session_type":  "customer",
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
		"user":          user,
		"display_user": map[string]any{
			"id":   user.ID,
			"name": displayUserName(user),
		},
		"app": map[string]any{
			"public_path":   s.cfg.App.PublicPath,
			"iframe_origin": requestOrigin(r, s.cfg),
			"theme":         req.Theme,
			"lang":          req.Lang,
		},
		"probe":             s.cfg.Probe,
		"entrypoint_count":  snapshotCount(snapshot),
		"entrypoints":       s.safeEntrypoints(r.Context(), snapshot),
		"entrypoint_source": snapshotSource(snapshot),
	})
}

func (s *Server) adminBootstrap(w http.ResponseWriter, r *http.Request) {
	if !s.adminHostAllowed(r.Host) {
		http.NotFound(w, r)
		return
	}
	req, ok := s.readBootstrapRequest(w, r)
	if !ok {
		return
	}
	credential := firstNonEmpty(req.Ticket, req.Token, req.LegacyToken)
	if credential == "" {
		http.Error(w, "admin ticket is required", http.StatusUnauthorized)
		return
	}
	user, err := s.verifyBootstrapUser(r.Context(), credential, strings.TrimSpace(req.UserID))
	if err != nil {
		http.Error(w, err.Error(), statusForBootstrapError(err))
		return
	}
	if !isAdminUser(user) {
		http.Error(w, "admin permission required", http.StatusForbidden)
		return
	}
	scopes := []string{
		"admin:reports:list",
		"admin:reports:read_internal",
		"admin:entrypoints:read_inventory",
	}
	sessionToken, session := s.newSession("admin", user, req, scopes)
	if err := s.store.CreateSession(r.Context(), session); err != nil {
		http.Error(w, "create session failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"session_id":    session.ID,
		"session_token": sessionToken,
		"session_type":  "admin",
		"scopes":        scopes,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
		"user":          user,
	})
}

func (s *Server) readBootstrapRequest(w http.ResponseWriter, r *http.Request) (iframeBootstrapRequest, bool) {
	var req iframeBootstrapRequest
	if err := decodeJSON(r, &req, 1<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return req, false
	}
	req.SrcHost = normalizeSrcHost(req.SrcHost)
	req.SrcURL = strings.TrimSpace(req.SrcURL)
	if !s.srcHostAllowed(req.SrcHost) {
		http.Error(w, "src_host not allowed", http.StatusForbidden)
		return req, false
	}
	if !srcURLMatchesHost(req.SrcURL, req.SrcHost) {
		http.Error(w, "src_url does not match src_host", http.StatusForbidden)
		return req, false
	}
	return req, true
}

func (s *Server) verifyBootstrapUser(ctx context.Context, credential, expectedUserID string) (*adminclient.User, error) {
	if strings.TrimSpace(s.cfg.Sub2API.AdminBaseURL) == "" {
		return nil, errServiceUnavailable("sub2api user verification is not configured")
	}
	user, err := s.admin.VerifyUser(ctx, credential)
	if err != nil {
		return nil, errUnauthorized("token verification failed")
	}
	if expectedUserID != "" {
		if err := validateVerifiedUser(expectedUserID, user); err != nil {
			return nil, errForbidden(err.Error())
		}
	}
	return user, nil
}

func (s *Server) newSession(sessionType string, user *adminclient.User, req iframeBootstrapRequest, scopes []string) (string, store.Session) {
	sessionID := "sess_" + randomToken(18)
	prefix := "lg_cust_"
	if sessionType == "admin" {
		prefix = "lg_admin_"
	}
	sessionToken := prefix + randomToken(32)
	now := time.Now()
	scopesJSON, _ := json.Marshal(scopes)
	return sessionToken, store.Session{
		ID:           sessionID,
		TokenHash:    store.TokenHash(sessionToken),
		UserID:       user.ID,
		Username:     user.Username,
		SessionType:  sessionType,
		ScopesJSON:   string(scopesJSON),
		ParentOrigin: originFromSrcURL(req.SrcURL),
		SrcHost:      req.SrcHost,
		SrcURL:       safeSrcURL(req.SrcURL),
		TicketID:     shortTicketID(req.Ticket),
		Theme:        req.Theme,
		Lang:         req.Lang,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.cfg.Security.SessionTTL),
	}
}

func (s *Server) customerEntrypoints(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.cache.Get(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"entrypoints": s.safeEntrypoints(r.Context(), snapshot),
		"policy": map[string]any{
			"hide_urls_in_ui":               true,
			"disable_raw_debug_in_customer": true,
		},
		"source":           snapshotSource(snapshot),
		"entrypoint_count": snapshotCount(snapshot),
		"public_path":      s.cfg.App.PublicPath,
	})
}

func (s *Server) createCustomerReport(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	var req customerReportRequest
	if err := decodeJSON(r, &req, 8<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Samples) == 0 {
		http.Error(w, "samples are required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	reportID := "rpt_" + now.Format("20060102_") + randomToken(12)
	shareToken := "share_" + randomToken(24)
	shareURL := publicMountURL(r, s.cfg) + "/report/" + url.PathEscape(reportID)
	if s.cfg.Security.CustomerReportShareEnabled {
		shareURL += "?share_token=" + url.QueryEscape(shareToken)
	}
	snapshot, _ := s.cache.Get(r.Context(), false)
	customEntrypoints := s.customerCustomEntrypoints(req.CustomEndpoints)
	customerReport, internalReport, supportSummary, meta := buildReports(reportID, session, req, snapshot, customEntrypoints, now)
	customerJSON, _ := json.Marshal(customerReport)
	internalJSON, _ := json.Marshal(internalReport)
	supportJSON, _ := json.Marshal(supportSummary)
	problemCodesJSON, _ := json.Marshal(meta.ProblemCodes)
	report := store.Report{
		ID:                 reportID,
		SessionID:          session.ID,
		UserID:             session.UserID,
		SummaryJSON:        supportJSON,
		PayloadJSON:        internalJSON,
		CustomerReportJSON: customerJSON,
		InternalReportJSON: internalJSON,
		SupportSummaryJSON: supportJSON,
		ShareTokenHash:     store.TokenHash(shareToken),
		ShareEnabled:       s.cfg.Security.CustomerReportShareEnabled,
		Level:              meta.Level,
		Score:              meta.Score,
		ProblemCodesJSON:   problemCodesJSON,
		CreatedAt:          now,
		UpdatedAt:          now,
		CustomerExpiresAt:  now.Add(reportRetention),
		InternalExpiresAt:  now.Add(reportRetention),
	}
	if err := s.store.CreateReport(r.Context(), report); err != nil {
		http.Error(w, "create report failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"report_id":        reportID,
		"share_url":        shareURL,
		"customer_summary": customerReport["summary"],
	})
}

func (s *Server) customerNetinfoResolve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomEndpoints []customerCustomEndpoint `json:"custom_endpoints"`
	}
	if err := decodeJSON(r, &req, 1<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	customEntrypoints := s.customerCustomEntrypoints(req.CustomEndpoints)
	items := make([]map[string]any, 0, len(customEntrypoints))
	for _, ep := range customEntrypoints {
		items = append(items, map[string]any{
			"endpoint_public_id": ep.ID,
			"dns_records":        s.endpointDNSInfo(r.Context(), ep.Host),
		})
	}
	writeJSON(w, map[string]any{"items": items})
}

func (s *Server) getCustomerReport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/customer/reports/")
	report, err := s.findReport(r.Context(), id)
	if err != nil {
		http.Error(w, "get report failed", http.StatusInternalServerError)
		return
	}
	if report == nil {
		http.NotFound(w, r)
		return
	}
	if !s.customerReportAllowed(r, report) {
		http.Error(w, "report access denied", http.StatusUnauthorized)
		return
	}
	writeJSON(w, report.CustomerReportJSON)
}

func (s *Server) listAdminReports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.ReportFilter{
		Page:             intQuery(q.Get("page"), 1),
		PageSize:         intQuery(q.Get("page_size"), 20),
		UserID:           strings.TrimSpace(q.Get("user_id")),
		ReportID:         strings.TrimSpace(q.Get("report_id")),
		Level:            strings.TrimSpace(q.Get("level")),
		EndpointPublicID: strings.TrimSpace(q.Get("endpoint_public_id")),
		ProblemCode:      strings.TrimSpace(q.Get("problem_code")),
		From:             timeQuery(q.Get("from")),
		To:               timeQuery(q.Get("to")),
	}
	result, err := s.store.ListReports(r.Context(), filter)
	if err != nil {
		http.Error(w, "list reports failed", http.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, report := range result.Items {
		items = append(items, adminReportItem(report))
	}
	writeJSON(w, map[string]any{
		"items":     items,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

func (s *Server) getAdminReport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/reports/")
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
	runID := internalRunID(report.InternalReportJSON)
	events, _ := s.store.ListDiagEvents(r.Context(), runID, nil)
	ownerSession, _ := s.store.GetSession(r.Context(), report.SessionID)
	writeJSON(w, map[string]any{
		"report_id":              report.ID,
		"customer_report":        report.CustomerReportJSON,
		"internal_report":        report.InternalReportJSON,
		"support_summary":        report.SupportSummaryJSON,
		"owner_session":          adminSessionInfo(ownerSession),
		"diag_events_available":  len(events) > 0,
		"server_probe_available": false,
		"customer_share_enabled": report.ShareEnabled,
		"customer_expires_at":    report.CustomerExpiresAt,
		"internal_expires_at":    report.InternalExpiresAt,
	})
}

func (s *Server) listReportEvents(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/reports/")
	id = strings.TrimSuffix(id, "/events")
	id = strings.Trim(id, "/")
	report, err := s.findReport(r.Context(), id)
	if err != nil {
		http.Error(w, "get report failed", http.StatusInternalServerError)
		return
	}
	if report == nil {
		http.NotFound(w, r)
		return
	}
	events, err := s.store.ListDiagEvents(r.Context(), internalRunID(report.InternalReportJSON), nil)
	if err != nil {
		http.Error(w, "list events failed", http.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, map[string]any{
			"id":                 event.ID,
			"run_id":             event.RunID,
			"request_id":         event.RequestID,
			"endpoint_public_id": event.EndpointPublicID,
			"kind":               event.Kind,
			"safe_summary":       event.SafeSummaryJSON,
			"internal":           event.InternalJSON,
			"created_at":         event.CreatedAt,
		})
	}
	writeJSON(w, map[string]any{"items": items})
}

func (s *Server) adminEntrypointInventory(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.cache.Get(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"source":         snapshotSource(snapshot),
		"public_path":    s.cfg.App.PublicPath,
		"valid_count":    snapshotCount(snapshot),
		"filtered_count": 0,
		"entrypoints":    snapshotEntrypoints(snapshot),
	})
}

func (s *Server) customerReportAllowed(r *http.Request, report *store.Report) bool {
	if report == nil {
		return false
	}
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		session, err := s.store.FindSessionByToken(r.Context(), token)
		if err == nil && session != nil && session.SessionType == "customer" && session.ID == report.SessionID {
			return true
		}
	}
	return reportShareAllowed(report, r.URL.Query().Get("share_token"))
}

func reportShareAllowed(report *store.Report, shareToken string) bool {
	if report == nil || !report.ShareEnabled {
		return false
	}
	if report.ShareTokenHash == "" {
		return true
	}
	return store.TokenHash(strings.TrimSpace(shareToken)) == report.ShareTokenHash
}

type customerReportRequest struct {
	SchemaVersion   string                   `json:"schema_version"`
	RunID           string                   `json:"run_id"`
	ClientEnv       map[string]string        `json:"client_env"`
	EndpointLabels  map[string]string        `json:"endpoint_labels"`
	EndpointNetInfo map[string]endpointInfo  `json:"endpoint_netinfo"`
	CustomEndpoints []customerCustomEndpoint `json:"custom_endpoints"`
	Samples         []customerSample         `json:"samples"`
}

type endpointInfo struct {
	OriginPeer netinfo.IPInfo   `json:"origin_peer"`
	DNSRecords []netinfo.IPInfo `json:"dns_records"`
}

type customerCustomEndpoint struct {
	EndpointPublicID string `json:"endpoint_public_id"`
	DisplayName      string `json:"display_name"`
	ProbeBaseURL     string `json:"probe_base_url"`
}

type customerSample struct {
	EndpointPublicID      string   `json:"endpoint_public_id"`
	Kind                  string   `json:"kind"`
	RequestID             string   `json:"request_id"`
	Size                  string   `json:"size"`
	OK                    bool     `json:"ok"`
	DurationMS            *float64 `json:"duration_ms"`
	TTFBMS                *float64 `json:"ttfb_ms"`
	TTFTMS                *float64 `json:"ttft_ms"`
	EndpointMS            *float64 `json:"endpoint_ms"`
	MBPS                  *float64 `json:"mbps"`
	ErrorKind             string   `json:"error_kind"`
	ErrorMessage          string   `json:"error_message"`
	TimingDetailAvailable bool     `json:"timing_detail_available"`
	StreamBuffered        bool     `json:"stream_buffered"`
}

type reportMeta struct {
	Level        string
	Score        int
	ProblemCodes []string
}

func buildReports(reportID string, session *store.Session, req customerReportRequest, snapshot *entrypoints.Snapshot, customEntrypoints []entrypoints.EntryPoint, now time.Time) (map[string]any, map[string]any, map[string]any, reportMeta) {
	entrypointMap := map[string]entrypoints.EntryPoint{}
	if snapshot != nil {
		for _, ep := range snapshot.Entrypoints {
			entrypointMap[ep.ID] = ep
		}
	}
	for _, ep := range customEntrypoints {
		if ep.ID != "" {
			entrypointMap[ep.ID] = ep
		}
	}
	grouped := map[string][]customerSample{}
	for _, sample := range req.Samples {
		id := safeIdentifier(sample.EndpointPublicID)
		if id == "" {
			continue
		}
		sample.EndpointPublicID = id
		grouped[id] = append(grouped[id], sample)
	}
	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	customerEntrypoints := make([]map[string]any, 0, len(ids))
	internalEntrypoints := make([]map[string]any, 0, len(ids))
	allProblemCodes := []string{}
	bestName := ""
	bestScore := -1
	bestLevel := "bad"
	requestIDs := []string{}
	for _, id := range ids {
		ep := entrypointMap[id]
		displayName := ep.Name
		if displayName == "" {
			displayName = safeDisplayLabel(req.EndpointLabels[id])
		}
		if displayName == "" {
			displayName = "入口 " + id
		}
		metrics, codes := summarizeEndpointSamples(grouped[id])
		level := levelFromScore(metrics.Score)
		netInfo := sanitizeEndpointInfo(req.EndpointNetInfo[id])
		customerEntrypoints = append(customerEntrypoints, map[string]any{
			"endpoint_public_id":         id,
			"display_name":               displayName,
			"level":                      level,
			"success_rate":               metrics.SuccessRate,
			"http_loss_rate":             metrics.HTTPLossRate,
			"timeout_rate":               metrics.TimeoutRate,
			"latency_p50_ms":             metrics.LatencyP50,
			"latency_p95_ms":             metrics.LatencyP95,
			"ttfb_p95_ms":                metrics.TTFBP95,
			"download_mbps":              metrics.DownloadMBPS,
			"upload_mbps":                metrics.UploadMBPS,
			"stream_first_event_ms":      metrics.StreamFirstEventMS,
			"stream_buffering_suspected": metrics.StreamBuffered,
			"cors_ok":                    metrics.CORSOK,
			"timing_detail_available":    metrics.TimingDetailAvailable,
			"origin_peer":                netInfo.OriginPeer,
			"endpoint_dns":               netInfo.DNSRecords,
		})
		internalEntrypoints = append(internalEntrypoints, map[string]any{
			"endpoint_public_id":     id,
			"internal_entrypoint_id": ep.ID,
			"name":                   displayName,
			"base_url":               ep.BaseURL,
			"lg_base_url":            ep.LGBaseURL,
			"source":                 ep.Source,
			"browser_metrics":        metrics,
			"netinfo":                netInfo,
			"diag_event_refs":        sampleRequestIDs(grouped[id]),
			"server_probe":           map[string]any{},
		})
		for _, requestID := range sampleRequestIDs(grouped[id]) {
			if !containsString(requestIDs, requestID) {
				requestIDs = append(requestIDs, requestID)
			}
		}
		allProblemCodes = appendUniqueStrings(allProblemCodes, codes...)
		if metrics.Score > bestScore {
			bestScore = metrics.Score
			bestLevel = level
			bestName = displayName
		}
	}
	if bestScore < 0 {
		bestScore = 0
	}
	if len(allProblemCodes) == 0 {
		allProblemCodes = []string{}
	}
	shortCode := "LG-" + strings.ToUpper(strings.ReplaceAll(randomToken(4), "_", ""))[:5]
	customerReport := map[string]any{
		"schema_version": "2.0",
		"report_id":      reportID,
		"created_at":     now.Format(time.RFC3339),
		"summary": map[string]any{
			"level":              bestLevel,
			"score":              bestScore,
			"best_endpoint_name": bestName,
			"main_message":       customerMainMessage(bestLevel),
			"recommendations":    customerRecommendations(bestLevel, allProblemCodes),
		},
		"client_env":  sanitizeClientEnv(req.ClientEnv),
		"entrypoints": customerEntrypoints,
		"support_reference": map[string]any{
			"report_id":  reportID,
			"short_code": shortCode,
		},
	}
	supportSummary := map[string]any{
		"report_id":          reportID,
		"user_id":            session.UserID,
		"level":              bestLevel,
		"score":              bestScore,
		"best_endpoint_name": bestName,
		"problem_codes":      allProblemCodes,
		"safe_message":       customerMainMessage(bestLevel),
		"internal_hint":      internalHint(allProblemCodes),
	}
	internalReport := map[string]any{
		"schema_version": "2.0",
		"report_id":      reportID,
		"run_id":         safeIdentifier(req.RunID),
		"owner_user_id":  session.UserID,
		"entrypoint_inventory": map[string]any{
			"admin_raw_count":       len(snapshotEntrypoints(snapshot)),
			"valid_count":           len(snapshotEntrypoints(snapshot)) + len(customEntrypoints),
			"customer_custom_count": len(customEntrypoints),
			"filtered_count":        0,
			"filtered":              []any{},
		},
		"entrypoints":      internalEntrypoints,
		"diag_request_ids": requestIDs,
		"diagnosis":        internalDiagnosis(allProblemCodes),
		"support_summary":  supportSummary,
	}
	return customerReport, internalReport, supportSummary, reportMeta{
		Level:        bestLevel,
		Score:        bestScore,
		ProblemCodes: allProblemCodes,
	}
}

type endpointMetrics struct {
	SuccessRate           float64  `json:"success_rate"`
	HTTPLossRate          float64  `json:"http_loss_rate"`
	TimeoutRate           float64  `json:"timeout_rate"`
	LatencyP50            *float64 `json:"latency_p50_ms"`
	LatencyP95            *float64 `json:"latency_p95_ms"`
	TTFBP95               *float64 `json:"ttfb_p95_ms"`
	DownloadMBPS          *float64 `json:"download_mbps"`
	UploadMBPS            *float64 `json:"upload_mbps"`
	StreamFirstEventMS    *float64 `json:"stream_first_event_ms"`
	StreamBuffered        bool     `json:"stream_buffering_suspected"`
	CORSOK                bool     `json:"cors_ok"`
	TimingDetailAvailable bool     `json:"timing_detail_available"`
	Score                 int      `json:"score"`
}

func summarizeEndpointSamples(samples []customerSample) (endpointMetrics, []string) {
	total := len(samples)
	if total == 0 {
		return endpointMetrics{Score: 0}, []string{"DIAG_ENDPOINT_UNAVAILABLE"}
	}
	success := 0
	timeouts := 0
	corsOK := true
	timingDetail := false
	streamBuffered := false
	durations := []float64{}
	ttfb := []float64{}
	download := []float64{}
	upload := []float64{}
	var streamFirst *float64
	for _, sample := range samples {
		if sample.OK {
			success++
		}
		if sample.ErrorKind == "timeout" {
			timeouts++
		}
		if strings.Contains(strings.ToLower(sample.ErrorMessage), "cors") || strings.Contains(strings.ToLower(sample.ErrorMessage), "failed to fetch") {
			corsOK = false
		}
		if sample.TimingDetailAvailable {
			timingDetail = true
		}
		if sample.StreamBuffered {
			streamBuffered = true
		}
		if sample.OK && sample.DurationMS != nil && sample.Kind != "stream" {
			durations = append(durations, *sample.DurationMS)
		}
		if sample.OK && sample.TTFBMS != nil {
			ttfb = append(ttfb, *sample.TTFBMS)
		}
		if sample.OK && sample.MBPS != nil && sample.Kind == "download" {
			download = append(download, *sample.MBPS)
		}
		if sample.OK && sample.MBPS != nil && sample.Kind == "upload" {
			upload = append(upload, *sample.MBPS)
		}
		if sample.OK && sample.Kind == "stream" && sample.TTFTMS != nil {
			streamFirst = sample.TTFTMS
		}
	}
	successRate := roundRatio(success, total)
	score := int(math.Round(successRate * 100))
	if !corsOK {
		score -= 30
	}
	if streamBuffered {
		score -= 10
	}
	if p95 := percentileFloat(durations, 95); p95 != nil && *p95 > 1500 {
		score -= 20
	} else if p95 != nil && *p95 > 800 {
		score -= 10
	}
	score = clamp(score, 0, 100)
	codes := []string{}
	if success == 0 {
		codes = append(codes, "DIAG_ENDPOINT_UNAVAILABLE")
	}
	if !timingDetail {
		codes = append(codes, "TIMING_ALLOW_ORIGIN_MISSING")
	}
	if !corsOK {
		codes = append(codes, "CORS_PREFLIGHT_FAILED")
	}
	if streamBuffered {
		codes = append(codes, "STREAM_BUFFERING_SUSPECTED")
	}
	if score < 80 && success > 0 {
		codes = append(codes, "CLIENT_NETWORK_OR_EDGE_PROBLEM")
	}
	return endpointMetrics{
		SuccessRate:           successRate,
		HTTPLossRate:          roundRatio(total-success, total),
		TimeoutRate:           roundRatio(timeouts, total),
		LatencyP50:            percentileFloat(durations, 50),
		LatencyP95:            percentileFloat(durations, 95),
		TTFBP95:               percentileFloat(ttfb, 95),
		DownloadMBPS:          averageFloat(download),
		UploadMBPS:            averageFloat(upload),
		StreamFirstEventMS:    streamFirst,
		StreamBuffered:        streamBuffered,
		CORSOK:                corsOK,
		TimingDetailAvailable: timingDetail,
		Score:                 score,
	}, codes
}

func (s *Server) safeEntrypoints(ctx context.Context, snapshot *entrypoints.Snapshot) []map[string]any {
	if snapshot == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(snapshot.Entrypoints))
	for i, ep := range snapshot.Entrypoints {
		out = append(out, map[string]any{
			"id":                 ep.ID,
			"endpoint_public_id": ep.ID,
			"display_name":       ep.Name,
			"name":               ep.Name,
			"display_order":      i + 1,
			"description":        ep.Description,
			"probe_base_url":     ep.LGBaseURL,
			"dns_records":        s.endpointDNSInfo(ctx, ep.Host),
			"capabilities":       []string{"meta", "ping", "blob", "upload", "stream", "headers"},
		})
	}
	return out
}

func (s *Server) endpointDNSInfo(ctx context.Context, host string) []netinfo.IPInfo {
	return netinfo.ResolveHost(ctx, host, 6)
}

func sanitizeEndpointInfo(info endpointInfo) endpointInfo {
	return endpointInfo{
		OriginPeer: sanitizeIPInfo(info.OriginPeer),
		DNSRecords: sanitizeIPInfoList(info.DNSRecords, 8),
	}
}

func sanitizeIPInfoList(items []netinfo.IPInfo, limit int) []netinfo.IPInfo {
	if len(items) == 0 || limit <= 0 {
		return []netinfo.IPInfo{}
	}
	out := make([]netinfo.IPInfo, 0, min(len(items), limit))
	seen := map[string]bool{}
	for _, item := range items {
		clean := sanitizeIPInfo(item)
		if clean.IP == "" || seen[clean.IP] {
			continue
		}
		seen[clean.IP] = true
		out = append(out, clean)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func sanitizeIPInfo(item netinfo.IPInfo) netinfo.IPInfo {
	ip := strings.TrimSpace(item.IP)
	if ip == "" || net.ParseIP(strings.Trim(ip, "[]")) == nil {
		return netinfo.IPInfo{}
	}
	return netinfo.IPInfo{
		IP:     ip,
		ASN:    safeASN(item.ASN),
		ASName: safeASName(item.ASName),
	}
}

func safeASN(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(value), "AS"))
	if value == "" {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return value
}

func safeASName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len([]rune(value)) > 80 {
		return string([]rune(value)[:80])
	}
	return value
}

const maxCustomerCustomEndpoints = 8

func (s *Server) customerCustomEntrypoints(items []customerCustomEndpoint) []entrypoints.EntryPoint {
	out := make([]entrypoints.EntryPoint, 0, min(len(items), maxCustomerCustomEndpoints))
	seen := map[string]bool{}
	for _, item := range items {
		if len(out) >= maxCustomerCustomEndpoints {
			break
		}
		id := safeIdentifier(item.EndpointPublicID)
		if id == "" || !strings.HasPrefix(id, "custom_") || seen[id] {
			continue
		}
		ep, ok := s.customerCustomEntrypoint(id, item)
		if !ok {
			continue
		}
		seen[id] = true
		out = append(out, ep)
	}
	return out
}

func (s *Server) customerCustomEntrypoint(id string, item customerCustomEndpoint) (entrypoints.EntryPoint, bool) {
	parsed, err := url.Parse(strings.TrimSpace(item.ProbeBaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return entrypoints.EntryPoint{}, false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if !s.customerCustomEndpointAllowed(parsed) {
		return entrypoints.EntryPoint{}, false
	}
	parsed.Path = customProbePath(parsed.Path, s.cfg.App.PublicPath)
	name := safeDisplayLabel(item.DisplayName)
	if name == "" {
		name = "自定义入口"
	}
	return entrypoints.EntryPoint{
		ID:          id,
		Source:      "customer_custom",
		Name:        name,
		Description: "customer supplied endpoint",
		BaseURL:     customBaseURL(parsed, s.cfg.App.PublicPath),
		PublicPath:  s.cfg.App.PublicPath,
		LGBaseURL:   strings.TrimRight(parsed.String(), "/"),
		Origin:      urlx.Origin(parsed),
		Host:        parsed.Host,
		Scheme:      parsed.Scheme,
		Enabled:     true,
	}, true
}

func (s *Server) customerCustomEndpointAllowed(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if s.cfg.App.Env == "production" && u.Scheme == "http" && !s.cfg.Security.AllowHTTPEndpoints {
		return false
	}
	if !s.cfg.Security.AllowPrivateEndpoints && urlx.IsPrivateHost(u.Hostname()) {
		return false
	}
	return true
}

func customProbePath(currentPath, publicPath string) string {
	cleanPublicPath := strings.TrimRight("/"+strings.TrimLeft(publicPath, "/"), "/")
	if cleanPublicPath == "" {
		cleanPublicPath = "/"
	}
	pathValue := strings.TrimRight(currentPath, "/")
	if pathValue == "" {
		return cleanPublicPath
	}
	if pathValue == cleanPublicPath || strings.HasSuffix(pathValue, cleanPublicPath) {
		return pathValue
	}
	return pathValue + cleanPublicPath
}

func customBaseURL(probeURL *url.URL, publicPath string) string {
	base := *probeURL
	cleanPublicPath := strings.TrimRight("/"+strings.TrimLeft(publicPath, "/"), "/")
	pathValue := strings.TrimRight(base.Path, "/")
	switch {
	case cleanPublicPath == "" || cleanPublicPath == "/":
		base.Path = ""
	case pathValue == cleanPublicPath:
		base.Path = ""
	case strings.HasSuffix(pathValue, cleanPublicPath):
		base.Path = strings.TrimRight(strings.TrimSuffix(pathValue, cleanPublicPath), "/")
	default:
		base.Path = pathValue
	}
	base.RawQuery = ""
	base.Fragment = ""
	return strings.TrimRight(base.String(), "/")
}

func adminSessionInfo(session *store.Session) map[string]any {
	if session == nil {
		return map[string]any{}
	}
	return map[string]any{
		"session_id":    session.ID,
		"user_id":       session.UserID,
		"username":      session.Username,
		"session_type":  session.SessionType,
		"parent_origin": session.ParentOrigin,
		"src_host":      session.SrcHost,
		"src_url":       session.SrcURL,
		"ticket_id":     session.TicketID,
		"theme":         session.Theme,
		"lang":          session.Lang,
		"created_at":    session.CreatedAt,
		"expires_at":    session.ExpiresAt,
		"scopes_json":   session.ScopesJSON,
	}
}

func adminReportItem(report store.Report) map[string]any {
	summary := map[string]any{}
	_ = json.Unmarshal(report.SupportSummaryJSON, &summary)
	problemCodes := []string{}
	_ = json.Unmarshal(report.ProblemCodesJSON, &problemCodes)
	return map[string]any{
		"report_id":              report.ID,
		"created_at":             report.CreatedAt,
		"user_id":                report.UserID,
		"level":                  firstNonEmpty(report.Level, stringMapValue(summary, "level")),
		"score":                  report.Score,
		"best_endpoint_name":     stringMapValue(summary, "best_endpoint_name"),
		"problem_codes":          problemCodes,
		"customer_share_enabled": report.ShareEnabled,
	}
}

func internalRunID(raw json.RawMessage) string {
	var body struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.RunID
}

type codedError struct {
	message string
	status  int
}

func (e codedError) Error() string { return e.message }

func errUnauthorized(message string) error {
	return codedError{message: message, status: http.StatusUnauthorized}
}
func errForbidden(message string) error {
	return codedError{message: message, status: http.StatusForbidden}
}
func errServiceUnavailable(message string) error {
	return codedError{message: message, status: http.StatusServiceUnavailable}
}

func statusForBootstrapError(err error) int {
	if coded, ok := err.(codedError); ok {
		return coded.status
	}
	return http.StatusUnauthorized
}

func fallbackOrigins(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isAdminUser(user *adminclient.User) bool {
	if user == nil {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(user.Role))
	return user.IsAdmin || role == "admin" || role == "root" || role == "owner"
}

func displayUserName(user *adminclient.User) string {
	if user == nil {
		return ""
	}
	if strings.TrimSpace(user.Username) != "" {
		return user.Username
	}
	return "用户 " + user.ID
}

func safeSrcURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func originFromSrcURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func shortTicketID(ticket string) string {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return ""
	}
	hash := store.TokenHash(ticket)
	return hash[:16]
}

func sanitizeClientEnv(env map[string]string) map[string]string {
	allowed := []string{"browser", "os", "timezone", "language", "viewport"}
	out := map[string]string{}
	for _, key := range allowed {
		if value := strings.TrimSpace(env[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

func safeDisplayLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "://") || strings.Contains(value, ".") {
		return ""
	}
	if len([]rune(value)) > 40 {
		return string([]rune(value)[:40])
	}
	return value
}

func safeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sampleRequestIDs(samples []customerSample) []string {
	out := []string{}
	for _, sample := range samples {
		id := safeIdentifier(sample.RequestID)
		if id != "" && !containsString(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func customerMainMessage(level string) string {
	switch level {
	case "good":
		return "当前网络访问正常"
	case "warning":
		return "当前网络存在波动"
	default:
		return "当前诊断发现异常"
	}
}

func customerRecommendations(level string, codes []string) []string {
	if containsString(codes, "CORS_PREFLIGHT_FAILED") {
		return []string{"请联系支持并提供报告编号"}
	}
	if level == "good" {
		return []string{"继续使用推荐入口"}
	}
	if level == "warning" {
		return []string{"可切换推荐入口或稍后重试"}
	}
	return []string{"更换网络后重试", "联系支持并提供报告编号"}
}

func internalHint(codes []string) string {
	if containsString(codes, "STREAM_BUFFERING_SUSPECTED") {
		return "检查 CDN/Nginx proxy_buffering、gzip、no-transform"
	}
	if containsString(codes, "CORS_PREFLIGHT_FAILED") {
		return "检查 /diag/* CORS 与 Timing-Allow-Origin"
	}
	if containsString(codes, "DIAG_ENDPOINT_UNAVAILABLE") {
		return "检查 public_path 反向代理挂载和 /diag/* 转发"
	}
	return "查看入口指标和 diag_events 时间线"
}

func internalDiagnosis(codes []string) []map[string]any {
	out := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		out = append(out, map[string]any{
			"code":          code,
			"severity":      severityForCode(code),
			"evidence":      []string{"browser_samples", "diag_events"},
			"operator_hint": internalHint([]string{code}),
		})
	}
	return out
}

func severityForCode(code string) string {
	if code == "DIAG_ENDPOINT_UNAVAILABLE" || code == "CORS_PREFLIGHT_FAILED" {
		return "bad"
	}
	return "warning"
}

func levelFromScore(score int) string {
	if score >= 90 {
		return "good"
	}
	if score >= 70 {
		return "warning"
	}
	return "bad"
}

func roundRatio(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return math.Round((float64(n)/float64(d))*1000) / 1000
}

func percentileFloat(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return roundedPtr(sorted[0])
	}
	pos := (p / 100) * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return roundedPtr(sorted[lower])
	}
	weight := pos - float64(lower)
	return roundedPtr(sorted[lower]*(1-weight) + sorted[upper]*weight)
}

func averageFloat(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return roundedPtr(sum / float64(len(values)))
}

func roundedPtr(value float64) *float64 {
	rounded := math.Round(value*100) / 100
	return &rounded
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func appendUniqueStrings(values []string, additions ...string) []string {
	for _, item := range additions {
		if item != "" && !containsString(values, item) {
			values = append(values, item)
		}
	}
	return values
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func intQuery(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func timeQuery(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func stringMapValue(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}
