package main

import (
	"fmt"
	"math"
	"strings"
)

const (
	SchemaVersion        = 1
	PromptVersion        = "djev-authoritative-v6"
	ContextVersion       = "djev-observation-v6"
	MaxModelLen          = 4096
	ReservedOutputTokens = 512
	DtMs                 = 1000.0 / 60.0
	ArenaCenterX         = 480.0
	ArenaCenterY         = 310.0
	LivePathLease        = "medium"
)

var (
	ActionIDs   = []string{"hold", "left", "right", "up", "down", "up_left", "up_right", "down_left", "down_right"}
	FireIDs     = []string{"shoot", "cease"}
	IntentIDs   = []string{"evade", "recover", "position"}
	BombIDs     = []string{"hold", "detonate"}
	PickupKinds = []string{"bomb", "weapon", "wingman", "missile"}
	PathIDs     = func() []string {
		ids := make([]string, len(ActionIDs))
		for i, movement := range ActionIDs {
			ids[i] = movement + "__" + LivePathLease
		}
		return ids
	}()
	FireDescriptions   = map[string]string{"shoot": "Fire gun and homing missiles.", "cease": "Do not fire."}
	IntentDescriptions = map[string]string{
		"evade":    "hold_collision=true.",
		"recover":  "hold_collision=false and inside_center_region=false.",
		"position": "hold_collision=false and inside_center_region=true.",
	}
	directionVectors = map[string][2]int{
		"hold": {0, 0}, "left": {-1, 0}, "right": {1, 0}, "up": {0, -1}, "down": {0, 1},
		"up_left": {-1, -1}, "up_right": {1, -1}, "down_left": {-1, 1}, "down_right": {1, 1},
	}
)

// MaxPackedStateChars is a variable so tests can force the context-budget path.
var MaxPackedStateChars = 6000

// Tier numbers lead each label: Djev compares a leading rank far more reliably than prose or raw numbers.
var pathTiers = map[string]int{"GOOD": 1, "OK": 2, "RISKY": 3, "TRAP": 4, "DOOMED": 5, "DEADLY": 6}

const (
	openGapPx        = 40
	tightGapPx       = 15
	trapWallRoomPx   = 25
	nearWallRoomPx   = 70
	nearEnemyPx      = 110
	goodMinEscapes   = 4
	okMinEscapes     = 2
	centerProgressPx = 15
	pathInstructions = "Pick the move that keeps the ship alive. Each move starts with its tier number: 1 is best, 6 is worst. Always pick a move with the lowest tier number present. Within that tier prefer more escapes, then keeps course, then not near enemy, then open space, then toward center. If every move is tier 6, pick the one hit latest."
	fireInstructions = "Choose shoot if enemy_count>0, otherwise cease. Shoot also launches homing missiles."
	bombInstructions = "Pick the choice with rank 1. Collect floating bomb pickups to refill charges."
	// bombUrgentTier is the best path tier at which the detonate label reports that no safe move exists.
	bombUrgentTier    = 5
	intentInstruction = "Classify current intent."
)

var motionWords = map[string]string{"stationary": "stationary", "starts": "starts moving", "continues": "continues", "turns": "turns", "reverses": "reverses"}

// DecisionRequest is a validated /api/decision body; Raw is logged verbatim.
type DecisionRequest struct {
	Raw          map[string]any
	RunID        string
	Epoch        int64
	Sequence     int64
	SnapshotTick int64
	State        map[string]any
	Forecast     map[string]any
}

func validateDecisionRequest(body any) (*DecisionRequest, error) {
	object, err := requireSchema(body)
	if err != nil {
		return nil, err
	}
	runID, ok := object["run_id"].(string)
	if !ok || runID == "" {
		return nil, validationError("run_id must be a nonempty string")
	}
	request := &DecisionRequest{Raw: object, RunID: runID}
	if request.Epoch, err = nonnegativeInt(object["epoch"], "epoch"); err != nil {
		return nil, err
	}
	if request.Sequence, err = nonnegativeInt(object["sequence"], "sequence"); err != nil {
		return nil, err
	}
	if request.SnapshotTick, err = nonnegativeInt(object["snapshot_tick"], "snapshot_tick"); err != nil {
		return nil, err
	}
	if _, err = finiteNumber(object["client_send_wall_ms"], "client_send_wall_ms", false, min0()); err != nil {
		return nil, err
	}
	if request.State, err = requireObject(object["state"], "state"); err != nil {
		return nil, err
	}
	if request.Forecast, err = requireObject(object["forecast"], "forecast"); err != nil {
		return nil, err
	}
	if _, present := object["checkpoint"]; !present {
		return nil, validationError("checkpoint is required for trace logging")
	}
	return request, nil
}

