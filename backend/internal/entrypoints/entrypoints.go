package entrypoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/urlx"
)

type AdminClient interface {
	GetSettings(ctx context.Context) (*adminclient.SystemSettings, error)
}

type EntryPoint struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Name        string `json:"name"`
	Description string `json:"description"`
	RawValue    string `json:"raw_value"`
	BaseURL     string `json:"base_url"`
	PublicPath  string `json:"public_path"`
	LGBaseURL   string `json:"lg_base_url"`
	Origin      string `json:"origin"`
	Host        string `json:"host"`
	Scheme      string `json:"scheme"`
	Enabled     bool   `json:"enabled"`
}

type Snapshot struct {
	FetchedAt       time.Time    `json:"fetched_at"`
	ExpiresAt       time.Time    `json:"expires_at"`
	Source          string       `json:"source"`
	EntryPointCount int          `json:"entrypoint_count"`
	PublicPath      string       `json:"public_path"`
	Entrypoints     []EntryPoint `json:"entrypoints"`
	Warning         string       `json:"warning,omitempty"`
}

type Cache struct {
	cfg    *config.Config
	admin  AdminClient
	mu     sync.Mutex
	cached *Snapshot
}

func NewCache(cfg *config.Config, admin AdminClient) *Cache {
	return &Cache{cfg: cfg, admin: admin}
}

func (c *Cache) Get(ctx context.Context, refresh bool) (*Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if !refresh && c.cached != nil && now.Before(c.cached.ExpiresAt) {
		copy := *c.cached
		copy.Source = "cache"
		return &copy, nil
	}

	snapshot, err := c.fetch(ctx, now)
	if err == nil {
		c.cached = snapshot
		return snapshot, nil
	}
	if c.cached != nil {
		copy := *c.cached
		copy.Source = "cache"
		copy.Warning = err.Error()
		return &copy, nil
	}
	fallback, fallbackErr := c.fallback(now, err)
	if fallbackErr != nil {
		return nil, errors.Join(err, fallbackErr)
	}
	c.cached = fallback
	return fallback, nil
}

func (c *Cache) fetch(ctx context.Context, now time.Time) (*Snapshot, error) {
	settings, err := c.admin.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	items := collectSettings(settings)
	entrypoints := Normalize(items, c.cfg)
	if len(entrypoints) == 0 {
		return nil, errors.New("admin api returned no valid base_url")
	}
	return c.snapshot(now, "admin_api", entrypoints, ""), nil
}

func (c *Cache) fallback(now time.Time, cause error) (*Snapshot, error) {
	items := make([]RawEndpoint, 0, len(c.cfg.Fallback.StaticEndpoints))
	for i, item := range c.cfg.Fallback.StaticEndpoints {
		items = append(items, RawEndpoint{
			Source:      "static_fallback",
			Name:        item.Name,
			Description: item.Description,
			RawValue:    item.BaseURL,
			Index:       i + 1,
		})
	}
	entrypoints := Normalize(items, c.cfg)
	if len(entrypoints) == 0 {
		return nil, errors.New("no static fallback endpoints")
	}
	return c.snapshot(now, "static_fallback", entrypoints, cause.Error()), nil
}

func (c *Cache) snapshot(now time.Time, source string, entrypoints []EntryPoint, warning string) *Snapshot {
	ttl := time.Duration(c.cfg.Sub2API.EndpointCacheTTLSeconds) * time.Second
	return &Snapshot{
		FetchedAt:       now,
		ExpiresAt:       now.Add(ttl),
		Source:          source,
		EntryPointCount: len(entrypoints),
		PublicPath:      c.cfg.App.PublicPath,
		Entrypoints:     entrypoints,
		Warning:         warning,
	}
}

type RawEndpoint struct {
	Source      string
	Name        string
	Description string
	RawValue    string
	Index       int
}

func collectSettings(settings *adminclient.SystemSettings) []RawEndpoint {
	if settings == nil {
		return nil
	}
	out := make([]RawEndpoint, 0, 1+len(settings.CustomEndpoints)+len(settings.CustomCamel))
	defaultURL := firstNonEmpty(settings.BaseURL, settings.BaseURLCamel, settings.APIBaseURL, settings.APIBaseURLCamel)
	if defaultURL != "" {
		out = append(out, RawEndpoint{
			Source:      "admin_default",
			Name:        "默认入口",
			Description: "default base_url from admin settings",
			RawValue:    defaultURL,
			Index:       1,
		})
	}
	addCustom := func(items []adminclient.CustomEndpoint) {
		for _, item := range items {
			raw := firstNonEmpty(item.BaseURL, item.BaseURLCamel, item.Endpoint)
			if raw == "" {
				continue
			}
			out = append(out, RawEndpoint{
				Source:      "admin_custom",
				Name:        item.Name,
				Description: item.Description,
				RawValue:    raw,
				Index:       len(out) + 1,
			})
		}
	}
	addCustom(settings.CustomEndpoints)
	addCustom(settings.CustomCamel)
	return out
}

func Normalize(raw []RawEndpoint, cfg *config.Config) []EntryPoint {
	seen := map[string]bool{}
	out := make([]EntryPoint, 0, len(raw))
	for _, item := range raw {
		parsed, canonical, err := urlx.CanonicalBase(item.RawValue)
		if err != nil || !allowedScheme(parsed, cfg) || !allowedHost(parsed, cfg) {
			continue
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		lgBaseURL, err := urlx.Join(canonical, cfg.App.PublicPath)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = parsed.Hostname()
		}
		if name == "" {
			name = fmt.Sprintf("入口 %d", item.Index)
		}
		id := endpointID(canonical)
		out = append(out, EntryPoint{
			ID:          id,
			Source:      item.Source,
			Name:        name,
			Description: item.Description,
			RawValue:    item.RawValue,
			BaseURL:     canonical,
			PublicPath:  cfg.App.PublicPath,
			LGBaseURL:   lgBaseURL,
			Origin:      urlx.Origin(parsed),
			Host:        parsed.Host,
			Scheme:      parsed.Scheme,
			Enabled:     true,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func MarshalSnapshot(snapshot *Snapshot) string {
	b, _ := json.Marshal(snapshot)
	return string(b)
}

func allowedScheme(u *url.URL, cfg *config.Config) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if cfg.App.Env == "production" && u.Scheme == "http" && !cfg.Security.AllowHTTPEndpoints {
		return false
	}
	return true
}

func allowedHost(u *url.URL, cfg *config.Config) bool {
	if cfg.Security.AllowPrivateEndpoints {
		return true
	}
	return !urlx.IsPrivateHost(u.Hostname())
}

func endpointID(baseURL string) string {
	sum := sha256.Sum256([]byte(baseURL))
	return hex.EncodeToString(sum[:])[:12]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
