package entrypoints

import (
	"testing"

	"sub2api-origin-lg/backend/internal/adminclient"
	"sub2api-origin-lg/backend/internal/config"
)

func TestNormalizeUsesBaseURLAndPublicPath(t *testing.T) {
	cfg := config.Default()
	cfg.App.PublicPath = "/tools/lg"
	cfg.App.Env = "development"
	raw := []RawEndpoint{
		{Source: "admin_default", Name: "默认入口", RawValue: "https://api.example.com/v1/"},
		{Source: "admin_custom", Name: "duplicate", RawValue: "https://api.example.com/v1"},
		{Source: "admin_custom", Name: "CDN", RawValue: "https://cdn.example.com/v1"},
	}
	got := Normalize(raw, cfg)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, ep := range got {
		if ep.BaseURL == "" || ep.LGBaseURL == "" {
			t.Fatalf("missing base_url/lg_base_url: %+v", ep)
		}
		if ep.PublicPath != "/tools/lg" {
			t.Fatalf("public_path = %q", ep.PublicPath)
		}
		if ep.LGBaseURL == "https://api.example.com/v1/lg" {
			t.Fatalf("hardcoded default public path: %+v", ep)
		}
	}
}

func TestCollectSettingsKeepsLegacyFieldsInsideAdminLayer(t *testing.T) {
	settings := &adminclient.SystemSettings{
		APIBaseURL: "https://legacy-default.example.com/v1",
		CustomEndpoints: []adminclient.CustomEndpoint{
			{Name: "old endpoint", Endpoint: "https://old-custom.example.com/v1"},
		},
		CustomCamel: []adminclient.CustomEndpoint{
			{Name: "new camel", BaseURLCamel: "https://camel-custom.example.com/v1"},
		},
	}
	cfg := config.Default()
	cfg.App.Env = "development"
	got := Normalize(collectSettings(settings), cfg)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for _, ep := range got {
		if ep.BaseURL == "" {
			t.Fatalf("base_url missing: %+v", ep)
		}
		if ep.LGBaseURL == "" {
			t.Fatalf("lg_base_url missing: %+v", ep)
		}
	}
}
