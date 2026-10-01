package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const strategyBody = "Prioritize survival and useful fire. Shooting has no movement penalty and no ammo cost.\n"

func sourceHash() string {
	sum := sha256.Sum256([]byte("core-source\ncontroller-source"))
	return hex.EncodeToString(sum[:])
}

type fakeUpstream struct {
	mu     sync.Mutex
	calls  [][]byte
	handle func(body []byte) (*UpstreamResult, error)
}

func (f *fakeUpstream) Call(_ context.Context, body []byte) (*UpstreamResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, body)
	f.mu.Unlock()
	return f.handle(body)
}

func (f *fakeUpstream) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func okResult(t *testing.T, raw string, elapsed float64) *UpstreamResult {
	t.Helper()
	parsed, err := decodeJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	status := 200
	return &UpstreamResult{Parsed: parsed, RawBody: &raw, Status: &status, ElapsedS: elapsed, Usage: usageFromPayload(parsed)}
}

type fixture struct {
	t        *testing.T
	root     string
	server   *Server
	upstream *fakeUpstream
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	html := `<html><body><script id="space-decision-core">core-source</script><script id="space-djev-controller">controller-source</script></body></html>`
	must(t, os.WriteFile(filepath.Join(root, "space-shooter.html"), []byte(html), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "strategy.md"), []byte("version: "+PromptVersion+"\n"+strategyBody), 0o644))
	upstream := &fakeUpstream{handle: func([]byte) (*UpstreamResult, error) { return nil, errors.New("unexpected upstream call") }}
	server := NewServer(Config{
		HTMLPath: filepath.Join(root, "space-shooter.html"), StrategyPath: filepath.Join(root, "strategy.md"),
		RunsDir: filepath.Join(root, "runs"), DjevURL: "http://user:secret@djev.invalid:8011/?key=x", DjevModel: "fixture-model",
	}, upstream)
	return &fixture{t: t, root: root, server: server, upstream: upstream}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	value, err := decodeJSON([]byte(text))
	must(t, err)
	return value.(map[string]any)
}

func startBody(t *testing.T, engineHash string) map[string]any {
	return decode(t, fmt.Sprintf(`{"schema_version":1,"manifest":{"seed":20260920,"profile":"hardest",
		"difficulty":{"bulletDensity":4,"enemyDensity":3,"fastBulletRatio":0.85,"fastBulletSpeed":2.4},
		"engine_version":"slice-a-test","engine_hash":%q,"dt_ms":16.666666666666668,
		"prompt_version":%q,"context_version":%q,"mode":"cli",
		"rules":{"player_speed_px_s":112,"lease_ticks":{"short":15,"medium":30}}}}`, engineHash, PromptVersion, ContextVersion))
}

func candidateJSON(movement string) string {
	contact, moveContact, escapes, escapeGap := "null", "null", "6", "28.75"
	if movement == "left" {
		contact, moveContact, escapes, escapeGap = "125.0", "125.0", "0", "null"
	}
	return fmt.Sprintf(`{"id":%q,
		"short":{"endpoint":{"x":480.123,"y":407.987},"contact_ms":null,"clearance_px":37.25,"enemy_clearance_px":null,
			"edge_distances_px":{"left":460.123,"right":459.877,"top":398.987,"bottom":193.013},"shot_eta_ms":218.75},
		"medium":{"endpoint":{"x":481.456,"y":406.654},"contact_ms":%s,"clearance_px":28.75,"enemy_clearance_px":64.25,
			"edge_distances_px":{"left":461.456,"right":458.544,"top":397.654,"bottom":194.346},"shot_eta_ms":null,
			"crowd_count":2,"move_contact_ms":%s,"escape_options":%s,"escape_clearance_px":%s,
			"pickup_collect":null,"pickup_toward":null}}`,
		movement, contact, moveContact, escapes, escapeGap)
}

func decisionBody(t *testing.T, runID string) map[string]any {
	candidates := make([]string, len(ActionIDs))
	for i, movement := range ActionIDs {
		candidates[i] = candidateJSON(movement)
	}
	return decode(t, fmt.Sprintf(`{"schema_version":1,"run_id":%q,"epoch":3,"sequence":4,"snapshot_tick":120,"client_send_wall_ms":2000.5,
		"state":{"tick":120,"sim_ms":2000.0,"wave":2,
			"difficulty":{"bulletDensity":4,"enemyDensity":3,"fastBulletRatio":0.85,"fastBulletSpeed":2.4},
			"player":{"x":480.12,"y":408.34,"w":20,"h":18,"lives":3,"cooldown_ms":40.25,"invulnerability_ms":0},
			"active_command":{"decision_id":"old-decision","sequence":3,"movement":"up","fire":"shoot","lease":"short","remaining_ms":83.34,"intent":"evade"},
			"last_intent":"recover","enemy_fire_in_ms":155.5,
			"bomb":{"charges":3,"max_charges":6,"radius_px":200,"bullets_in_radius":4,"enemies_in_radius":1},"missiles_active":2,
			"threat_counts":{"enemies":6,"enemy_bullets":11,"nearest_threats_total":17},
			"nearest_threats":[{"kind":"bullet","x":500,"y":320,"vx":0,"vy":405.25,"w":8,"h":8}],
			"recent_commands":[{"movement":"left","fire":"cease","lease":"medium","elapsed_ms":500,"dx":-56,"dy":0,"source":"djev"}],
			"recent_hits":[{"ago_ms":200,"kind":"bullet","x":482,"y":402,"vx":0,"vy":405,"lives_after":1}]},
		"forecast":{"expected_delay_ms":260,"latency_samples":0,"latency_spread_ms":0,"horizon_ms":760,
			"prefix":{"authorized_remaining_ms":83.34,"contact_ms":null},
			"assumptions":{"enemy_motion":"current_linear","future_spawns_included":false,
				"contact_window":"after_arrival_while_vulnerable","enemy_clearance_window":"after_arrival"},
			"candidates":[%s]},
		"checkpoint":{"seed":20260920,"rng_state":"must stay out of upstream","tick":120}}`, runID, strings.Join(candidates, ",")))
}

func terminalBody() map[string]any {
	value, _ := decodeJSON([]byte(`{"reason":"target","tick":7261,"sim_ms":121016.7,"wall_ms":121020,"lives":2,"wave":3,"score":900,"qualification_violations":[]}`))
	return value.(map[string]any)
}

func medium(body map[string]any, index int) map[string]any {
	return body["forecast"].(map[string]any)["candidates"].([]any)[index].(map[string]any)["medium"].(map[string]any)
}

func (f *fixture) start() string {
	f.t.Helper()
	result, err := f.server.StartRun(startBody(f.t, sourceHash()))
	must(f.t, err)
	return result["run_id"].(string)
}