// activeMovement validates state.active_command and returns its movement ("" when absent).
func activeMovement(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	command, err := requireObject(value, "active_command")
	if err != nil {
		return "", err
	}
	movement, _, err := enumValue(command["movement"], "active_command.movement", ActionIDs, false)
	if err != nil {
		return "", err
	}
	if _, _, err = enumValue(command["intent"], "active_command.intent", IntentIDs, true); err != nil {
		return "", err
	}
	if remaining, present := command["remaining_ms"]; present {
		if _, err = finiteNumber(remaining, "active_command.remaining_ms", false, min0()); err != nil {
			return "", err
		}
	} else if endTick, present := command["end_tick"]; present {
		if _, err = nonnegativeInt(endTick, "active_command.end_tick"); err != nil {
			return "", err
		}
	} else {
		return "", validationError("active_command.remaining_ms or end_tick is required")
	}
	return movement, nil
}

type packedPlayer struct {
	ordered *OrderedMap
	x, y    float64
}

func packPlayer(value any) (*packedPlayer, error) {
	player, err := requireObject(value, "player")
	if err != nil {
		return nil, err
	}
	out := NewOrderedMap()
	result := &packedPlayer{ordered: out}
	fields := []struct {
		key     string
		minimum *float64
	}{{"x", nil}, {"y", nil}, {"w", min0()}, {"h", min0()}}
	for _, field := range fields {
		raw, number, err := observedNumber(player, field.key, "player", false, field.minimum)
		if err != nil {
			return nil, err
		}
		out.Set(field.key, raw)
		switch field.key {
		case "x":
			result.x = *number
		case "y":
			result.y = *number
		}
	}
	if _, err := nonnegativeInt(player["lives"], "player.lives"); err != nil {
		return nil, err
	}
	out.Set("lives", player["lives"])
	for _, key := range []string{"cooldown_ms", "invulnerability_ms"} {
		raw, _, err := observedNumber(player, key, "player", false, min0())
		if err != nil {
			return nil, err
		}
		out.Set(key, raw)
	}
	return result, nil
}

func edgeRoom(value any, name string) (float64, error) {
	var distances []float64
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"left", "right", "top", "bottom"} {
			_, number, err := observedNumber(typed, key, name, false, min0())
			if err != nil {
				return 0, err
			}
			distances = append(distances, *number)
		}
	case []any:
		if len(typed) != 4 {
			return 0, validationError("%s must contain four edge distances", name)
		}
		for i, item := range typed {
			number, err := finiteNumber(item, fmt.Sprintf("%s[%d]", name, i), false, min0())
			if err != nil {
				return 0, err
			}
			distances = append(distances, *number)
		}
	default:
		return 0, validationError("%s must contain four edge distances", name)
	}
	room := distances[0]
	for _, distance := range distances[1:] {
		room = math.Min(room, distance)
	}
	return room, nil
}

func candidateForecasts(value any) (map[string]map[string]any, error) {
	candidates, err := requireList(value, "forecast.candidates")
	if err != nil {
		return nil, err
	}
	if len(candidates) != len(ActionIDs) {
		return nil, validationError("forecast.candidates must contain exactly nine movement candidates")
	}
	byID := map[string]map[string]any{}
	for i, item := range candidates {
		candidate, err := requireObject(item, fmt.Sprintf("forecast.candidates[%d]", i))
		if err != nil {
			return nil, err
		}
		movement, _, err := enumValue(candidate["id"], fmt.Sprintf("forecast.candidates[%d].id", i), ActionIDs, false)
		if err != nil {
			return nil, err
		}
		if _, dup := byID[movement]; dup {
			return nil, validationError("candidate %s is duplicated", movement)
		}
		byID[movement] = candidate
	}
	var missing []string
	for _, movement := range ActionIDs {
		if _, ok := byID[movement]; !ok {
			missing = append(missing, movement)
		}
	}
	if len(missing) > 0 {
		return nil, validationError("missing movement candidates: %s", strings.Join(missing, ", "))
	}
	return byID, nil
}

