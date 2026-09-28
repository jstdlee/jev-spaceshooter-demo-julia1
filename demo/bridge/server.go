package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const endRunUpstreamWait = 2500 * time.Millisecond

// Config locates the served page, prompt, trace directory, and upstream identity.
type Config struct {
	HTMLPath     string
	StrategyPath string
	RunsDir      string
	DjevURL      string
	DjevModel    string
	DjevFlavor   string // request flavor for the default provider (see provider.go)
	DjevAPIKey   string // server-side key for the default provider; never sent to the page
}

// RunState is one game's trace and ordering state.
type RunState struct {
	RunID         string
	RunPath       string
	EventsPath    string
	Manifest      map[string]any
	PromptVersion string
	PromptText    string
	PromptHash    string
	ContextVer    string
	EngineHash    string

	upstream  chan struct{} // one in-flight decision; end_run waits on it
	eventMu   sync.Mutex
	traceMu   sync.Mutex
	trace     *os.File
	appendIdx int64

	identityMu    sync.Mutex
	modelIdentity map[string]any

	provider Provider // endpoint, model and flavor for every decision of this run
	client   Upstream // per-run upstream when the page chose a provider; nil uses the server's

	lastEventID     *int64
	eventHashes     map[int64]string
	complete        bool
	decisions       int64
	invalidDecision int64
	eventCount      int64
	terminal        map[string]any
}

func (run *RunState) identity() map[string]any {
	run.identityMu.Lock()
	defer run.identityMu.Unlock()
	out := make(map[string]any, len(run.modelIdentity))
	for key, value := range run.modelIdentity {
		out[key] = value
	}
	return out
}

// Server holds all runs; Upstream is swappable in tests.
type Server struct {
	cfg      Config
	upstream Upstream
	runsMu   sync.Mutex
	runs     map[string]*RunState
	started  time.Time
	// exchanges keeps recent upstream request/response bodies for the event-log detail view.
	exchanges exchangeLog
	// afterUpstreamLock is a test hook run after a decision acquires the run's upstream slot.
	afterUpstreamLock func(*RunState)
}

func NewServer(cfg Config, upstream Upstream) *Server {
	return &Server{cfg: cfg, upstream: upstream, runs: map[string]*RunState{}, started: time.Now()}
}

func utcNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Server) monotonicMs() float64 {
	return round(float64(time.Since(s.started).Nanoseconds())/1e6, 3)
}

func sha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return buf
}

func makeRunID() string {
	token := strings.TrimRight(strings.ReplaceAll(base64.RawURLEncoding.EncodeToString(randomBytes(8)), "_", "-"), "-")
	return fmt.Sprintf("run-%s-%s", time.Now().UTC().Format("20060102T150405Z"), token)
}

func decisionID(runID string, epoch, sequence int64) string {
	return fmt.Sprintf("%s-e%d-s%d-%s", runID, epoch, sequence, hex.EncodeToString(randomBytes(4)))
}

var scriptPattern = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

func extractScript(html, scriptID string) (string, error) {
	idPattern := regexp.MustCompile(`(?i)\bid=["']` + regexp.QuoteMeta(scriptID) + `["']`)
	for _, match := range scriptPattern.FindAllStringSubmatch(html, -1) {
		if idPattern.MatchString(match[1]) {
			return match[2], nil
		}
	}
	return "", validationError("missing inline script '%s'", scriptID)
}

// engineSourceHash matches the benchmark: sha256 of core + "\n" + controller inline scripts.
func (s *Server) engineSourceHash() (string, error) {
	raw, err := os.ReadFile(s.cfg.HTMLPath)
	if err != nil {
		return "", err
	}
	core, err := extractScript(string(raw), "space-decision-core")
	if err != nil {
		return "", err
	}
	controller, err := extractScript(string(raw), "space-djev-controller")
	if err != nil {
		return "", err
	}
	return sha256Text(core + "\n" + controller), nil
}

