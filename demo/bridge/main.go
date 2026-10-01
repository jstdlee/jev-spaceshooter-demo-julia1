// Command bridge serves the space shooter page and relays structured decisions to Djev.
//
//	go run ./demo/bridge --host 127.0.0.1 --port 7865
//	go run ./demo/bridge validate-events '<event batch JSON>'
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxBodyBytes = 4 * 1024 * 1024

// loadEnvFile sets keys from a .env file without overriding the process environment.
func loadEnvFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, strings.TrimSpace(value))
		}
	}
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// findDemoDir accepts running from the repository root or from demo/.
func findDemoDir() string {
	for _, candidate := range []string{"demo", "."} {
		if _, err := os.Stat(filepath.Join(candidate, "space-shooter.html")); err == nil {
			return candidate
		}
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(filepath.Dir(exe))
	}
	return "demo"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := marshalCompact(value)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"schema_version":1,"error":"response encoding failed","code":"internal_error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	w.Write(body)
}

func writeError(w http.ResponseWriter, err error) {
	var apiErr *ApiError
	if errors.As(err, &apiErr) {
		writeJSON(w, apiErr.Status, map[string]any{"schema_version": SchemaVersion, "error": apiErr.Message, "code": apiErr.Code})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"schema_version": SchemaVersion, "error": err.Error(), "code": "internal_error"})
}

func readJSON(r *http.Request) (any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, validationError("could not read request body")
	}
	if len(raw) == 0 {
		return nil, validationError("request body is required")
	}
	if len(raw) > maxBodyBytes {
		return nil, validationError("request body is too large")
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, validationError("invalid JSON: %v", err)
	}
	return value, nil
}