// motionRelation says how a candidate relates to the executing command: aimed shots target where the ship was.
func motionRelation(movement, current string) string {
	if movement == "hold" {
		return "stationary"
	}
	c := directionVectors[current]
	if current == "" || c == [2]int{0, 0} {
		return "starts"
	}
	m := directionVectors[movement]
	dot := c[0]*m[0] + c[1]*m[1]
	switch {
	case dot > 0:
		return "continues"
	case dot < 0:
		return "reverses"
	}
	return "turns"
}

// PathRow holds the validated facts for one medium-lease movement candidate.
type PathRow struct {
	Path            string
	Collision       bool
	GapRaw          any // original clearance_px value (nil when null)
	WallRoom        float64
	CenterProgress  float64
	Crowd           int64
	MoveCollisionMs *float64
	EscapeOptions   int64
	EscapeGapPx     *float64
	EnemyGapPx      *float64
	Motion          string
	PickupCollect   string // wanted pickup this move's path touches, or ""
	PickupToward    string // wanted pickup this move closes in on, or ""
	PickupWanted    bool   // any move collects or approaches a wanted pickup
}

func pathTable(value any, player *packedPlayer, currentMovement string) ([]PathRow, error) {
	byID, err := candidateForecasts(value)
	if err != nil {
		return nil, err
	}
	centerDistance := math.Hypot(player.x-ArenaCenterX, player.y-ArenaCenterY)
	rows := make([]PathRow, 0, len(ActionIDs))
	for _, movement := range ActionIDs {
		name := fmt.Sprintf("candidate %s.%s", movement, LivePathLease)
		prediction, err := requireObject(byID[movement][LivePathLease], name)
		if err != nil {
			return nil, err
		}
		endpoint, err := requireObject(prediction["endpoint"], name+".endpoint")
		if err != nil {
			return nil, err
		}
		_, endX, err := observedNumber(endpoint, "x", name+".endpoint", false, nil)
		if err != nil {
			return nil, err
		}
		_, endY, err := observedNumber(endpoint, "y", name+".endpoint", false, nil)
		if err != nil {
			return nil, err
		}
		_, contact, err := observedNumber(prediction, "contact_ms", name, true, min0())
		if err != nil {
			return nil, err
		}
		gapRaw, _, err := observedNumber(prediction, "clearance_px", name, true, nil)
		if err != nil {
			return nil, err
		}
		wallRoom, err := edgeRoom(prediction["edge_distances_px"], name+".edge_distances_px")
		if err != nil {
			return nil, err
		}
		crowd, err := nonnegativeInt(prediction["crowd_count"], name+".crowd_count")
		if err != nil {
			return nil, err
		}
		_, moveContact, err := observedNumber(prediction, "move_contact_ms", name, true, min0())
		if err != nil {
			return nil, err
		}
		escapes, err := nonnegativeInt(prediction["escape_options"], name+".escape_options")
		if err != nil {
			return nil, err
		}
		_, escapeGap, err := observedNumber(prediction, "escape_clearance_px", name, true, nil)
		if err != nil {
			return nil, err
		}
		var enemyGap *float64
		if _, present := prediction["enemy_clearance_px"]; present {
			if _, enemyGap, err = observedNumber(prediction, "enemy_clearance_px", name, true, nil); err != nil {
				return nil, err
			}
		}
		collect, _, err := enumValue(prediction["pickup_collect"], name+".pickup_collect", PickupKinds, true)
		if err != nil {
			return nil, err
		}
		toward, _, err := enumValue(prediction["pickup_toward"], name+".pickup_toward", PickupKinds, true)
		if err != nil {
			return nil, err
		}
		rows = append(rows, PathRow{
			PickupCollect:   collect,
			PickupToward:    toward,
			Path:            movement + "__" + LivePathLease,
			Collision:       contact != nil,
			GapRaw:          gapRaw,
			WallRoom:        wallRoom,
			CenterProgress:  round(centerDistance-math.Hypot(*endX-ArenaCenterX, *endY-ArenaCenterY), 1),
			Crowd:           crowd,
			MoveCollisionMs: moveContact,
			EscapeOptions:   escapes,
			EscapeGapPx:     escapeGap,
			EnemyGapPx:      enemyGap,
			Motion:          motionRelation(movement, currentMovement),
		})
	}
	wanted := false
	for _, row := range rows {
		wanted = wanted || row.PickupCollect != "" || row.PickupToward != ""
	}
	for i := range rows {
		rows[i].PickupWanted = wanted
	}
	return rows, nil
}