func (f *fixture) records(runID string) []map[string]any {
	f.t.Helper()
	file, err := os.Open(filepath.Join(f.root, "runs", runID, "events.jsonl"))
	must(f.t, err)
	defer file.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		out = append(out, decode(f.t, scanner.Text()))
	}
	return out
}

// jsonEqual compares values after a JSON round trip so json.Number and float spellings agree.
func jsonEqual(t *testing.T, got, want any) bool {
	t.Helper()
	normalize := func(value any) any {
		raw, err := canonicalJSON(value)
		must(t, err)
		var out any
		must(t, json.Unmarshal(raw, &out))
		return out
	}
	return reflect.DeepEqual(normalize(got), normalize(want))
}

func assertJSON(t *testing.T, got, want any) {
	t.Helper()
	if !jsonEqual(t, got, want) {
		gotText, _ := canonicalJSON(got)
		wantText, _ := canonicalJSON(want)
		t.Fatalf("mismatch\n got: %s\nwant: %s", gotText, wantText)
	}
}

func expectAPIError(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *ApiError
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("expected API error %d, got %v", status, err)
	}
}

func requestFor(t *testing.T, body map[string]any) *DecisionRequest {
	t.Helper()
	request, err := validateDecisionRequest(body)
	must(t, err)
	return request
}

func TestRunStartPinsPromptSourceHashAndRuntimeWithoutSecrets(t *testing.T) {
	f := newFixture(t)
	result, err := f.server.StartRun(startBody(t, sourceHash()))
	must(t, err)
	if result["prompt_version"] != PromptVersion || result["prompt_hash"] != sha256Text(strategyBody) {
		t.Fatalf("unexpected start result %v", result)
	}
	runID := result["run_id"].(string)
	if !regexp.MustCompile(`^run-\d{8}T\d{6}Z-[A-Za-z0-9-]+$`).MatchString(runID) {
		t.Fatalf("run id %q", runID)
	}
	record := f.records(runID)[0]
	if record["record_type"] != "run_started" || record["context_version"] != ContextVersion || record["engine_source_hash"] != sourceHash() || record["prompt_text"] != strategyBody {
		t.Fatalf("run_started record %v", record)
	}
	if !strings.HasSuffix(record["recorded_at_utc"].(string), "Z") || !strings.HasSuffix(record["server_time_utc"].(string), "Z") {
		t.Fatal("timestamps must be UTC with Z")
	}
	identity := record["model_identity"].(map[string]any)
	if identity["configured_model"] != "fixture-model" || identity["endpoint"] != "http://djev.invalid:8011" {
		t.Fatalf("model identity %v", identity)
	}
	line, _ := canonicalJSON(record)
	if strings.Contains(strings.ToLower(string(line)), "authorization") || strings.Contains(string(line), "secret") {
		t.Fatal("trace leaked credentials")
	}
	_, err = f.server.StartRun(startBody(t, strings.Repeat("0", 64)))
	expectAPIError(t, err, 400)
}

func TestRunStartRejectsOldManifestVersions(t *testing.T) {
	f := newFixture(t)
	for field, old := range map[string]string{"prompt_version": "djev-authoritative-v1", "context_version": "djev-observation-v1"} {
		body := startBody(t, sourceHash())
		body["manifest"].(map[string]any)[field] = old
		_, err := f.server.StartRun(body)
		expectAPIError(t, err, 400)
	}
}

func TestCenterProgressAndNullGapLabels(t *testing.T) {
	for _, tc := range []struct {
		endY   string
		toward bool
	}{{"494", true}, {"550", false}, {"606", false}} {
		body := decisionBody(t, "geometry")
		player := body["state"].(map[string]any)["player"].(map[string]any)
		player["x"], player["y"] = json.Number("480"), json.Number("550")
		player["private_extra"] = "not model-visible"
		prediction := medium(body, 0)
		prediction["endpoint"] = map[string]any{"x": json.Number("480"), "y": json.Number(tc.endY)}
		prediction["clearance_px"] = nil
		prediction["escape_clearance_px"] = nil
		request := requestFor(t, body)
		criteria, _, err := buildPathCriteria(request)
		must(t, err)
		// The ship starts 7.5 px below the home zone: re-entering it is "back to zone".
		want := "Tier 3 RISKY: 6/9 escapes, open gap, near enemy, busy space"
		if tc.toward {
			want += ", back to zone, toward center"
		} else {
			want += ", outside zone"
		}
		if criteria.Get("hold__medium") != want+"." {
			t.Fatalf("end_y %s: %q", tc.endY, criteria.Get("hold__medium"))
		}
	}
}

func TestModelStateIsOnlyEnemyCount(t *testing.T) {
	// Every choice carries its own facts; extra numeric state cost ~80 ms of Djev latency per call.
	packed, err := packModelContext(requestFor(t, decisionBody(t, "lean")))
	must(t, err)
	if !reflect.DeepEqual(packed.Keys(), []string{"enemy_count"}) {
		t.Fatalf("state keys %v", packed.Keys())
	}
	assertJSON(t, packed.Get("enemy_count"), 6)
}

func TestHoldCollisionAndMoveContactLabels(t *testing.T) {
	for _, contact := range []any{nil, json.Number("0"), json.Number("0.04"), json.Number("322.64"), json.Number("391.24")} {
		body := decisionBody(t, "hold-contact")
		body["forecast"].(map[string]any)["prefix"].(map[string]any)["contact_ms"] = json.Number("999")
		medium(body, 0)["contact_ms"] = contact
		medium(body, 0)["move_contact_ms"] = contact
		candidates := body["forecast"].(map[string]any)["candidates"].([]any)
		for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		}
		request := requestFor(t, body)
		_, err := packModelContext(request)
		must(t, err)
		criteria, _, err := buildPathCriteria(request)
		must(t, err)
		if criteria.Keys()[0] != "hold__medium" {
			t.Fatal("criteria must keep fixed movement order")
		}
		want := "Tier 3 RISKY: 6/9 escapes, tight gap, near enemy, busy space."
		if contact != nil {
			f, _ := numberValue(contact)
			want = fmt.Sprintf("Tier 6 DEADLY: a threat hits the ship in %d ms.", int64(f))
		}
		if criteria.Get("hold__medium") != want {
			t.Fatalf("contact %v: %q", contact, criteria.Get("hold__medium"))
		}
	}
}

func TestAllCollidingPathsKeepFixedOrder(t *testing.T) {
	body := decisionBody(t, "all-colliding")
	for i := range ActionIDs {
		medium(body, i)["contact_ms"] = json.Number("0")
		medium(body, i)["move_contact_ms"] = json.Number("0")
		medium(body, i)["clearance_px"] = json.Number("-0.25")
	}
	request := requestFor(t, body)
	criteria, _, err := buildPathCriteria(request)
	must(t, err)
	if !reflect.DeepEqual(criteria.Keys(), PathIDs) {
		t.Fatalf("keys %v", criteria.Keys())
	}
	for _, key := range criteria.Keys() {
		if criteria.Get(key) != "Tier 6 DEADLY: a threat hits the ship in 0 ms." {
			t.Fatalf("%s: %q", key, criteria.Get(key))
		}
	}
	_, err = packModelContext(request)
	must(t, err)
}

