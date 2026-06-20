package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sub2api-origin-lg/backend/internal/config"
	"sub2api-origin-lg/backend/internal/urlx"
)

type Client struct {
	cfg    *config.Config
	client *http.Client
}

type SystemSettings struct {
	APIBaseURL      string           `json:"api_base_url"`
	APIBaseURLCamel string           `json:"apiBaseUrl"`
	BaseURL         string           `json:"base_url"`
	BaseURLCamel    string           `json:"baseUrl"`
	CustomEndpoints []CustomEndpoint `json:"custom_endpoints"`
	CustomCamel     []CustomEndpoint `json:"customEndpoints"`
}

type CustomEndpoint struct {
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint"`
	BaseURL      string `json:"base_url"`
	BaseURLCamel string `json:"baseUrl"`
	Description  string `json:"description"`
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role,omitempty"`
	IsAdmin  bool   `json:"is_admin,omitempty"`
}

func New(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		client: &http.Client{
			Timeout: 8 * time.Second,
		},
	}
}

func (c *Client) GetSettings(ctx context.Context) (*SystemSettings, error) {
	if strings.TrimSpace(c.cfg.Sub2API.AdminBaseURL) == "" || strings.TrimSpace(c.cfg.Sub2API.AdminAPIKey) == "" {
		return nil, errors.New("sub2api admin_base_url or admin_api_key is empty")
	}
	endpoint, err := urlx.Join(c.cfg.Sub2API.AdminBaseURL, c.cfg.Sub2API.SettingsPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.cfg.Sub2API.AdminAPIKey)

	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("admin settings status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	var wrapped struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.Data) > 0 {
		var settings SystemSettings
		if err := json.Unmarshal(wrapped.Data, &settings); err != nil {
			return nil, err
		}
		return &settings, nil
	}

	var settings SystemSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

func (c *Client) VerifyUser(ctx context.Context, token string) (*User, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("token is empty")
	}
	if strings.TrimSpace(c.cfg.Sub2API.AdminBaseURL) == "" || strings.TrimSpace(c.cfg.Sub2API.UserInfoPath) == "" {
		return nil, errors.New("sub2api user verification is not configured")
	}
	endpoint, err := urlx.Join(c.cfg.Sub2API.AdminBaseURL, c.cfg.Sub2API.UserInfoPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	user, err := parseUser(body)
	if err != nil {
		return nil, err
	}
	if user.ID == "" {
		return nil, errors.New("userinfo response missing user id")
	}
	return user, nil
}

func parseUser(body []byte) (*User, error) {
	var wrapped struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.Data) > 0 {
		return parseUserObject(wrapped.Data)
	}
	return parseUserObject(body)
}

func parseUserObject(body []byte) (*User, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if nested, ok := raw["user"]; ok {
		if nestedRaw, err := json.Marshal(nested); err == nil {
			return parseUserObject(nestedRaw)
		}
	}
	user := &User{
		ID:       stringValue(raw, "id", "user_id", "userId"),
		Username: stringValue(raw, "username", "name"),
		Email:    maskEmail(stringValue(raw, "email")),
		Role:     strings.ToLower(strings.TrimSpace(stringValue(raw, "role"))),
	}
	user.IsAdmin = user.Role == "admin"
	return user, nil
}

func stringValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch typed := v.(type) {
			case string:
				return typed
			case float64:
				return fmt.Sprintf("%.0f", typed)
			}
		}
	}
	return ""
}

func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" {
		return ""
	}
	return parts[0][:1] + "***@" + parts[1]
}