func (row PathRow) nearEnemy() bool { return row.EnemyGapPx != nil && *row.EnemyGapPx < nearEnemyPx }

func pathTier(row PathRow) string {
	switch {
	case row.MoveCollisionMs != nil:
		return "DEADLY"
	case row.EscapeOptions == 0:
		return "DOOMED"
	case row.WallRoom < trapWallRoomPx:
		return "TRAP"
	}
	gap := row.EscapeGapPx
	tier := "RISKY"
	if row.EscapeOptions >= goodMinEscapes && (gap == nil || *gap >= openGapPx) && !row.nearEnemy() {
		tier = "GOOD"
	} else if row.EscapeOptions >= okMinEscapes && (gap == nil || *gap >= tightGapPx) {
		tier = "OK"
	}
	// Aimed shots converge where the ship was, and a wall halves the escape directions: holding
	// still, reversing, or ending near a wall costs one tier. Djev follows the leading tier
	// number reliably but largely ignores within-tier word preferences.
	if row.Motion == "stationary" || row.Motion == "reverses" || row.WallRoom < nearWallRoomPx {
		tier = shiftTier(tier, 1)
	}
	// Supplies are a strategic goal, so they live in the rank Djev follows: collecting a wanted
	// pickup lifts a safe move one tier, and while one is reachable, safe moves that ignore it
	// drop one. With only the collect bonus Djev gathered 24 of 96 pickups and survived 3/6
	// lockstep runs; with pursuit, 41 of 101 and 5/6.
	switch {
	case isPowerUp(row.PickupCollect):
		// Upgrading firepower is the proactive goal: a weapon, escort-jet, or missile block lifts a safe move two tiers.
		tier = shiftTier(tier, -2)
	case row.PickupCollect != "":
		tier = shiftTier(tier, -1)
	case row.PickupWanted && row.PickupToward == "":
		tier = shiftTier(tier, 1)
	}
	return tier
}

// isPowerUp reports whether a pickup kind upgrades firepower (everything but a bomb charge).
func isPowerUp(kind string) bool {
	return kind == "weapon" || kind == "wingman" || kind == "missile"
}

// shiftTier moves among the safe tiers only: GOOD, OK, RISKY.
func shiftTier(tier string, delta int) string {
	order := []string{"GOOD", "OK", "RISKY"}
	for i, name := range order {
		if name == tier {
			j := i + delta
			if j < 0 {
				j = 0
			}
			if j >= len(order) {
				j = len(order) - 1
			}
			return order[j]
		}
	}
	return tier
}

// pathLabel translates one path forecast into facts the model can compare without arithmetic.
func pathLabel(row PathRow) string {
	tier := pathTier(row)
	prefix := fmt.Sprintf("Tier %d %s", pathTiers[tier], tier)
	switch tier {
	case "DEADLY":
		return fmt.Sprintf("%s: a threat hits the ship in %d ms.", prefix, int64(*row.MoveCollisionMs))
	case "DOOMED":
		return prefix + ": safe now, but every follow-up move is hit."
	case "TRAP":
		return prefix + ": ends pinned against the wall with no escape room."
	}
	gapWord := "open gap"
	if gap := row.EscapeGapPx; gap != nil && *gap < openGapPx {
		gapWord = "tight gap"
		if *gap < tightGapPx {
			gapWord = "grazing gap"
		}
	}
	parts := []string{fmt.Sprintf("%d/9 escapes", row.EscapeOptions), gapWord}
	if row.nearEnemy() {
		parts = append(parts, "near enemy")
	}
	if row.WallRoom < nearWallRoomPx {
		parts = append(parts, "near wall")
	}
	if isPowerUp(row.PickupCollect) {
		parts = append(parts, "collects "+row.PickupCollect+" (power up)")
	} else if row.PickupCollect != "" {
		parts = append(parts, "collects "+row.PickupCollect)
	} else if row.PickupToward != "" {
		parts = append(parts, "toward "+row.PickupToward)
	}
	switch {
	case row.Crowd <= 1:
		parts = append(parts, "open space")
	case row.Crowd <= 3:
		parts = append(parts, "busy space")
	default:
		parts = append(parts, "crowded")
	}
	// Julia 1 weighs every motion word about as heavily as the safety tier (for example it
	// rates "stationary" above "continues"), so only the course-keeping fact is stated.
	if row.Motion == "continues" {
		parts = append(parts, "keeps course")
	}
	if row.CenterProgress > centerProgressPx {
		parts = append(parts, "toward center")
	}
	return fmt.Sprintf("%s: %s.", prefix, strings.Join(parts, ", "))
}