func TestPathTiersRankContactEscapesWallsGapsAndEnemyDistance(t *testing.T) {
	// The fixture's hold row is stationary (one tier lower); "up" continues the executing command.
	cases := []struct {
		index  int
		update string
		want   string
	}{
		{0, `{"move_contact_ms":40.6}`, "Tier 6 DEADLY: a threat hits the ship in 40 ms."},
		{0, `{"escape_options":0}`, "Tier 5 DOOMED: safe now, but every follow-up move is hit."},
		{0, `{"edge_distances_px":{"left":461.456,"right":458.544,"top":397.654,"bottom":24.9}}`, "Tier 4 TRAP: ends pinned against the wall with no escape room."},
		{3, `{"escape_options":1}`, "Tier 3 RISKY: 1/9 escapes, tight gap, near enemy, busy space, keeps course."},
		{3, `{"escape_clearance_px":14.9}`, "Tier 3 RISKY: 6/9 escapes, grazing gap, near enemy, busy space, keeps course."},
		{3, `{"escape_clearance_px":40,"enemy_clearance_px":110,"crowd_count":1}`, "Tier 1 GOOD: 6/9 escapes, open gap, open space, keeps course."},
		{3, `{"escape_clearance_px":40,"enemy_clearance_px":109.9}`, "Tier 2 OK: 6/9 escapes, open gap, near enemy, busy space, keeps course."},
		{3, `{"escape_clearance_px":40,"enemy_clearance_px":null,"crowd_count":4,"escape_options":3}`, "Tier 2 OK: 3/9 escapes, open gap, crowded, keeps course."},
		{0, `{"escape_clearance_px":40,"enemy_clearance_px":110,"crowd_count":1}`, "Tier 2 OK: 6/9 escapes, open gap, open space."},
		{4, `{"escape_clearance_px":40,"enemy_clearance_px":110,"crowd_count":1}`, "Tier 2 OK: 6/9 escapes, open gap, open space."},
		{3, `{"escape_clearance_px":40,"enemy_clearance_px":110,"crowd_count":1,"edge_distances_px":{"left":461,"right":458,"top":69.9,"bottom":194}}`, "Tier 2 OK: 6/9 escapes, open gap, near wall, open space, keeps course."},
		{0, `{}`, "Tier 3 RISKY: 6/9 escapes, tight gap, near enemy, busy space."},
	}
	for _, tc := range cases {
		body := decisionBody(t, "tier")
		for key, value := range decode(t, tc.update) {
			medium(body, tc.index)[key] = value
		}
		criteria, _, err := buildPathCriteria(requestFor(t, body))
		must(t, err)
		if got := criteria.Get(PathIDs[tc.index]); got != tc.want {
			t.Fatalf("%s %s: got %q", PathIDs[tc.index], tc.update, got)
		}
	}
}

func TestBombCriteriaRankUseOnlyWithoutSafeMove(t *testing.T) {
	urgent := buildBombCriteria(BombFacts{Charges: 1, RadiusPx: 200, Bullets: 7, Enemies: 0}, 6)
	if urgent.Get("detonate") != "Rank 1 USE NOW: every move is tier 5 or 6; destroys 7 bullets and 0 enemy ships within 200 px; 0 charges left after." ||
		urgent.Get("hold") != "Rank 2 WAIT: every move is tier 5 or 6; the ship is likely hit. Keeps 1 charges and 0 jets." {
		t.Fatalf("urgent %q / %q", urgent.Get("detonate"), urgent.Get("hold"))
	}
	if !reflect.DeepEqual(urgent.Keys(), BombIDs) {
		t.Fatalf("bomb choice order %v", urgent.Keys())
	}
	calm := buildBombCriteria(BombFacts{Charges: 2, RadiusPx: 200, Bullets: 3, Enemies: 1}, 2)
	if calm.Get("hold") != "Rank 1 SAVE: a tier 2 move exists. Keeps 2 charges and 0 jets." || calm.Get("detonate") != "Rank 2 WASTE: a tier 2 move exists; destroys 3 bullets and 1 enemy ships within 200 px; 1 charges left after." {
		t.Fatalf("calm %q / %q", calm.Get("hold"), calm.Get("detonate"))
	}
	nothing := buildBombCriteria(BombFacts{Charges: 2, RadiusPx: 200}, 6)
	if !strings.HasPrefix(nothing.Get("hold").(string), "Rank 1 SAVE: nothing is in range") {
		t.Fatalf("empty blast must not rank first: %q", nothing.Get("hold"))
	}
	empty := buildBombCriteria(BombFacts{Charges: 0, RadiusPx: 200, Bullets: 7}, 6)
	if empty.Get("hold") != "Rank 1: no charges left." {
		t.Fatalf("%q", empty.Get("hold"))
	}
	sacrifice := buildBombCriteria(BombFacts{Charges: 0, RadiusPx: 200, Bullets: 5, SacrificeJets: 3}, 6)
	if sacrifice.Get("detonate") != "Rank 1 USE NOW: every move is tier 5 or 6; destroys 5 bullets and 0 enemy ships within 200 px; no bombs left, so one escort jet self-destructs (2 jets left after)." {
		t.Fatalf("sacrifice %q", sacrifice.Get("detonate"))
	}
	for _, tc := range []struct {
		criteria *OrderedMap
		want     string
	}{{urgent, "detonate"}, {calm, "hold"}, {nothing, "hold"}, {empty, "hold"}, {sacrifice, "detonate"}} {
		if got := oracleBomb(map[string]any{"detonate": tc.criteria.Get("detonate")}); got != tc.want {
			t.Fatalf("oracle chose %s, want %s", got, tc.want)
		}
	}
	body := decisionBody(t, "bomb")
	delete(body["state"].(map[string]any), "bomb")
	_, err := packModelContext(requestFor(t, body))
	expectAPIError(t, err, 400)
}

