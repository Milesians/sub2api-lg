package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App      AppConfig      `yaml:"app" json:"app"`
	Security SecurityConfig `yaml:"security" json:"security"`
	Sub2API  Sub2APIConfig  `yaml:"sub2api" json:"sub2api"`
	Probe    ProbeConfig    `yaml:"probe" json:"probe"`
	Storage  StorageConfig  `yaml:"storage" json:"storage"`
	Fallback FallbackConfig `yaml:"fallback" json:"fallback"`
}

type AppConfig struct {
	Listen                string `yaml:"listen" json:"listen"`
	PublicPath            string `yaml:"public_path" json:"public_path"`
	RouterPrefix          string `yaml:"router_prefix" json:"router_prefix"`
	PublicURL             string `yaml:"public_url" json:"public_url"`
	Env                   string `yaml:"env" json:"env"`
	TrustForwardedHeaders bool   `yaml:"trust_forwarded_headers" json:"trust_forwarded_headers"`
}

type SecurityConfig struct {
	AllowedParentOrigins  []string      `yaml:"allowed_parent_origins" json:"allowed_parent_origins"`
	AllowedSrcHosts       []string      `yaml:"allowed_src_hosts" json:"allowed_src_hosts"`
	DiagSessionSecret     string        `yaml:"diag_session_secret" json:"-"`
	DiagSessionTTLSeconds int           `yaml:"diag_session_ttl_seconds" json:"diag_session_ttl_seconds"`
	AllowHTTPEndpoints    bool          `yaml:"allow_http_endpoints" json:"allow_http_endpoints"`
	AllowPrivateEndpoints bool          `yaml:"allow_private_endpoints" json:"allow_private_endpoints"`
	SessionTTL            time.Duration `yaml:"-" json:"-"`
}

type Sub2APIConfig struct {
	AdminBaseURL            string `yaml:"admin_base_url" json:"-"`
	AdminAPIKey             string `yaml:"admin_api_key" json:"-"`
	SettingsPath            string `yaml:"settings_path" json:"settings_path"`
	UserInfoPath            string `yaml:"userinfo_path" json:"userinfo_path"`
	EndpointCacheTTLSeconds int    `yaml:"endpoint_cache_ttl_seconds" json:"endpoint_cache_ttl_seconds"`
}

type ProbeConfig struct {
	BrowserRepeat      int        `yaml:"browser_repeat" json:"browser_repeat"`
	BrowserTimeoutMS   int        `yaml:"browser_timeout_ms" json:"browser_timeout_ms"`
	ServerProbeEnabled bool       `yaml:"server_probe_enabled" json:"server_probe_enabled"`
	ServerRepeat       int        `yaml:"server_repeat" json:"server_repeat"`
	ServerTimeoutMS    int        `yaml:"server_timeout_ms" json:"server_timeout_ms"`
	EnableICMP         bool       `yaml:"enable_icmp" json:"enable_icmp"`
	Paths              ProbePaths `yaml:"paths" json:"paths"`
	BlobSizes          []string   `yaml:"blob_sizes" json:"blob_sizes"`
	MaxBlobSize        string     `yaml:"max_blob_size" json:"max_blob_size"`
	Allow5MBlob        bool       `yaml:"allow_5m_blob" json:"allow_5m_blob"`
	Stream             StreamSpec `yaml:"stream" json:"stream"`
}

type ProbePaths struct {
	Ping   string `yaml:"ping" json:"ping"`
	Blob   string `yaml:"blob" json:"blob"`
	Upload string `yaml:"upload" json:"upload"`
	Stream string `yaml:"stream" json:"stream"`
}

type StreamSpec struct {
	Events     int `yaml:"events" json:"events"`
	IntervalMS int `yaml:"interval_ms" json:"interval_ms"`
	Bytes      int `yaml:"bytes" json:"bytes"`
}

type StorageConfig struct {
	Driver string `yaml:"driver" json:"driver"`
	DSN    string `yaml:"dsn" json:"dsn"`
}

type FallbackConfig struct {
	StaticEndpoints []StaticEndpoint `yaml:"static_endpoints" json:"static_endpoints"`
}

type StaticEndpoint struct {
	Name        string `yaml:"name" json:"name"`
	BaseURL     string `yaml:"base_url" json:"base_url"`
	Description string `yaml:"description" json:"description"`
}