// buildPathCriteria returns the path labels and the best (lowest) tier among them.
func buildPathCriteria(request *DecisionRequest) (*OrderedMap, int, error) {
	current, err := activeMovement(request.State["active_command"])
	if err != nil {
		return nil, 0, err
	}
	player, err := packPlayer(request.State["player"])
	if err != nil {
		return nil, 0, err
	}
	rows, err := pathTable(request.Forecast["candidates"], player, current)
	if err != nil {
		return nil, 0, err
	}
	criteria := NewOrderedMap()
	best := len(pathTiers)
	for _, row := range rows {
		criteria.Set(row.Path, pathLabel(row))
		if tier := pathTiers[pathTier(row)]; tier < best {
			best = tier
		}
	}
	return criteria, best, nil
}

// BombFacts is the validated state.bomb observation.
type BombFacts struct {
	Charges, MaxCharges, RadiusPx, Bullets, Enemies int64
	SacrificeJets                                   int64 // escort jets that can self-destruct once bombs run out
}

func bombFacts(value any) (BombFacts, error) {
	object, err := requireObject(value, "bomb")
	if err != nil {
		return BombFacts{}, err
	}
	var facts BombFacts
	for key, target := range map[string]*int64{"charges": &facts.Charges, "max_charges": &facts.MaxCharges, "radius_px": &facts.RadiusPx, "bullets_in_radius": &facts.Bullets, "enemies_in_radius": &facts.Enemies} {
		if *target, err = nonnegativeInt(object[key], "bomb."+key); err != nil {
			return BombFacts{}, err
		}
	}
	if value, present := object["sacrifice_jets"]; present {
		if facts.SacrificeJets, err = nonnegativeInt(value, "bomb.sacrifice_jets"); err != nil {
			return BombFacts{}, err
		}
	}
	return facts, nil
}

// buildBombCriteria states what detonating would do now. Like paths, each choice leads with a
// rank: detonate ranks first only when no move beats tier 5 and a blast would destroy something.
func buildBombCriteria(facts BombFacts, bestTier int) *OrderedMap {
	criteria := NewOrderedMap()
	if facts.Charges == 0 && facts.SacrificeJets == 0 {
		criteria.Set("hold", "Rank 1: no charges left.")
		criteria.Set("detonate", "Rank 2: no charges left; detonating does nothing.")
		return criteria
	}
	cost := fmt.Sprintf("%d charges left after", facts.Charges-1)
	if facts.Charges == 0 {
		cost = fmt.Sprintf("no bombs left, so one escort jet self-destructs (%d jets left after)", facts.SacrificeJets-1)
	}
	blast := fmt.Sprintf("destroys %d bullets and %d enemy ships within %d px; %s",
		facts.Bullets, facts.Enemies, facts.RadiusPx, cost)
	if bestTier >= bombUrgentTier && facts.Bullets+facts.Enemies > 0 {
		criteria.Set("hold", fmt.Sprintf("Rank 2 WAIT: every move is tier 5 or 6; the ship is likely hit. Keeps %d charges and %d jets.", facts.Charges, facts.SacrificeJets))
		criteria.Set("detonate", "Rank 1 USE NOW: every move is tier 5 or 6; "+blast+".")
		return criteria
	}
	reason := fmt.Sprintf("a tier %d move exists", bestTier)
	if facts.Bullets+facts.Enemies == 0 {
		reason = "nothing is in range"
	}
	criteria.Set("hold", fmt.Sprintf("Rank 1 SAVE: %s. Keeps %d charges and %d jets.", reason, facts.Charges, facts.SacrificeJets))
	criteria.Set("detonate", fmt.Sprintf("Rank 2 WASTE: %s; %s.", reason, blast))
	return criteria
}