func TestPickupLabelsAndRanks(t *testing.T) {
	// "up" (index 3) continues the executing command and is tier 2 OK in the base fixture.
	body := decisionBody(t, "pickup")
	medium(body, 3)["pickup_collect"] = "bomb"
	medium(body, 5)["pickup_toward"] = "weapon"
	criteria, _, err := buildPathCriteria(requestFor(t, body))
	must(t, err)
	if got := criteria.Get("up__medium"); got != "Tier 1 GOOD: 6/9 escapes, tight gap, near enemy, collects bomb, busy space, keeps course." {
		t.Fatalf("collect: %q", got)
	}
	if got := criteria.Get("up_left__medium"); got != "Tier 2 OK: 6/9 escapes, tight gap, near enemy, toward weapon, busy space, keeps course." {
		t.Fatalf("toward: %q", got)
	}
	medium(body, 6)["pickup_collect"] = "weapon"
	medium(body, 6)["escape_clearance_px"] = json.Number("14.9")
	criteria, _, err = buildPathCriteria(requestFor(t, body))
	must(t, err)
	if got := criteria.Get("up_right__medium"); got != "Tier 1 GOOD: 6/9 escapes, grazing gap, near enemy, collects weapon (power up), busy space, keeps course." {
		t.Fatalf("weapon block lifts two tiers: %q", got)
	}
	delete(medium(body, 6), "pickup_collect")
	medium(body, 6)["pickup_collect"] = nil
	medium(body, 6)["escape_clearance_px"] = json.Number("28.75")
	criteria, _, err = buildPathCriteria(requestFor(t, body))
	must(t, err)
	// Pursuit: while a wanted pickup is reachable, safe moves that ignore it drop a tier.
	if got := criteria.Get("up_right__medium"); got != "Tier 3 RISKY: 6/9 escapes, tight gap, near enemy, busy space, keeps course." {
		t.Fatalf("ignoring the pickup: %q", got)
	}
	// Pickups never lift a move out of an unsafe tier.
	medium(body, 1)["pickup_collect"] = "bomb"
	criteria, _, err = buildPathCriteria(requestFor(t, body))
	must(t, err)
	if got := criteria.Get("left__medium"); got != "Tier 6 DEADLY: a threat hits the ship in 125 ms." {
		t.Fatalf("deadly collect: %q", got)
	}
	medium(body, 2)["pickup_toward"] = "shield"
	_, _, err = buildPathCriteria(requestFor(t, body))
	expectAPIError(t, err, 400)
}

func TestMissilePickupLabelsAndOracle(t *testing.T) {
	body := decisionBody(t, "missile-pickup")
	medium(body, 6)["pickup_collect"] = "missile"
	medium(body, 6)["escape_clearance_px"] = json.Number("14.9")
	medium(body, 5)["pickup_toward"] = "missile"
	criteria, _, err := buildPathCriteria(requestFor(t, body))
	must(t, err)
	if got := criteria.Get("up_right__medium"); got != "Tier 1 GOOD: 6/9 escapes, grazing gap, near enemy, collects missile (power up), busy space, keeps course." {
		t.Fatalf("missile pod lifts two tiers like a weapon block: %q", got)
	}
	if got, _ := criteria.Get("up_left__medium").(string); !strings.Contains(got, "toward missile") {
		t.Fatalf("toward: %q", got)
	}
	_, towardMissile := oracleScore("Tier 2 OK: 6/9 escapes, open gap, toward missile, open space.")
	_, plain := oracleScore("Tier 2 OK: 6/9 escapes, open gap, open space.")
	if towardMissile <= plain {
		t.Fatalf("the oracle prefers heading toward a missile pod: %v vs %v", towardMissile, plain)
	}
}

func TestMotionRelativeToExecutingCommand(t *testing.T) {
	want := map[string]string{"hold": "stationary", "right": "turns", "up": "continues", "down": "reverses",
		"up_left": "continues", "up_right": "continues", "down_left": "reverses", "down_right": "reverses"}
	for movement, relation := range want {
		if got := motionRelation(movement, "up"); got != relation {
			t.Fatalf("%s after up: %s", movement, got)
		}
	}
	if motionRelation("left", "") != "starts" || motionRelation("left", "hold") != "starts" {
		t.Fatal("moving from rest must be 'starts'")
	}
}

func TestPackingRejectsInvalidOrAmbiguousForecasts(t *testing.T) {
	for _, contact := range []any{json.Number("-1"), true, "none"} {
		body := decisionBody(t, "invalid")
		medium(body, 0)["contact_ms"] = contact
		_, err := packModelContext(requestFor(t, body))
		expectAPIError(t, err, 400)
	}
	for _, index := range []int{0, 1} {
		body := decisionBody(t, "ambiguous")
		replacement := "left"
		if index == 1 {
			replacement = "hold"
		}
		body["forecast"].(map[string]any)["candidates"].([]any)[index].(map[string]any)["id"] = replacement
		_, err := packModelContext(requestFor(t, body))
		expectAPIError(t, err, 400)
	}
	paths := []struct {
		path  []any
		value any
	}{
		{[]any{"state", "player", "w"}, nil},
		{[]any{"state", "player", "h"}, true},
		{[]any{"state", "player", "cooldown_ms"}, json.Number("-1")},
		{[]any{"state", "player", "lives"}, json.Number("1.5")},
		{[]any{"state", "threat_counts", "enemies"}, json.Number("-1")},
		{[]any{"forecast", "prefix", "contact_ms"}, json.Number("-1")},
		{[]any{"forecast", "candidates", 8, "medium", "endpoint", "x"}, true},
		{[]any{"forecast", "candidates", 8, "medium", "edge_distances_px", "right"}, json.Number("-1")},
	}
	for _, tc := range paths {
		body := decisionBody(t, "invalid-table")
		var target any = body
		for _, key := range tc.path[:len(tc.path)-1] {
			switch k := key.(type) {
			case string:
				target = target.(map[string]any)[k]
			case int:
				target = target.([]any)[k]
			}
		}
		target.(map[string]any)[tc.path[len(tc.path)-1].(string)] = tc.value
		_, err := packModelContext(requestFor(t, body))
		expectAPIError(t, err, 400)
	}
	for _, missing := range []string{"contact_ms", "clearance_px"} {
		body := decisionBody(t, "missing")
		delete(medium(body, 8), missing)
		_, err := packModelContext(requestFor(t, body))
		expectAPIError(t, err, 400)
	}
}

func TestNormalizationPreservesEveryCombination(t *testing.T) {
	for _, intent := range IntentIDs {
		for _, movement := range ActionIDs {
			for _, fire := range FireIDs {
				response := decode(t, fmt.Sprintf(`{"answers":{"intent":{"choice":%q,"confidence":0.65},"path":{"choice":"%s__medium","confidence":0.75},"fire":{"choice":%q,"confidence":0.85},"bomb":{"choice":"detonate","confidence":0.95}}}`, intent, movement, fire))
				result := normalizeDecisionResponse(response)
				if result["valid_choice"] != true || result["intent"] != intent || result["movement"] != movement || result["fire"] != fire || result["lease"] != "medium" {
					t.Fatalf("normalized %v", result)
				}
				assertJSON(t, result["confidence"], map[string]any{"intent": 0.65, "path": 0.75, "movement": 0.75, "fire": 0.85, "lease": 0.75, "bomb": 0.95})
				if result["bomb"] != "detonate" {
					t.Fatalf("bomb %v", result["bomb"])
				}
			}
		}
	}
	result := normalizeDecisionResponse(decode(t, `{"answers":{"intent":{"choice":"evade"},"path":{"choice":"left__short"},"fire":{"choice":"shoot"},"bomb":{"choice":"hold","confidence":0.9}}}`))
	if result["valid_choice"] != false || !strings.Contains(result["error"].(string), "unknown_path_choice") {
		t.Fatalf("short path must be invalid: %v", result)
	}
}