func (s *Server) Handler() http.Handler {
	routes := map[string]func(*http.Request, any) (map[string]any, error){
		"/api/run/start": func(_ *http.Request, body any) (map[string]any, error) { return s.StartRun(body) },
		"/api/decision":  func(r *http.Request, body any) (map[string]any, error) { return s.HandleDecision(r.Context(), body) },
		"/api/run/event": func(_ *http.Request, body any) (map[string]any, error) { return s.RecordRunEvents(body) },
		"/api/run/end":   func(_ *http.Request, body any) (map[string]any, error) { return s.EndRun(body) },
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			switch r.URL.Path {
			case "/", "/space-shooter.html":
				page, err := os.ReadFile(s.cfg.HTMLPath)
				if err != nil {
					writeError(w, err)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Write(page)
			case "/api/provider":
				s.serveProvider(w)
			case "/api/exchange":
				s.serveExchange(w, r)
			case "/health":
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"ok":true}`))
			default:
				writeJSON(w, http.StatusNotFound, map[string]any{"schema_version": SchemaVersion, "error": "not found"})
			}
		case http.MethodPost:
			handler, ok := routes[r.URL.Path]
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"schema_version": SchemaVersion, "error": "not found"})
				return
			}
			body, err := readJSON(r)
			if err == nil {
				var result map[string]any
				if result, err = handler(r, body); err == nil {
					writeJSON(w, http.StatusOK, result)
					return
				}
			}
			writeError(w, err)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"schema_version": SchemaVersion, "error": "method not allowed"})
		}
	})
}

// validateEventsCommand lets the JS strategy harness check event batches against the real validator.
func validateEventsCommand(raw string) int {
	body, err := decodeJSON([]byte(raw))
	if err == nil {
		_, err = ValidateEventBatch(body)
	}
	out := map[string]any{"ok": err == nil}
	if err != nil {
		out["error"] = err.Error()
	}
	encoded, _ := marshalCompact(out)
	fmt.Println(string(encoded))
	return 0
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "validate-events" {
		os.Exit(validateEventsCommand(os.Args[2]))
	}
	demoDir := flag.String("demo-dir", findDemoDir(), "directory containing space-shooter.html and strategy.md")
	host := flag.String("host", envOr("SHOOTER_HOST", "127.0.0.1"), "listen host")
	port := flag.Int("port", func() int { p, _ := strconv.Atoi(envOr("SHOOTER_PORT", "7862")); return p }(), "listen port")
	runsDir := flag.String("runs-dir", "", "trace directory (default <demo-dir>/runs)")
	upstreamMode := flag.String("upstream", "djev", "djev, or oracle for offline label simulation (no model call)")
	var mounts mountFlags
	flag.Var(&mounts, "mount", "serve another demo directory under a path prefix, e.g. /v2=../v2/demo (repeatable)")
	flag.Parse()

	loadEnvFile(filepath.Join(*demoDir, "..", ".env"))
	djevURL := strings.TrimRight(envOr("DJEV_URL", "http://127.0.0.1:8011"), "/")
	apiKey := envOr("DJEV_API_KEY", os.Getenv("API_KEY"))
	cfg := Config{
		HTMLPath:     filepath.Join(*demoDir, "space-shooter.html"),
		StrategyPath: filepath.Join(*demoDir, "strategy.md"),
		RunsDir:      *runsDir,
		DjevURL:      djevURL,
		DjevModel:    envOr("DJEV_MODEL", "jev-latest"),
		DjevFlavor:   defaultFlavor(),
		DjevAPIKey:   apiKey,
	}
	if cfg.RunsDir == "" {
		cfg.RunsDir = filepath.Join(*demoDir, "runs")
	}
	var upstream Upstream = newHTTPUpstream(djevURL, apiKey)
	switch *upstreamMode {
	case "djev":
	case "oracle":
		upstream = oracleUpstream{}
		cfg.DjevModel = "oracle"
	default:
		log.Fatalf("unknown --upstream %q", *upstreamMode)
	}
	server := NewServer(cfg, upstream)
	address := net.JoinHostPort(*host, strconv.Itoa(*port))
	fmt.Printf("Space shooter: http://%s/\n", address)
	handler := server.Handler()
	var mounted []mountedServer
	for _, m := range mounts {
		mcfg := cfg
		mcfg.HTMLPath = filepath.Join(m.dir, "space-shooter.html")
		mcfg.StrategyPath = filepath.Join(m.dir, "strategy.md")
		mcfg.RunsDir = filepath.Join(m.dir, "runs")
		mounted = append(mounted, mountedServer{prefix: m.prefix, handler: NewServer(mcfg, upstream).Handler()})
		fmt.Printf("Space shooter: http://%s%s/ (%s)\n", address, m.prefix, m.dir)
	}
	log.Fatal(http.ListenAndServe(address, mountHandler(handler, mounted)))
}

// mountFlags collects --mount prefix=demo-dir values.
type mountFlags []struct{ prefix, dir string }

func (m *mountFlags) String() string { return fmt.Sprint(*m) }

func (m *mountFlags) Set(value string) error {
	prefix, dir, ok := strings.Cut(value, "=")
	prefix = "/" + strings.Trim(prefix, "/")
	if !ok || prefix == "/" || dir == "" {
		return fmt.Errorf("--mount wants /prefix=demo-dir, got %q", value)
	}
	*m = append(*m, struct{ prefix, dir string }{prefix, dir})
	return nil
}

type mountedServer struct {
	prefix  string
	handler http.Handler
}

// mountHandler serves each mounted demo, with its own engine, trace directory and validation, under its prefix:
// /v2 redirects to /v2/, and /v2/api/decision reaches that demo's /api/decision. The page calls relative routes.
func mountHandler(root http.Handler, mounts []mountedServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, m := range mounts {
			if r.URL.Path == m.prefix {
				target := m.prefix + "/"
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusMovedPermanently)
				return
			}
			if strings.HasPrefix(r.URL.Path, m.prefix+"/") {
				inner := r.Clone(r.Context())
				inner.URL.Path = strings.TrimPrefix(r.URL.Path, m.prefix)
				inner.URL.RawPath = ""
				m.handler.ServeHTTP(w, inner)
				return
			}
		}
		root.ServeHTTP(w, r)
	})
}