// ContextBudgetExceeded is returned (not raised to HTTP) as a null decision.
type ContextBudgetExceeded struct{ Message string }

func (e *ContextBudgetExceeded) Error() string { return e.Message }

func packModelContext(request *DecisionRequest) (*OrderedMap, error) {
	prefix, err := requireObject(request.Forecast["prefix"], "forecast.prefix")
	if err != nil {
		return nil, err
	}
	assumptions, err := requireObject(request.Forecast["assumptions"], "forecast.assumptions")
	if err != nil {
		return nil, err
	}
	if assumptions["enemy_motion"] != "current_linear" {
		return nil, validationError("forecast.assumptions.enemy_motion must be current_linear")
	}
	if spawns, ok := assumptions["future_spawns_included"].(bool); !ok || spawns {
		return nil, validationError("forecast.assumptions.future_spawns_included must be false")
	}
	if assumptions["contact_window"] != "after_arrival_while_vulnerable" {
		return nil, validationError("forecast.assumptions.contact_window must be after_arrival_while_vulnerable")
	}
	if assumptions["enemy_clearance_window"] != "after_arrival" {
		return nil, validationError("forecast.assumptions.enemy_clearance_window must be after_arrival")
	}
	player, err := packPlayer(request.State["player"])
	if err != nil {
		return nil, err
	}
	rows, err := pathTable(request.Forecast["candidates"], player, "")
	if err != nil {
		return nil, err
	}
	counts, err := requireObject(request.State["threat_counts"], "threat_counts")
	if err != nil {
		return nil, err
	}
	if _, _, err := observedNumber(request.Forecast, "expected_delay_ms", "forecast", false, min0()); err != nil {
		return nil, err
	}
	if _, _, err := observedNumber(prefix, "contact_ms", "forecast.prefix", true, min0()); err != nil {
		return nil, err
	}
	if _, err := nonnegativeInt(counts["enemies"], "threat_counts.enemies"); err != nil {
		return nil, err
	}
	if _, err := bombFacts(request.State["bomb"]); err != nil {
		return nil, err
	}
	_ = rows
	// Every choice carries its own facts in its labels, so the model state is only what the fire
	// question needs. Djev's latency grows sharply with numeric state: the former eight-field
	// state cost about 80 ms per call (191 ms vs 108 ms) with no gain in choice quality.
	packed := NewOrderedMap().Set("enemy_count", counts["enemies"])
	encoded, err := marshalCompact(packed)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxPackedStateChars {
		return nil, &ContextBudgetExceeded{fmt.Sprintf("packed model state is %d chars, limit is %d", len(encoded), MaxPackedStateChars)}
	}
	return packed, nil
}

func choiceCriteria(ids []string, descriptions map[string]string) *OrderedMap {
	criteria := NewOrderedMap()
	for _, id := range ids {
		criteria.Set(id, descriptions[id])
	}
	return criteria
}

func buildUpstreamPayload(model, promptText string, packed, pathCriteria, bombCriteria *OrderedMap, flavor string) *OrderedMap {
	question := func(instructions string, criteria *OrderedMap) *OrderedMap {
		return NewOrderedMap().Set("type", "choice").Set("instructions", instructions).Set("criteria", criteria)
	}
	path := question(pathInstructions, pathCriteria)
	bomb := question(bombInstructions, bombCriteria)
	if flavor == "julia" {
		path.Set("option_questions", pathOptionQuestions())
		bomb.Set("option_questions", bombOptionQuestions())
	}
	payload := NewOrderedMap().
		Set("model", model).
		Set("instructions", promptText).
		Set("state", packed).
		Set("questions", NewOrderedMap().
			Set("path", path).
			Set("fire", question(fireInstructions, choiceCriteria(FireIDs, FireDescriptions))).
			Set("bomb", bomb))
	if flavor != "laya" {
		payload.Set("samples", 1).Set("steps", 1)
	}
	return payload
}

// pathOptionQuestions asks Julia 1 for two judgements of every path label, which the
// julia-api combines as a weighted sum. "Right answer" picks well among safe moves but
// ranks bad moves in the wrong order; the outcome score keeps danger ordered, so the
// pair avoids deadly moves when an escape exists. Weight 3 was tuned offline on
// recorded states (holdout: 100% deadly avoidance in crises, ~80% best tier overall).
func pathOptionQuestions() []any {
	return []any{
		NewOrderedMap().Set("type", "noul").Set("instructions", "Is this the right answer?").Set("weight", 1),
		NewOrderedMap().Set("type", "score").Set("instructions", "What happens to the ship?").
			Set("levels", []any{"the ship is destroyed", "the ship is trapped", "the ship survives with difficulty", "the ship is completely safe"}).
			Set("weight", 3),
	}
}