func TestInvalidIntentIsAtomicAndRawLogged(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	cases := []struct {
		intent string
		err    string
	}{{`"recover"`, "invalid_intent_answer"}, {`{"choice":"retreat","confidence":0.9}`, "unknown_intent_choice"}, {`{"choice":1}`, "invalid_intent_choice_type"}}
	for sequence, tc := range cases {
		intent := ""
		if tc.intent != "" {
			intent = `,"intent":` + tc.intent
		}
		raw := `{"model":"dgemma","answers":{"path":{"choice":"left__medium","confidence":0.9},"fire":{"choice":"shoot","confidence":0.8},"bomb":{"choice":"hold","confidence":0.9}` + intent + `}}`
		f.upstream.handle = func([]byte) (*UpstreamResult, error) { return okResult(t, raw, 0.1), nil }
		body := decisionBody(t, runID)
		body["sequence"] = json.Number(fmt.Sprint(sequence))
		result, err := f.server.HandleDecision(context.Background(), body)
		must(t, err)
		if result["api_ok"] != true || result["valid_choice"] != false || result["error"] != tc.err {
			t.Fatalf("result %v", result)
		}
		for _, key := range []string{"intent", "movement", "fire", "lease"} {
			if value, ok := result[key]; !ok || value != nil {
				t.Fatalf("%s must be present and null", key)
			}
		}
		records := f.records(runID)
		last := records[len(records)-1]
		assertJSON(t, last["rawsnapshot"], body)
		if last["rawupstreamresponse"].(map[string]any)["body"] != raw {
			t.Fatal("raw upstream body must be logged verbatim")
		}
		assertJSON(t, last["normalized"], result)
	}
}

func TestUpstreamTransportHTTPAndBudgetErrorsReturnNullDecision(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	for _, failure := range []string{"transport", "http", "budget"} {
		calls := f.upstream.callCount()
		switch failure {
		case "transport":
			f.upstream.handle = func([]byte) (*UpstreamResult, error) { return nil, errors.New("TimeoutError: fixture timeout") }
		default:
			f.upstream.handle = func([]byte) (*UpstreamResult, error) {
				result := okResult(t, `{"answers":{"intent":{"choice":"recover"},"path":{"choice":"right__medium"},"fire":{"choice":"shoot"},"bomb":{"choice":"hold","confidence":0.9}}}`, 0.1)
				status, message := 503, "HTTPError: 503"
				result.Status, result.Error = &status, &message
				return result, nil
			}
		}
		MaxPackedStateChars = 6000
		if failure == "budget" {
			MaxPackedStateChars = 1
		}
		result, err := f.server.HandleDecision(context.Background(), decisionBody(t, runID))
		MaxPackedStateChars = 6000
		must(t, err)
		if failure == "budget" && f.upstream.callCount() != calls {
			t.Fatal("budget failure must not call upstream")
		}
		if result["api_ok"] != false || result["valid_choice"] != false {
			t.Fatalf("%s: %v", failure, result)
		}
		assertJSON(t, result["confidence"], nullConfidence())
		records := f.records(runID)
		response := records[len(records)-1]
		assertJSON(t, response["normalized"], result)
		if failure == "budget" {
			if response["exactrequestbody"] != nil {
				t.Fatal("budget failure has no request body")
			}
			continue
		}
		request := records[len(records)-2]
		if request["record_type"] != "decision_request" || response["exactrequestbody"] != request["exactrequestbody"] {
			t.Fatal("response must repeat the exact request body")
		}
		assertJSON(t, decode(t, response["exactrequestbody"].(string)), response["exactactualpayload"])
	}
}

// End-to-end against a fake Djev HTTP server: the logged request body precedes transport and equals the wire bytes.
func TestExactRequestBodyPrecedesTransportAndMatchesWire(t *testing.T) {
	const apiKey = "fixture-api-key-never-in-trace"
	for _, outcome := range []string{"success", "http_error", "timeout"} {
		f := newFixture(t)
		var runID string
		var wire []byte
		djev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wire, _ = io.ReadAll(r.Body)
			if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer "+apiKey {
				t.Errorf("bad upstream request %s %q", r.URL.Path, r.Header.Get("Authorization"))
			}
			records := f.records(runID)
			if len(records) != 2 || records[1]["record_type"] != "decision_request" || records[1]["exactrequestbody"] != string(wire) {
				t.Errorf("request record must precede transport with exact wire body")
			}
			sent := decode(t, string(wire))
			if !reflect.DeepEqual(keysInWireOrder(t, wire, "state"), []string{"enemy_count"}) {
				t.Errorf("state key order %v", keysInWireOrder(t, wire, "state"))
			}
			if !reflect.DeepEqual(keysInWireOrder(t, wire, "questions"), []string{"path", "fire", "bomb"}) {
				t.Errorf("question order")
			}
			path := sent["questions"].(map[string]any)["path"].(map[string]any)["criteria"].(map[string]any)
			if path["left__medium"] != "Tier 6 DEADLY: a threat hits the ship in 125 ms." {
				t.Errorf("left label %q", path["left__medium"])
			}
			switch outcome {
			case "timeout":
				time.Sleep(300 * time.Millisecond)
			case "http_error":
				w.WriteHeader(503)
				w.Write([]byte(`{"error":"fixture failure"}`))
			default:
				w.Write([]byte(`{"model":"dgemma","answers":{"intent":{"choice":"recover","confidence":0.9},"path":{"choice":"left__medium","confidence":0.8},"fire":{"choice":"shoot","confidence":0.7},"bomb":{"choice":"hold","confidence":0.9}},"usage":{"input_tokens":100,"output_tokens":12}}`))
			}
		}))
		upstream := newHTTPUpstream(djev.URL, apiKey)
		if outcome == "timeout" {
			upstream.client.Timeout = 100 * time.Millisecond
		}
		f.server.upstream = upstream
		runID = f.start()
		result, err := f.server.HandleDecision(context.Background(), decisionBody(t, runID))
		djev.Close()
		must(t, err)
		if (result["api_ok"] == true) != (outcome == "success") || (result["valid_choice"] == true) != (outcome == "success") {
			t.Fatalf("%s: %v", outcome, result)
		}
		records := f.records(runID)
		last := records[len(records)-1]
		if last["record_type"] != "decision_response" || last["exactrequestbody"] != string(wire) {
			t.Fatalf("%s: response record body mismatch", outcome)
		}
		trace, _ := os.ReadFile(filepath.Join(f.root, "runs", runID, "events.jsonl"))
		if strings.Contains(string(trace), apiKey) || strings.Contains(strings.ToLower(string(trace)), "authorization") {
			t.Fatal("trace leaked credentials")
		}
		if outcome == "timeout" && !strings.HasPrefix(result["error"].(string), "TimeoutError") {
			t.Fatalf("timeout error %q", result["error"])
		}
	}
}

