package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Provider is the upstream /v1/systemone endpoint a run talks to. The API key never
// leaves the bridge: it is not returned to the page, logged, or written to traces.
type Provider struct {
	Preset string
	URL    string // base URL; the bridge appends /v1/systemone
	Model  string
	Flavor string // request format the endpoint accepts
	APIKey string
}

// Request flavors differ only in the optional fields an endpoint accepts.
//   - julia: per-option judgements (option_questions) plus samples/steps = 1
//   - djev:  samples/steps = 1, no option_questions
//   - laya:  neither (laya-api rejects both)
var providerFlavors = map[string]bool{"julia": true, "djev": true, "laya": true}

type providerPreset struct {
	ID, Label, URL, Model, Flavor string
}

var providerPresets = []providerPreset{
	{"julia", "Local Julia 1", "http://127.0.0.1:8011", "julia-1", "julia"},
	{"laya", "Local Laya", "http://127.0.0.1:8012", "typed-decisions", "laya"},
	{"djev", "djev-spark", "http://127.0.0.1:8000", "jev-latest", "djev"},
}

func defaultFlavor() string {
	if flavor := strings.TrimSpace(os.Getenv("DJEV_FLAVOR")); providerFlavors[flavor] {
		return flavor
	}
	return "julia"
}

// defaultProvider is the server's own configuration (.env / flags).
func (s *Server) defaultProvider() Provider {
	preset := "custom"
	for _, p := range providerPresets {
		if sameEndpoint(p.URL, s.cfg.DjevURL) && p.Model == s.cfg.DjevModel {
			preset = p.ID
		}
	}
	return Provider{Preset: preset, URL: s.cfg.DjevURL, Model: s.cfg.DjevModel, Flavor: s.cfg.DjevFlavor, APIKey: s.cfg.DjevAPIKey}
}

func sameEndpoint(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// parseProvider validates an optional run-start "provider" object. A missing object
// means the server default. A blank api_key reuses the server key only for the
// server's own endpoint, so that key is never sent to a host the page chose.
func (s *Server) parseProvider(value any) (Provider, bool, error) {
	if value == nil {
		return s.defaultProvider(), false, nil
	}
	object, err := requireObject(value, "provider")
	if err != nil {
		return Provider{}, false, err
	}
	text := func(key string, limit int) (string, error) {
		raw, present := object[key]
		if !present || raw == nil {
			return "", nil
		}
		value, ok := raw.(string)
		if !ok || len(value) > limit || strings.ContainsAny(value, "\r\n") {
			return "", validationError("provider.%s must be a single-line string of at most %d characters", key, limit)
		}
		return strings.TrimSpace(value), nil
	}
	preset, err := text("preset", 32)
	if err != nil {
		return Provider{}, false, err
	}
	rawURL, err := text("url", 512)
	if err != nil {
		return Provider{}, false, err
	}
	model, err := text("model", 128)
	if err != nil {
		return Provider{}, false, err
	}
	flavor, err := text("flavor", 16)
	if err != nil {
		return Provider{}, false, err
	}
	key, err := text("api_key", 1024)
	if err != nil {
		return Provider{}, false, err
	}
	if err := validateProviderURL(rawURL); err != nil {
		return Provider{}, false, err
	}
	if model == "" {
		return Provider{}, false, validationError("provider.model is required")
	}
	if !providerFlavors[flavor] {
		return Provider{}, false, validationError("provider.flavor must be julia, djev or laya")
	}
	if preset == "" {
		preset = "custom"
	}
	rawURL = strings.TrimRight(rawURL, "/")
	if key == "" && sameEndpoint(rawURL, s.cfg.DjevURL) {
		key = s.cfg.DjevAPIKey
	}
	return Provider{Preset: preset, URL: rawURL, Model: model, Flavor: flavor, APIKey: key}, true, nil
}

func validateProviderURL(raw string) error {
	parsed, err := url.Parse(raw)
	if raw == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return validationError("provider.url must be an absolute http(s) base URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return validationError("provider.url must not contain credentials, a query or a fragment")
	}
	if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v1/systemone") {
		return validationError("provider.url is a base URL; the bridge appends /v1/systemone")
	}
	return nil
}

// serveProvider handles GET /api/provider: the effective default and the presets, never a key.
func (s *Server) serveProvider(w http.ResponseWriter) {
	current := s.defaultProvider()
	presets := make([]map[string]any, 0, len(providerPresets))
	for _, p := range providerPresets {
		presets = append(presets, map[string]any{"id": p.ID, "label": p.Label, "url": p.URL, "model": p.Model, "flavor": p.Flavor})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": SchemaVersion,
		"oracle":         s.cfg.DjevModel == "oracle",
		"default": map[string]any{
			"preset": current.Preset, "url": safeEndpoint(current.URL), "model": current.Model,
			"flavor": current.Flavor, "has_server_key": current.APIKey != "",
		},
		"presets": presets,
	})
}