// bombOptionQuestions stops Julia 1 wasting bombs: "right answer" alone detonated on
// "Rank 2 WASTE ... destroys N bullets" labels (the word "destroys" attracts it). Adding
// "Is this the rank 1 choice?" and the outcome score held on 151/151 recorded wasted-bomb
// states and still detonated in 96% of "Rank 1 USE NOW" emergencies (offline, 2026-09-29).
func bombOptionQuestions() []any {
	return []any{
		NewOrderedMap().Set("type", "noul").Set("instructions", "Is this the right answer?").Set("weight", 1),
		NewOrderedMap().Set("type", "noul").Set("instructions", "Is this the rank 1 choice?").Set("weight", 2),
		NewOrderedMap().Set("type", "score").Set("instructions", "What happens to the ship?").
			Set("levels", []any{"the ship is destroyed", "the ship is trapped", "the ship survives with difficulty", "the ship is completely safe"}).
			Set("weight", 3),
	}
}

type answer struct {
	choice     string
	confidence any // *float64 rounded, or nil
	err        string
}

func extractAnswer(response any, name string, allowed []string) answer {
	root, ok := response.(map[string]any)
	if !ok {
		return answer{err: "invalid_root"}
	}
	answers, ok := root["answers"].(map[string]any)
	if !ok {
		return answer{err: "invalid_answers"}
	}
	raw, present := answers[name]
	if !present || raw == nil {
		return answer{err: "missing_" + name}
	}
	item, ok := raw.(map[string]any)
	if !ok {
		return answer{err: "invalid_" + name + "_answer"}
	}
	choice, ok := item["choice"].(string)
	if !ok {
		return answer{err: "invalid_" + name + "_choice_type"}
	}
	choice = strings.ToLower(strings.TrimSpace(choice))
	valid := false
	for _, candidate := range allowed {
		if choice == candidate {
			valid = true
			break
		}
	}
	if !valid {
		return answer{err: "unknown_" + name + "_choice"}
	}
	var confidence any
	if number, ok := numberValue(item["confidence"]); ok {
		confidence = round(math.Max(0, math.Min(1, number)), 3)
	}
	return answer{choice: choice, confidence: confidence}
}

func nilIfEmpty(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func nullConfidence() map[string]any {
	return map[string]any{"intent": nil, "path": nil, "movement": nil, "fire": nil, "lease": nil, "bomb": nil}
}

// normalizeDecisionResponse validates the path, fire, and bomb answers atomically. Intent is no
// longer asked (it never steered the game and cost latency); a stray intent answer must still be valid.
func normalizeDecisionResponse(response any) map[string]any {
	intent := extractAnswer(response, "intent", IntentIDs)
	if intent.err == "missing_intent" {
		intent = answer{}
	}
	path := extractAnswer(response, "path", PathIDs)
	fire := extractAnswer(response, "fire", FireIDs)
	bomb := extractAnswer(response, "bomb", BombIDs)
	var errors []string
	for _, item := range []answer{intent, path, fire, bomb} {
		if item.err != "" {
			errors = append(errors, item.err)
		}
	}
	if len(errors) > 0 {
		return map[string]any{
			"intent": nil, "movement": nil, "fire": nil, "lease": nil, "bomb": nil, "valid_choice": false,
			"confidence": nullConfidence(), "error": strings.Join(errors, ","),
		}
	}
	separator := strings.LastIndex(path.choice, "__")
	return map[string]any{
		"intent":       nilIfEmpty(intent.choice),
		"movement":     path.choice[:separator],
		"fire":         fire.choice,
		"lease":        path.choice[separator+2:],
		"bomb":         bomb.choice,
		"valid_choice": true,
		"confidence": map[string]any{
			"intent": intent.confidence, "path": path.confidence, "movement": path.confidence,
			"fire": fire.confidence, "lease": path.confidence, "bomb": bomb.confidence,
		},
		"error": nil,
	}
}