// keysInWireOrder returns the object keys under a top-level field in the order they were serialized.
func keysInWireOrder(t *testing.T, wire []byte, field string) []string {
	t.Helper()
	var top map[string]json.RawMessage
	must(t, json.Unmarshal(wire, &top))
	decoder := json.NewDecoder(strings.NewReader(string(top[field])))
	var keys []string
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch v := token.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if depth == 1 { // at depth 1 every string token is a key; skip its value
				keys = append(keys, v)
				var skip json.RawMessage
				must(t, decoder.Decode(&skip))
			}
		}
	}
	return keys
}

func TestDecisionSendsCompactContextAndExcludesCheckpoint(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	raw := `{"model":"dgemma","answers":{"intent":{"choice":"position","confidence":0.6},"path":{"choice":"down_right__medium","confidence":0.7},"fire":{"choice":"shoot","confidence":0.8},"bomb":{"choice":"hold","confidence":0.9}},"usage":{"input_tokens":314,"output_tokens":16}}`
	f.upstream.handle = func([]byte) (*UpstreamResult, error) { return okResult(t, raw, 0.1895), nil }
	body := decisionBody(t, runID)
	result, err := f.server.HandleDecision(context.Background(), body)
	must(t, err)
	if result["intent"] != "position" || result["movement"] != "down_right" || result["fire"] != "shoot" || result["lease"] != "medium" {
		t.Fatalf("result %v", result)
	}
	assertJSON(t, result["usage"], map[string]any{"input_tokens": 314, "output_tokens": 16})
	assertJSON(t, result["api_token_throughput"], round(330/0.1895, 1))
	assertJSON(t, result["confidence"], map[string]any{"intent": 0.6, "path": 0.7, "movement": 0.7, "fire": 0.8, "lease": 0.7, "bomb": 0.9})

	wire := f.upstream.calls[0]
	payload := decode(t, string(wire))
	if !reflect.DeepEqual(sortedTop(payload), []string{"instructions", "model", "questions", "samples", "state", "steps"}) {
		t.Fatalf("payload keys %v", sortedTop(payload))
	}
	motion := map[string]string{"hold": "stationary", "right": "turns", "up": "continues", "down": "reverses",
		"up_left": "continues", "up_right": "continues", "down_left": "reverses", "down_right": "reverses"}
	want := map[string]any{}
	for _, movement := range ActionIDs {
		tier := "Tier 2 OK"
		if motion[movement] == "stationary" || motion[movement] == "reverses" {
			tier = "Tier 3 RISKY"
		}
		course := ""
		if motion[movement] == "continues" {
			course = ", keeps course"
		}
		label := fmt.Sprintf("%s: 6/9 escapes, tight gap, near enemy, busy space%s.", tier, course)
		if movement == "left" {
			label = "Tier 6 DEADLY: a threat hits the ship in 125 ms."
		}
		want[movement+"__medium"] = label
	}
	questions := payload["questions"].(map[string]any)
	assertJSON(t, questions["path"].(map[string]any)["criteria"], want)
	assertJSON(t, questions["fire"].(map[string]any)["criteria"], map[string]any{"shoot": "Fire gun and homing missiles.", "cease": "Do not fire."})
	assertJSON(t, questions["bomb"].(map[string]any)["criteria"], map[string]any{
		"hold":     "Rank 1 SAVE: a tier 2 move exists. Keeps 3 charges and 0 jets.",
		"detonate": "Rank 2 WASTE: a tier 2 move exists; destroys 4 bullets and 1 enemy ships within 200 px; 2 charges left after.",
	})
	assertJSON(t, payload["state"], decode(t, `{"enemy_count":6}`))
	for _, forbidden := range []string{"checkpoint", "rng_state", "seed", "old-decision"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("payload leaked %q", forbidden)
		}
	}
	if !strings.Contains(string(wire), `"state":{"enemy_count":6}`) {
		t.Fatal("state must reach the wire as the observed enemy count only")
	}
	records := f.records(runID)
	request := records[1]
	if request["prompt_version"] != PromptVersion || request["context_version"] != ContextVersion {
		t.Fatal("request record versions")
	}
	assertJSON(t, request["rawsnapshot"], body)
	assertJSON(t, request["exactactualpayload"], payload)
	response := records[2]
	if response["rawupstreamresponse"].(map[string]any)["body"] != raw || response["model_identity"].(map[string]any)["response_model"] != "dgemma" {
		t.Fatal("response record must keep raw body and response model")
	}
	assertJSON(t, response["normalized"], result)
}