func (s *Server) loadStrategy(promptVersion string) (string, string, error) {
	if promptVersion != PromptVersion {
		return "", "", validationError("unknown prompt_version '%s'", promptVersion)
	}
	raw, err := os.ReadFile(s.cfg.StrategyPath)
	if err != nil {
		return "", "", err
	}
	firstLine, body, found := strings.Cut(string(raw), "\n")
	if !found || strings.TrimSpace(firstLine) != "version: "+promptVersion {
		return "", "", validationError("strategy.md first line must pin the requested prompt version")
	}
	if strings.TrimSpace(body) == "" {
		return "", "", validationError("strategy prompt body must not be empty")
	}
	if len(strings.Fields(body)) > 180 {
		return "", "", validationError("strategy prompt body must be at most 180 words")
	}
	return body, sha256Text(body), nil
}

func safeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	parsed, err := url.Parse(endpoint)
	if err != nil {
		parts := strings.Split(endpoint, "@")
		return parts[len(parts)-1]
	}
	host := parsed.Hostname()
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: host, Path: strings.TrimRight(parsed.Path, "/")}).String()
}

func (s *Server) getRun(value any) (*RunState, error) {
	runID, ok := value.(string)
	if !ok || runID == "" {
		return nil, validationError("run_id must be a nonempty string")
	}
	s.runsMu.Lock()
	run := s.runs[runID]
	s.runsMu.Unlock()
	if run == nil {
		return nil, notFoundError("unknown run_id '%s'", runID)
	}
	return run, nil
}

// appendRecord writes one canonical JSONL trace line; fsync only for records that must survive a crash.
func (run *RunState) appendRecord(record map[string]any, fsync bool) error {
	run.traceMu.Lock()
	defer run.traceMu.Unlock()
	run.appendIdx++
	full := map[string]any{"schema_version": SchemaVersion, "append_index": run.appendIdx, "recorded_at_utc": utcNow()}
	for key, value := range record {
		full[key] = value
	}
	line, err := canonicalJSON(full)
	if err != nil {
		return err
	}
	if _, err := run.trace.Write(append(line, '\n')); err != nil {
		return err
	}
	if fsync {
		return run.trace.Sync()
	}
	return nil
}

var engineHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateManifest(value any) (map[string]any, error) {
	manifest, err := requireObject(value, "manifest")
	if err != nil {
		return nil, err
	}
	if manifest["prompt_version"] != PromptVersion {
		return nil, validationError("manifest.prompt_version is unknown")
	}
	if manifest["context_version"] != ContextVersion {
		return nil, validationError("manifest.context_version is unknown")
	}
	if mode := manifest["mode"]; mode != "browser" && mode != "cli" {
		return nil, validationError("manifest.mode must be browser or cli")
	}
	dt, err := finiteNumber(manifest["dt_ms"], "manifest.dt_ms", false, min0())
	if err != nil {
		return nil, err
	}
	if *dt-DtMs > 0.001 || DtMs-*dt > 0.001 {
		return nil, validationError("manifest.dt_ms must be 1000/60")
	}
	if hash, ok := manifest["engine_hash"].(string); !ok || !engineHashPattern.MatchString(hash) {
		return nil, validationError("manifest.engine_hash must be a lowercase SHA-256 hex digest")
	}
	if _, ok := manifest["rules"].(map[string]any); !ok {
		return nil, validationError("manifest.rules must be an object")
	}
	return manifest, nil
}

