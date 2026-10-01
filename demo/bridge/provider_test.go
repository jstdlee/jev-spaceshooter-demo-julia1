package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func testProviderServer() *Server {
	return &Server{cfg: Config{DjevURL: "http://127.0.0.1:8011", DjevModel: "julia-1", DjevFlavor: "julia", DjevAPIKey: "server-secret"}}
}

func TestProviderDefaultsAndPresetsNeverExposeKey(t *testing.T) {
	s := testProviderServer()
	rec := httptest.NewRecorder()
	s.serveProvider(rec)
	body := rec.Body.String()
	if strings.Contains(body, "server-secret") {
		t.Fatalf("key leaked: %s", body)
	}
	for _, want := range []string{`"preset":"julia"`, `"has_server_key":true`, `"id":"laya"`, `"flavor":"djev"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestParseProviderValidatesAndScopesServerKey(t *testing.T) {
	s := testProviderServer()
	if p, chosen, err := s.parseProvider(nil); err != nil || chosen || p.Model != "julia-1" || p.APIKey != "server-secret" {
		t.Fatalf("default: %+v %v %v", p, chosen, err)
	}
	own, _, err := s.parseProvider(map[string]any{"url": "http://127.0.0.1:8011/", "model": "julia-1", "flavor": "julia"})
	if err != nil || own.APIKey != "server-secret" || own.URL != "http://127.0.0.1:8011" {
		t.Fatalf("own endpoint should reuse server key: %+v %v", own, err)
	}
	other, _, err := s.parseProvider(map[string]any{"preset": "laya", "url": "http://127.0.0.1:8012", "model": "typed-decisions", "flavor": "laya"})
	if err != nil || other.APIKey != "" {
		t.Fatalf("server key must not go to another host: %+v %v", other, err)
	}
	custom, _, err := s.parseProvider(map[string]any{"url": "https://api.example.com", "model": "m", "flavor": "djev", "api_key": "user-key"})
	if err != nil || custom.APIKey != "user-key" || custom.Preset != "custom" {
		t.Fatalf("custom: %+v %v", custom, err)
	}
	for name, bad := range map[string]map[string]any{
		"relative":    {"url": "/v1", "model": "m", "flavor": "julia"},
		"credentials": {"url": "http://u:p@h", "model": "m", "flavor": "julia"},
		"query":       {"url": "http://h/?a=1", "model": "m", "flavor": "julia"},
		"full path":   {"url": "http://h/v1/systemone", "model": "m", "flavor": "julia"},
		"no model":    {"url": "http://h", "model": "", "flavor": "julia"},
		"flavor":      {"url": "http://h", "model": "m", "flavor": "gpt"},
		"newline key": {"url": "http://h", "model": "m", "flavor": "julia", "api_key": "a\nb"},
	} {
		if _, _, err := s.parseProvider(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPayloadFlavors(t *testing.T) {
	path, bomb := NewOrderedMap().Set("a", "A"), NewOrderedMap().Set("hold", "H")
	for flavor, want := range map[string][2]bool{"julia": {true, true}, "djev": {false, true}, "laya": {false, false}} {
		wire, _ := marshalCompact(buildUpstreamPayload("m", "p", NewOrderedMap(), path, bomb, nil, flavor))
		text := string(wire)
		if strings.Contains(text, "option_questions") != want[0] || strings.Contains(text, `"samples":1`) != want[1] {
			t.Errorf("%s: %s", flavor, text)
		}
	}
}