func Load(file string) (*Config, error) {
	cfg := Default()
	if b, err := os.ReadFile(file); err == nil {
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	applyEnv(cfg)
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		App: AppConfig{
			Listen:                "0.0.0.0:8080",
			PublicPath:            "/lg",
			RouterPrefix:          "/",
			Env:                   "production",
			TrustForwardedHeaders: true,
		},
		Security: SecurityConfig{
			DiagSessionTTLSeconds: 1800,
		},
		Sub2API: Sub2APIConfig{
			SettingsPath:            "/api/v1/admin/settings",
			UserInfoPath:            "/api/v1/user",
			EndpointCacheTTLSeconds: 60,
		},
		Probe: ProbeConfig{
			BrowserRepeat:      5,
			BrowserTimeoutMS:   8000,
			ServerProbeEnabled: true,
			ServerRepeat:       3,
			ServerTimeoutMS:    5000,
			Paths: ProbePaths{
				Ping:   "/diag/ping",
				Blob:   "/diag/blob",
				Upload: "/diag/upload",
				Stream: "/diag/stream",
			},
			BlobSizes:   []string{"64k", "1m"},
			MaxBlobSize: "1m",
			Stream: StreamSpec{
				Events:     20,
				IntervalMS: 200,
				Bytes:      32,
			},
		},
		Storage: StorageConfig{
			Driver: "sqlite",
			DSN:    "data/sub2api-origin-lg.db",
		},
	}
}

func (c *Config) Normalize() error {
	var err error
	c.App.PublicPath, err = ValidatePublicPath(c.App.PublicPath)
	if err != nil {
		return fmt.Errorf("app.public_path: %w", err)
	}
	c.App.RouterPrefix, err = normalizePrefix(c.App.RouterPrefix)
	if err != nil {
		return fmt.Errorf("app.router_prefix: %w", err)
	}
	if c.App.Listen == "" {
		c.App.Listen = "0.0.0.0:8080"
	}
	if c.App.Env == "" {
		c.App.Env = "production"
	}
	if c.Security.DiagSessionTTLSeconds <= 0 {
		c.Security.DiagSessionTTLSeconds = 1800
	}
	c.Security.SessionTTL = time.Duration(c.Security.DiagSessionTTLSeconds) * time.Second
	if c.Sub2API.EndpointCacheTTLSeconds <= 0 {
		c.Sub2API.EndpointCacheTTLSeconds = 60
	}
	if c.Sub2API.SettingsPath == "" {
		c.Sub2API.SettingsPath = "/api/v1/admin/settings"
	}
	if c.Probe.BrowserRepeat <= 0 {
		c.Probe.BrowserRepeat = 5
	}
	if c.Probe.BrowserTimeoutMS <= 0 {
		c.Probe.BrowserTimeoutMS = 8000
	}
	if c.Probe.ServerRepeat <= 0 {
		c.Probe.ServerRepeat = 3
	}
	if c.Probe.ServerTimeoutMS <= 0 {
		c.Probe.ServerTimeoutMS = 5000
	}
	c.Probe.Paths.Ping, err = ValidatePublicPath(defaultString(c.Probe.Paths.Ping, "/diag/ping"))
	if err != nil {
		return fmt.Errorf("probe.paths.ping: %w", err)
	}
	c.Probe.Paths.Blob, err = ValidatePublicPath(defaultString(c.Probe.Paths.Blob, "/diag/blob"))
	if err != nil {
		return fmt.Errorf("probe.paths.blob: %w", err)
	}
	c.Probe.Paths.Upload, err = ValidatePublicPath(defaultString(c.Probe.Paths.Upload, "/diag/upload"))
	if err != nil {
		return fmt.Errorf("probe.paths.upload: %w", err)
	}
	c.Probe.Paths.Stream, err = ValidatePublicPath(defaultString(c.Probe.Paths.Stream, "/diag/stream"))
	if err != nil {
		return fmt.Errorf("probe.paths.stream: %w", err)
	}
	if len(c.Probe.BlobSizes) == 0 {
		c.Probe.BlobSizes = []string{"64k", "1m"}
	}
	if c.Probe.Stream.Events <= 0 {
		c.Probe.Stream.Events = 20
	}
	if c.Probe.Stream.IntervalMS <= 0 {
		c.Probe.Stream.IntervalMS = 200
	}
	if c.Probe.Stream.Bytes <= 0 {
		c.Probe.Stream.Bytes = 32
	}
	if c.Storage.DSN == "" {
		c.Storage.DSN = "data/sub2api-origin-lg.db"
	}
	return nil
}

func ValidatePublicPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("must not be empty")
	}
	if !strings.HasPrefix(value, "/") {
		return "", errors.New("must start with /")
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("must be a path without scheme, host, query or fragment")
	}
	if strings.Contains(u.Path, "..") {
		return "", errors.New("must not contain path traversal")
	}
	cleaned := path.Clean(u.Path)
	if cleaned == "." {
		cleaned = "/"
	}
	return cleaned, nil
}

func normalizePrefix(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "/", nil
	}
	return ValidatePublicPath(value)
}

func applyEnv(c *Config) {
	setString(&c.App.Listen, "APP_LISTEN")
	setString(&c.App.PublicPath, "APP_PUBLIC_PATH")
	setString(&c.App.RouterPrefix, "APP_ROUTER_PREFIX")
	setString(&c.App.PublicURL, "APP_PUBLIC_URL")
	setString(&c.App.Env, "APP_ENV")
	setBool(&c.App.TrustForwardedHeaders, "APP_TRUST_FORWARDED_HEADERS")

	setCSV(&c.Security.AllowedParentOrigins, "ALLOWED_PARENT_ORIGINS")
	setCSV(&c.Security.AllowedSrcHosts, "ALLOWED_SRC_HOSTS")
	setString(&c.Security.DiagSessionSecret, "DIAG_SESSION_SECRET")
	setInt(&c.Security.DiagSessionTTLSeconds, "DIAG_SESSION_TTL_SECONDS")
	setBool(&c.Security.AllowHTTPEndpoints, "ALLOW_HTTP_ENDPOINTS")
	setBool(&c.Security.AllowPrivateEndpoints, "ALLOW_PRIVATE_ENDPOINTS")

	setString(&c.Sub2API.AdminBaseURL, "SUB2API_ADMIN_BASE_URL")
	setString(&c.Sub2API.AdminAPIKey, "SUB2API_ADMIN_API_KEY")
	setString(&c.Sub2API.SettingsPath, "SUB2API_SETTINGS_PATH")
	setString(&c.Sub2API.UserInfoPath, "SUB2API_USERINFO_PATH")
	setInt(&c.Sub2API.EndpointCacheTTLSeconds, "SUB2API_ENDPOINT_CACHE_TTL_SECONDS")

	setInt(&c.Probe.BrowserRepeat, "BROWSER_PROBE_REPEAT")
	setInt(&c.Probe.BrowserTimeoutMS, "BROWSER_PROBE_TIMEOUT_MS")
	setBool(&c.Probe.ServerProbeEnabled, "SERVER_PROBE_ENABLED")
	setInt(&c.Probe.ServerRepeat, "SERVER_PROBE_REPEAT")
	setInt(&c.Probe.ServerTimeoutMS, "SERVER_PROBE_TIMEOUT_MS")
	setBool(&c.Probe.EnableICMP, "ENABLE_ICMP")
	setString(&c.Probe.Paths.Ping, "PROBE_PATH_PING")
	setString(&c.Probe.Paths.Blob, "PROBE_PATH_BLOB")
	setString(&c.Probe.Paths.Upload, "PROBE_PATH_UPLOAD")
	setString(&c.Probe.Paths.Stream, "PROBE_PATH_STREAM")
	setCSV(&c.Probe.BlobSizes, "PROBE_BLOB_SIZES")
	setString(&c.Probe.MaxBlobSize, "PROBE_MAX_BLOB_SIZE")
	setBool(&c.Probe.Allow5MBlob, "PROBE_ALLOW_5M_BLOB")
	setInt(&c.Probe.Stream.Events, "PROBE_STREAM_EVENTS")
	setInt(&c.Probe.Stream.IntervalMS, "PROBE_STREAM_INTERVAL_MS")
	setInt(&c.Probe.Stream.Bytes, "PROBE_STREAM_BYTES")

	setString(&c.Storage.Driver, "DB_DRIVER")
	setString(&c.Storage.DSN, "SQLITE_DSN")
}

func setString(dst *string, name string) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		*dst = v
	}
}

func setBool(dst *bool, name string) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			*dst = parsed
		}
	}
}

func setInt(dst *int, name string) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		parsed, err := strconv.Atoi(v)
		if err == nil {
			*dst = parsed
		}
	}
}

func setCSV(dst *[]string, name string) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		*dst = out
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