func (s *Server) StartRun(body any) (map[string]any, error) {
	object, err := requireSchema(body)
	if err != nil {
		return nil, err
	}
	manifest, err := validateManifest(object["manifest"])
	if err != nil {
		return nil, err
	}
	provider, chosen, err := s.parseProvider(object["provider"])
	if err != nil {
		return nil, err
	}
	promptText, promptHash, err := s.loadStrategy(manifest["prompt_version"].(string))
	if err != nil {
		return nil, err
	}
	engineHash, err := s.engineSourceHash()
	if err != nil {
		return nil, err
	}
	if manifest["engine_hash"] != engineHash {
		return nil, validationError("manifest.engine_hash does not match current inline engine/controller source")
	}
	var runID, runPath string
	for {
		runID = makeRunID()
		runPath = filepath.Join(s.cfg.RunsDir, runID)
		if err := os.MkdirAll(s.cfg.RunsDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.Mkdir(runPath, 0o755); err == nil {
			break
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	eventsPath := filepath.Join(runPath, "events.jsonl")
	trace, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	run := &RunState{
		RunID: runID, RunPath: runPath, EventsPath: eventsPath, Manifest: manifest,
		PromptVersion: PromptVersion, PromptText: promptText, PromptHash: promptHash,
		ContextVer: ContextVersion, EngineHash: engineHash,
		upstream: make(chan struct{}, 1), trace: trace, eventHashes: map[int64]string{},
		modelIdentity: map[string]any{"configured_model": provider.Model, "endpoint": safeEndpoint(provider.URL), "provider": provider.Preset, "flavor": provider.Flavor, "response_model": nil},
		provider:      provider,
	}
	if chosen && s.cfg.DjevModel != "oracle" {
		run.client = newHTTPUpstream(provider.URL, provider.APIKey)
	}
	rules, _ := manifest["rules"].(map[string]any)
	err = run.appendRecord(map[string]any{
		"record_type":        "run_started",
		"run_id":             runID,
		"status":             "incomplete",
		"manifest":           manifest,
		"engine_source_hash": engineHash,
		"prompt_version":     run.PromptVersion,
		"prompt_hash":        promptHash,
		"prompt_text":        promptText,
		"context_version":    run.ContextVer,
		"model_identity":     run.identity(),
		"prng":               map[string]any{"algorithm": rules["prng_algorithm"], "seed": manifest["seed"], "initial_rng_state": manifest["initial_rng_state"]},
		"server_time_utc":    utcNow(),
	}, true)
	if err != nil {
		trace.Close()
		return nil, err
	}
	s.runsMu.Lock()
	s.runs[runID] = run
	s.runsMu.Unlock()
	absolute, _ := filepath.Abs(eventsPath)
	return map[string]any{"schema_version": SchemaVersion, "run_id": runID, "prompt_version": run.PromptVersion, "prompt_hash": promptHash, "trace_path": absolute}, nil
}

func nullDecision(request *DecisionRequest, id string, apiOK bool, errText string, latencyMs float64) map[string]any {
	return map[string]any{
		"schema_version": SchemaVersion, "run_id": request.RunID, "epoch": request.Epoch, "sequence": request.Sequence,
		"decision_id": id, "intent": nil, "movement": nil, "fire": nil, "lease": nil, "bomb": nil,
		"valid_choice": false, "api_ok": apiOK, "error": errText, "latency_ms": round(latencyMs, 1),
		"usage":      map[string]any{"input_tokens": nil, "output_tokens": nil},
		"confidence": nullConfidence(), "api_token_throughput": nil,
	}
}

func (s *Server) HandleDecision(ctx context.Context, body any) (map[string]any, error) {
	request, err := validateDecisionRequest(body)
	if err != nil {
		return nil, err
	}
	run, err := s.getRun(request.RunID)
	if err != nil {
		return nil, err
	}
	select {
	case run.upstream <- struct{}{}:
	default:
		return nil, conflictError("decision_in_progress", "decision already in progress for this run")
	}
	defer func() { <-run.upstream }()
	if s.afterUpstreamLock != nil {
		s.afterUpstreamLock(run)
	}
	run.eventMu.Lock()
	complete := run.complete
	run.eventMu.Unlock()
	if complete {
		return nil, conflictError("run_complete", "run is already complete")
	}

	serverStart := time.Now()
	id := decisionID(run.RunID, request.Epoch, request.Sequence)
	receivedUTC := utcNow()
	receivedMono := s.monotonicMs()
	tokenBudget := map[string]any{"packed_state_chars": nil, "tokenizer": "unavailable", "max_model_len": MaxModelLen, "reserved_output_tokens": ReservedOutputTokens}

	criteria, bestTier, err := buildPathCriteria(request)
	var packed *OrderedMap
	var bomb BombFacts
	if err == nil {
		packed, err = packModelContext(request)
	}
	if err == nil {
		bomb, err = bombFacts(request.State["bomb"])
	}
	if err != nil {
		var budget *ContextBudgetExceeded
		if errors.As(err, &budget) {
			result := nullDecision(request, id, false, "context_budget_exceeded", 0)
			if werr := run.appendRecord(map[string]any{
				"record_type": "decision_response", "run_id": run.RunID, "decision_id": id,
				"epoch": request.Epoch, "sequence": request.Sequence, "rawsnapshot": request.Raw,
				"exactactualpayload": nil, "exactrequestbody": nil, "rawupstreamresponse": nil,
				"times":      map[string]any{"server_received_utc": receivedUTC, "server_received_monotonic_ms": receivedMono},
				"normalized": result, "error": budget.Message, "token_budget": tokenBudget,
			}, true); werr != nil {
				return nil, werr
			}
			return result, nil
		}
		if isValidationError(err) {
			_ = run.appendRecord(map[string]any{
				"record_type": "decision_validation_failed", "run_id": run.RunID, "decision_id": id,
				"epoch": request.Raw["epoch"], "sequence": request.Raw["sequence"], "rawsnapshot": request.Raw,
				"times": map[string]any{"server_received_utc": receivedUTC, "server_received_monotonic_ms": receivedMono},
			}, true)
		}
		return nil, err
	}

	identity := run.identity()
	bombCriteria := buildBombCriteria(bomb, bestTier)
	payload := buildUpstreamPayload(fmt.Sprint(identity["configured_model"]), run.PromptText, packed, criteria, bombCriteria, run.provider.Flavor)
	// The exact wire bytes are logged before transport and reused for the request itself.
	wire, err := marshalCompact(payload)
	if err != nil {
		return nil, err
	}
	exactBody := string(wire)
	packedJSON, _ := marshalCompact(packed)
	tokenBudget["packed_state_chars"] = len(packedJSON)
	tokenBudget["target_input_tokens"] = MaxModelLen - ReservedOutputTokens
	if err := run.appendRecord(map[string]any{
		"record_type": "decision_request", "run_id": run.RunID, "decision_id": id,
		"epoch": request.Epoch, "sequence": request.Sequence, "snapshot_tick": request.SnapshotTick,
		"rawsnapshot": request.Raw, "exactactualpayload": payload, "exactrequestbody": exactBody,
		"prompt_version": run.PromptVersion, "prompt_hash": run.PromptHash, "context_version": run.ContextVer,
		"model_identity": identity,
		"times": map[string]any{
			"server_received_utc": receivedUTC, "server_received_monotonic_ms": receivedMono,
			"client_send_wall_ms": request.Raw["client_send_wall_ms"],
		},
		"token_budget": tokenBudget,
	}, false); err != nil {
		return nil, err
	}

	upstreamUTC := utcNow()
	upstreamMono := s.monotonicMs()
	upstreamStart := time.Now()
	s.exchanges.add(Exchange{RunID: run.RunID, DecisionID: id, Sequence: request.Sequence,
		Endpoint: safeEndpoint(run.provider.URL) + "/v1/systemone", RecordedAt: upstreamUTC, RequestBody: exactBody, Pending: true})
	client := s.upstream
	if run.client != nil {
		client = run.client
	}
	upstream, callErr := client.Call(ctx, wire)
	if callErr != nil {
		result := nullDecision(request, id, false, callErr.Error(), float64(time.Since(upstreamStart).Nanoseconds())/1e6)
		s.exchanges.complete(run.RunID, request.Sequence, nil, nil, result["error"], round(float64(time.Since(upstreamStart).Nanoseconds())/1e6, 1))
		run.eventMu.Lock()
		run.invalidDecision++
		run.eventMu.Unlock()
		if err := run.appendRecord(map[string]any{
			"record_type": "decision_response", "run_id": run.RunID, "decision_id": id,
			"epoch": request.Epoch, "sequence": request.Sequence, "rawsnapshot": request.Raw,
			"exactactualpayload": payload, "exactrequestbody": exactBody,
			"rawupstreamresponse": map[string]any{"status": nil, "body": nil, "parsed": nil, "error": result["error"]},
			"times": map[string]any{
				"server_received_utc": receivedUTC, "upstream_started_utc": upstreamUTC,
				"upstream_started_monotonic_ms": upstreamMono, "latency_ms": result["latency_ms"],
				"bridge_total_ms": round(float64(time.Since(serverStart).Nanoseconds())/1e6, 1),
			},
			"normalized": result, "model_identity": run.identity(),
		}, true); err != nil {
			return nil, err
		}
		return result, nil
	}

	apiOK := upstream.Status != nil && *upstream.Status >= 200 && *upstream.Status < 300 && upstream.Error == nil
	var normalized map[string]any
	if apiOK {
		normalized = normalizeDecisionResponse(upstream.Parsed)
	} else {
		errText := ""
		if upstream.Error != nil {
			errText = *upstream.Error
		} else if upstream.Status != nil {
			errText = fmt.Sprintf("upstream_status_%d", *upstream.Status)
		}
		normalized = map[string]any{"intent": nil, "movement": nil, "fire": nil, "lease": nil, "bomb": nil, "valid_choice": false, "confidence": nullConfidence(), "error": errText}
	}
	if parsed, ok := upstream.Parsed.(map[string]any); ok {
		if model, ok := parsed["model"].(string); ok {
			run.identityMu.Lock()
			run.modelIdentity["response_model"] = model
			run.identityMu.Unlock()
		}
	}
	usage := upstream.Usage
	if usage == nil {
		usage = map[string]any{"input_tokens": nil, "output_tokens": nil}
	}
	throughput := tokenThroughput(usage, upstream.ElapsedS)
	result := map[string]any{
		"schema_version": SchemaVersion, "run_id": run.RunID, "epoch": request.Epoch, "sequence": request.Sequence,
		"decision_id": id, "intent": normalized["intent"], "movement": normalized["movement"],
		"fire": normalized["fire"], "lease": normalized["lease"], "bomb": normalized["bomb"], "valid_choice": normalized["valid_choice"],
		"api_ok": apiOK, "error": normalized["error"], "latency_ms": round(upstream.ElapsedS*1000, 1),
		"usage": usage, "confidence": normalized["confidence"], "api_token_throughput": throughput,
		// The exact labels Djev chose from, so the page can show the decision as the model saw it.
		"labels": map[string]any{"path": criteria, "bomb": bombCriteria},
	}
	valid := normalized["valid_choice"] == true
	run.eventMu.Lock()
	run.decisions++
	if !valid || !apiOK {
		run.invalidDecision++
	}
	run.eventMu.Unlock()
	var rawBody, status, upstreamErr any
	if upstream.RawBody != nil {
		rawBody = *upstream.RawBody
	}
	if upstream.Status != nil {
		status = *upstream.Status
	}
	if upstream.Error != nil {
		upstreamErr = *upstream.Error
	}
	s.exchanges.complete(run.RunID, request.Sequence, status, rawBody, upstreamErr, round(float64(time.Since(upstreamStart).Nanoseconds())/1e6, 1))
	if err := run.appendRecord(map[string]any{
		"record_type": "decision_response", "run_id": run.RunID, "decision_id": id,
		"epoch": request.Epoch, "sequence": request.Sequence, "rawsnapshot": request.Raw,
		"exactactualpayload": payload, "exactrequestbody": exactBody,
		"rawupstreamresponse": map[string]any{"status": status, "body": rawBody, "parsed": upstream.Parsed, "error": upstreamErr},
		"times": map[string]any{
			"server_received_utc": receivedUTC, "upstream_started_utc": upstreamUTC,
			"upstream_started_monotonic_ms": upstreamMono, "latency_ms": result["latency_ms"],
			"client_send_wall_ms": request.Raw["client_send_wall_ms"],
			"bridge_total_ms":     round(float64(time.Since(serverStart).Nanoseconds())/1e6, 1),
		},
		"usage": usage, "api_token_throughput": throughput, "normalized": result, "model_identity": run.identity(),
	}, !apiOK || !valid); err != nil {
		return nil, err
	}
	return result, nil
}

func validateEvent(value any) (map[string]any, error) {
	event, err := requireObject(value, "event")
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"event_id", "epoch"} {
		if _, err := nonnegativeInt(event[key], "event."+key); err != nil {
			return nil, err
		}
	}
	if err := integerOrNone(event["sequence"], "event.sequence"); err != nil {
		return nil, err
	}
	if _, err := nonnegativeInt(event["tick"], "event.tick"); err != nil {
		return nil, err
	}
	for _, key := range []string{"sim_ms", "wall_ms"} {
		if _, err := finiteNumber(event[key], "event."+key, false, min0()); err != nil {
			return nil, err
		}
	}
	if kind, ok := event["type"].(string); !ok || kind == "" {
		return nil, validationError("event.type must be a nonempty string")
	}
	if _, ok := event["payload"].(map[string]any); !ok {
		return nil, validationError("event.payload must be an object")
	}
	return event, nil
}

// ValidateEventBatch checks a /api/run/event body without touching run state.
func ValidateEventBatch(body any) ([]map[string]any, error) {
	object, err := requireSchema(body)
	if err != nil {
		return nil, err
	}
	raw, err := requireList(object["events"], "events")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, validationError("events must not be empty")
	}
	events := make([]map[string]any, len(raw))
	for i, item := range raw {
		if events[i], err = validateEvent(item); err != nil {
			return nil, err
		}
	}
	return events, nil
}

var executionTypes = map[string]bool{"command_applied": true, "command_ended": true, "neutral_started": true, "neutral_ended": true, "shot": true, "response_received": true, "response_rejected": true}
var durableTypes = map[string]bool{"hit": true, "checkpoint": true, "terminal": true, "qualification_invalidated": true}

func (s *Server) RecordRunEvents(body any) (map[string]any, error) {
	object, err := requireSchema(body)
	if err != nil {
		return nil, err
	}
	run, err := s.getRun(object["run_id"])
	if err != nil {
		return nil, err
	}
	events, err := ValidateEventBatch(object)
	if err != nil {
		return nil, err
	}
	run.eventMu.Lock()
	defer run.eventMu.Unlock()
	if run.complete {
		return nil, conflictError("run_complete", "run is already complete")
	}
	tempLast := run.lastEventID
	var fresh []any
	freshHashes := map[int64]string{}
	execution, hits, checkpoints := []any{}, []any{}, []any{}
	fsync := false
	for _, event := range events {
		eventID, _ := intValue(event["event_id"])
		encoded, err := canonicalJSON(event)
		if err != nil {
			return nil, err
		}
		hash := string(encoded)
		if tempLast != nil && eventID <= *tempLast {
			if run.eventHashes[eventID] != hash && freshHashes[eventID] != hash {
				return nil, conflictError("event_conflict", "conflicting duplicate event_id %d", eventID)
			}
			continue
		}
		if tempLast == nil {
			if eventID != 0 && eventID != 1 {
				return nil, conflictError("event_gap", "event gap before first event_id %d", eventID)
			}
		} else if eventID != *tempLast+1 {
			return nil, conflictError("event_gap", "event gap: expected %d, got %d", *tempLast+1, eventID)
		}
		id := eventID
		tempLast = &id
		fresh = append(fresh, event)
		freshHashes[eventID] = hash
		kind := event["type"].(string)
		if executionTypes[kind] {
			execution = append(execution, event)
		}
		if kind == "hit" {
			hits = append(hits, event)
		}
		if kind == "checkpoint" {
			checkpoints = append(checkpoints, event)
		}
		fsync = fsync || durableTypes[kind]
	}
	if len(fresh) > 0 {
		first := fresh[0].(map[string]any)["event_id"]
		last := fresh[len(fresh)-1].(map[string]any)["event_id"]
		if err := run.appendRecord(map[string]any{
			"record_type": "client_events", "run_id": run.RunID, "event_id_start": first, "event_id_end": last,
			"events": fresh, "clientexecution": execution, "hits": hits, "checkpoints": checkpoints,
			"times": map[string]any{"server_receive_utc": utcNow()},
		}, fsync); err != nil {
			return nil, err
		}
		for id, hash := range freshHashes {
			run.eventHashes[id] = hash
		}
		run.lastEventID = tempLast
		run.eventCount += int64(len(fresh))
	}
	acked := int64(-1)
	if run.lastEventID != nil {
		acked = *run.lastEventID
	}
	return map[string]any{"schema_version": SchemaVersion, "run_id": run.RunID, "acked_event_id": acked}, nil
}

var terminalReasons = map[string]bool{"death": true, "target": true, "aborted": true, "timing_invalid": true, "trace_error": true}

func validateTerminal(value any) (map[string]any, error) {
	terminal, err := requireObject(value, "terminal")
	if err != nil {
		return nil, err
	}
	if reason, _ := terminal["reason"].(string); !terminalReasons[reason] {
		return nil, validationError("terminal.reason is unknown")
	}
	for _, key := range []string{"tick"} {
		if _, err := nonnegativeInt(terminal[key], "terminal."+key); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"sim_ms", "wall_ms"} {
		if _, err := finiteNumber(terminal[key], "terminal."+key, false, min0()); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"lives", "wave"} {
		if _, err := nonnegativeInt(terminal[key], "terminal."+key); err != nil {
			return nil, err
		}
	}
	if _, err := finiteNumber(terminal["score"], "terminal.score", false, min0()); err != nil {
		return nil, err
	}
	violations, err := requireList(terminal["qualification_violations"], "terminal.qualification_violations")
	if err != nil {
		return nil, err
	}
	for i, violation := range violations {
		if _, ok := violation.(string); !ok {
			return nil, validationError("terminal.qualification_violations[%d] must be a string", i)
		}
	}
	return terminal, nil
}

func (s *Server) EndRun(body any) (map[string]any, error) {
	object, err := requireSchema(body)
	if err != nil {
		return nil, err
	}
	run, err := s.getRun(object["run_id"])
	if err != nil {
		return nil, err
	}
	lastEventID, err := nonnegativeInt(object["last_event_id"], "last_event_id")
	if err != nil {
		return nil, err
	}
	terminal, err := validateTerminal(object["terminal"])
	if err != nil {
		return nil, err
	}
	// Wait for an in-flight decision so its response record precedes run_ended.
	select {
	case run.upstream <- struct{}{}:
	case <-time.After(endRunUpstreamWait):
		return nil, conflictError("decision_in_progress", "decision still in progress for this run")
	}
	defer func() { <-run.upstream }()
	run.eventMu.Lock()
	defer run.eventMu.Unlock()
	expectedLast := int64(0)
	if run.lastEventID != nil {
		expectedLast = *run.lastEventID
	}
	if run.complete {
		return nil, conflictError("run_complete", "run is already complete")
	}
	if lastEventID != expectedLast {
		return nil, conflictError("event_gap", "terminal last_event_id %d does not match acked %d", lastEventID, expectedLast)
	}
	summary := map[string]any{
		"status": "complete", "terminal_reason": terminal["reason"], "last_event_id": expectedLast,
		"events": run.eventCount, "decisions": run.decisions, "invalid_decisions": run.invalidDecision,
		"trace_complete": true, "sim_ms": terminal["sim_ms"], "wall_ms": terminal["wall_ms"],
		"lives": terminal["lives"], "wave": terminal["wave"], "score": terminal["score"],
		"qualification_violations": terminal["qualification_violations"],
	}
	run.complete = true
	run.terminal = terminal
	if err := run.appendRecord(map[string]any{
		"record_type": "run_ended", "run_id": run.RunID, "last_event_id": expectedLast,
		"terminal": terminal, "summary": summary, "times": map[string]any{"server_receive_utc": utcNow()},
	}, true); err != nil {
		return nil, err
	}
	absolute, _ := filepath.Abs(run.EventsPath)
	return map[string]any{"schema_version": SchemaVersion, "run_id": run.RunID, "complete": true, "trace_path": absolute, "summary": summary}, nil
}