func sortedTop(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func TestRemovedContextFieldsAreNotRequired(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	f.upstream.handle = func([]byte) (*UpstreamResult, error) {
		return okResult(t, `{"answers":{"intent":{"choice":"position"},"path":{"choice":"hold__medium"},"fire":{"choice":"cease"},"bomb":{"choice":"hold","confidence":0.9}}}`, 0.1), nil
	}
	body := decisionBody(t, runID)
	state := body["state"].(map[string]any)
	for _, key := range []string{"sim_ms", "wave", "difficulty", "active_command", "last_intent", "recent_commands", "recent_hits", "nearest_threats", "enemy_fire_in_ms"} {
		delete(state, key)
	}
	delete(state["threat_counts"].(map[string]any), "enemy_bullets")
	forecast := body["forecast"].(map[string]any)
	for _, key := range []string{"latency_samples", "latency_spread_ms", "horizon_ms"} {
		delete(forecast, key)
	}
	delete(forecast["prefix"].(map[string]any), "authorized_remaining_ms")
	for i := range ActionIDs {
		delete(medium(body, i), "enemy_clearance_px")
		delete(medium(body, i), "shot_eta_ms")
		delete(body["forecast"].(map[string]any)["candidates"].([]any)[i].(map[string]any), "short")
	}
	result, err := f.server.HandleDecision(context.Background(), body)
	must(t, err)
	if result["valid_choice"] != true || result["movement"] != "hold" {
		t.Fatalf("result %v", result)
	}
}

func TestValidationRejectsBeforeUpstream(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	mutations := []func(map[string]any){
		func(body map[string]any) {
			delete(body["forecast"].(map[string]any)["candidates"].([]any)[0].(map[string]any), "medium")
		},
		func(body map[string]any) {
			body["forecast"].(map[string]any)["assumptions"].(map[string]any)["contact_window"] = "whole_horizon"
		},
		func(body map[string]any) {
			body["forecast"].(map[string]any)["assumptions"].(map[string]any)["enemy_clearance_window"] = "prefix"
		},
	}
	for _, mutate := range mutations {
		body := decisionBody(t, runID)
		mutate(body)
		_, err := f.server.HandleDecision(context.Background(), body)
		expectAPIError(t, err, 400)
	}
	if f.upstream.callCount() != 0 {
		t.Fatal("invalid requests must not reach upstream")
	}
	records := f.records(runID)
	if records[len(records)-1]["record_type"] != "decision_validation_failed" {
		t.Fatal("validation failure must be traced")
	}
}

func TestEventBatchesAreIdempotentAndRejectGapsAndConflicts(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	one := `{"event_id":1,"epoch":3,"sequence":4,"tick":121,"sim_ms":2016.7,"wall_ms":2017,"type":"command_applied","payload":{"decision_id":"d1"}}`
	two := `{"event_id":2,"epoch":3,"sequence":null,"tick":122,"sim_ms":2033.3,"wall_ms":2035,"type":"hit","payload":{"lives_after":2}}`
	batch := func(events ...string) map[string]any {
		return decode(t, fmt.Sprintf(`{"schema_version":1,"run_id":%q,"events":[%s]}`, runID, strings.Join(events, ",")))
	}
	ack, err := f.server.RecordRunEvents(batch(one, two))
	must(t, err)
	assertJSON(t, ack["acked_event_id"], 2)
	before := f.records(runID)
	ack, err = f.server.RecordRunEvents(batch(one, two))
	must(t, err)
	assertJSON(t, ack["acked_event_id"], 2)
	if len(f.records(runID)) != len(before) {
		t.Fatal("repeated batch must not append")
	}
	_, err = f.server.RecordRunEvents(batch(strings.Replace(two, `"lives_after":2`, `"lives_after":1`, 1)))
	expectAPIError(t, err, 409)
	_, err = f.server.RecordRunEvents(batch(strings.Replace(two, `"event_id":2`, `"event_id":4`, 1)))
	expectAPIError(t, err, 409)
	ack, err = f.server.RecordRunEvents(batch(strings.Replace(two, `"event_id":2`, `"event_id":3`, 1)))
	must(t, err)
	assertJSON(t, ack["acked_event_id"], 3)
	records := f.records(runID)
	if records[1]["record_type"] != "client_events" || len(records[1]["hits"].([]any)) != 1 || len(records[1]["clientexecution"].([]any)) != 1 {
		t.Fatalf("client_events views %v", records[1])
	}
	_, err = ValidateEventBatch(decode(t, `{"schema_version":1,"events":[{"event_id":1,"epoch":0,"sequence":null,"tick":1,"sim_ms":1,"wall_ms":1,"type":"","payload":{}}]}`))
	expectAPIError(t, err, 400)
}

func TestConcurrentDecisionIsRejectedWithoutUpstreamCall(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	run, _ := f.server.getRun(runID)
	run.upstream <- struct{}{}
	defer func() { <-run.upstream }()
	_, err := f.server.HandleDecision(context.Background(), decisionBody(t, runID))
	expectAPIError(t, err, 409)
	if f.upstream.callCount() != 0 {
		t.Fatal("rejected decision must not call upstream")
	}
}

func TestRunEndRequiresAckedEventsAndMarksComplete(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	_, err := f.server.RecordRunEvents(decode(t, fmt.Sprintf(`{"schema_version":1,"run_id":%q,"events":[{"event_id":1,"epoch":3,"sequence":4,"tick":121,"sim_ms":2016.7,"wall_ms":2017,"type":"checkpoint","payload":{"hash":"abc"}}]}`, runID)))
	must(t, err)
	end := func(last int) (map[string]any, error) {
		return f.server.EndRun(map[string]any{"schema_version": json.Number("1"), "run_id": runID, "last_event_id": json.Number(fmt.Sprint(last)), "terminal": terminalBody()})
	}
	_, err = end(2)
	expectAPIError(t, err, 409)
	result, err := end(1)
	must(t, err)
	summary := result["summary"].(map[string]any)
	if result["complete"] != true || summary["status"] != "complete" || summary["terminal_reason"] != "target" {
		t.Fatalf("end result %v", result)
	}
	records := f.records(runID)
	if records[len(records)-1]["record_type"] != "run_ended" {
		t.Fatal("run_ended must be last")
	}
	_, err = f.server.HandleDecision(context.Background(), decisionBody(t, runID))
	expectAPIError(t, err, 409)
}

func TestRunEndWaitsForInflightDecision(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	_, err := f.server.RecordRunEvents(decode(t, fmt.Sprintf(`{"schema_version":1,"run_id":%q,"events":[{"event_id":1,"epoch":3,"sequence":4,"tick":121,"sim_ms":2016.7,"wall_ms":2017,"type":"checkpoint","payload":{"hash":"abc"}}]}`, runID)))
	must(t, err)
	started, release := make(chan struct{}), make(chan struct{})
	f.upstream.handle = func([]byte) (*UpstreamResult, error) {
		close(started)
		<-release
		return okResult(t, `{"model":"dgemma","answers":{"intent":{"choice":"evade"},"path":{"choice":"up__medium"},"fire":{"choice":"shoot"},"bomb":{"choice":"hold","confidence":0.9}}}`, 0.4895), nil
	}
	decisionDone, endDone := make(chan map[string]any, 1), make(chan map[string]any, 1)
	go func() {
		result, err := f.server.HandleDecision(context.Background(), decisionBody(t, runID))
		if err != nil {
			t.Error(err)
		}
		decisionDone <- result
	}()
	<-started
	go func() {
		result, err := f.server.EndRun(map[string]any{"schema_version": json.Number("1"), "run_id": runID, "last_event_id": json.Number("1"), "terminal": terminalBody()})
		if err != nil {
			t.Error(err)
		}
		endDone <- result
	}()
	select {
	case <-endDone:
		t.Fatal("end_run returned before the in-flight decision finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	decision, end := <-decisionDone, <-endDone
	if decision["movement"] != "up" || end["complete"] != true {
		t.Fatalf("decision %v end %v", decision, end)
	}
	var types []string
	for _, record := range f.records(runID) {
		types = append(types, record["record_type"].(string))
	}
	if strings.Join(types, ",") != "run_started,client_events,decision_request,decision_response,run_ended" {
		t.Fatalf("record order %v", types)
	}
}

func TestDecisionRechecksCompleteAfterAcquiringLock(t *testing.T) {
	f := newFixture(t)
	runID := f.start()
	f.server.afterUpstreamLock = func(run *RunState) {
		run.eventMu.Lock()
		run.complete = true
		run.eventMu.Unlock()
	}
	_, err := f.server.HandleDecision(context.Background(), decisionBody(t, runID))
	expectAPIError(t, err, 409)
	if f.upstream.callCount() != 0 {
		t.Fatal("completed run must not call upstream")
	}
}

func TestHTTPRoutesAndErrors(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(f.server.Handler())
	defer server.Close()
	post := func(path, body string) (int, map[string]any) {
		response, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
		must(t, err)
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, decode(t, string(raw))
	}
	status, body := post("/api/run/start", `{"schema_version":2}`)
	if status != 400 || body["code"] != "bad_request" {
		t.Fatalf("%d %v", status, body)
	}
	status, body = post("/api/decision", `not json`)
	if status != 400 {
		t.Fatalf("%d %v", status, body)
	}
	status, body = post("/api/run/event", `{"schema_version":1,"run_id":"missing","events":[]}`)
	if status != 404 || body["code"] != "not_found" {
		t.Fatalf("%d %v", status, body)
	}
	status, _ = post("/api/unknown", `{}`)
	if status != 404 {
		t.Fatal("unknown route")
	}
	response, err := http.Get(server.URL + "/")
	must(t, err)
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(page), "space-decision-core") || response.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("page not served")
	}
	response, err = http.Get(server.URL + "/health")
	must(t, err)
	health, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(health) != `{"ok":true}` {
		t.Fatalf("health %s", health)
	}
}

func TestSafeEndpointStripsCredentialsAndQuery(t *testing.T) {
	if got := safeEndpoint("http://user:pw@127.0.0.1:8011/api/?key=secret"); got != "http://127.0.0.1:8011/api" {
		t.Fatalf("%q", got)
	}
}

func TestRoundMatchesPython(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{{2.675, 2.67}, {0.125, 0.12}, {1741.4, 1741.4}, {-0.05, -0.1}} {
		digits := 2
		if tc.in == 1741.4 || tc.in == -0.05 {
			digits = 1
		}
		if got := round(tc.in, digits); got != tc.want {
			t.Fatalf("round(%v,%d)=%v want %v", tc.in, digits, got, tc.want)
		}
	}
}

func TestHomeZoneTierAndTags(t *testing.T) {
	row := func(startX, startY, endX, endY float64) PathRow {
		return PathRow{EscapeOptions: 9, WallRoom: 200, Motion: "continues",
			ZoneStartOut: zoneOutside(startX, startY), ZoneEndOut: zoneOutside(endX, endY)}
	}
	cases := []struct {
		name string
		r    PathRow
		tier string
		tag  string
	}{
		{"inside stays inside", row(480, 300, 500, 300), "GOOD", ""},
		{"inside leaves", row(480, 530, 480, 580), "OK", "leaves zone"},
		{"outside returns", row(480, 600, 480, 560), "GOOD", "back to zone"},
		{"outside re-enters from just outside", row(480, 548, 480, 530), "GOOD", "back to zone"},
		{"outside drifts along", row(480, 600, 520, 600), "OK", "outside zone"},
		{"outside moves further out", row(60, 300, 20, 300), "OK", "outside zone"},
	}
	for _, tc := range cases {
		if got := pathTier(tc.r); got != tc.tier {
			t.Errorf("%s: tier %s, want %s", tc.name, got, tc.tier)
		}
		label := pathLabel(tc.r)
		if tc.tag != "" && !strings.Contains(label, tc.tag) {
			t.Errorf("%s: label %q lacks %q", tc.name, label, tc.tag)
		}
		if tc.tag == "" && strings.Contains(label, "zone") {
			t.Errorf("%s: label %q mentions the zone", tc.name, label)
		}
	}
	// The zone never makes a move RISKY: an OK move outside the zone stays OK.
	okOutside := row(480, 600, 520, 600)
	okOutside.EscapeOptions = 3
	if got := pathTier(okOutside); got != "OK" {
		t.Errorf("an OK move outside the zone became %s", got)
	}
	// Danger tiers ignore the zone.
	deadly := row(480, 600, 480, 610)
	ms := 120.0
	deadly.MoveCollisionMs = &ms
	if pathTier(deadly) != "DEADLY" {
		t.Error("zone rule must not touch DEADLY")
	}
}

func TestFutureTiersLabelsAndOracle(t *testing.T) {
	row := func(survival float64, clear int64, startX, startY, endX, endY float64) PathRow {
		return PathRow{WallRoom: 200, Motion: "turns", Futures: &FutureRow{Survival: survival, Clear: clear, Futures: 4},
			ZoneStartOut: stationOutside(startX, startY), ZoneEndOut: stationOutside(endX, endY)}
	}
	cases := []struct {
		name  string
		r     PathRow
		tier  string
		label string
	}{
		{"all futures, in station", row(1, 4, 480, 530, 470, 530), "GOOD", "Tier 1 GOOD: survives 4/4 futures."},
		{"all futures, leaves station", row(1, 4, 480, 440, 480, 420), "OK", "Tier 2 OK: survives 4/4 futures, leaves zone."},
		{"all futures, heads back", row(1, 4, 480, 380, 480, 400), "GOOD", "Tier 1 GOOD: survives 4/4 futures, back to zone."},
		{"most futures", row(0.95, 3, 480, 530, 470, 530), "OK", "Tier 2 OK: survives 3/4 futures."},
		{"some futures", row(0.7, 2, 480, 530, 470, 530), "RISKY", "Tier 3 RISKY: survives 2/4 futures."},
		{"mostly hit", row(0.4, 0, 480, 530, 470, 530), "DOOMED", "Tier 5 DOOMED: survives 0/4 futures, mostly hit."},
		{"every future hit", row(0, 0, 480, 530, 470, 530), "DEADLY", "Tier 6 DEADLY: the ship is hit in every sampled future."},
	}
	for _, tc := range cases {
		if got := pathTier(tc.r); got != tc.tier {
			t.Errorf("%s: tier %s, want %s", tc.name, got, tc.tier)
		}
		if got := pathLabel(tc.r); got != tc.label {
			t.Errorf("%s: label %q, want %q", tc.name, got, tc.label)
		}
	}
	// The oracle ranks by tier, then futures survived.
	criteria := map[string]any{
		"a": pathLabel(row(0.95, 3, 480, 530, 470, 530)),
		"b": pathLabel(row(1, 4, 480, 530, 470, 530)),
		"c": pathLabel(row(0.7, 2, 480, 530, 470, 530)),
	}
	if got := oracleChoice(criteria, []string{"a", "b", "c"}); got != "b" {
		t.Errorf("oracle picked %s, want b", got)
	}
}
