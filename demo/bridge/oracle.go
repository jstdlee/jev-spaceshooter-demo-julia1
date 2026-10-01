package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// oracleUpstream answers like a perfect reader of the path labels, with no model call.
// It exists only for offline simulation: it measures whether the labels carry enough
// information to survive, separately from how well Djev follows them.
type oracleUpstream struct{}

var (
	tierPattern    = regexp.MustCompile(`^Tier (\d) `)
	escapesPattern = regexp.MustCompile(`(\d)/9 escapes`)
	futuresPattern = regexp.MustCompile(`survives (\d+)/(\d+) futures`)
	hitInPattern   = regexp.MustCompile(`hits the ship in (\d+) ms`)
)

func oracleScore(label string) (tier int, score float64) {
	match := tierPattern.FindStringSubmatch(label)
	if match == nil {
		return 9, 0
	}
	tier, _ = strconv.Atoi(match[1])
	if hit := hitInPattern.FindStringSubmatch(label); hit != nil {
		ms, _ := strconv.Atoi(hit[1])
		return tier, float64(ms)
	}
	// Mirrors the path question's within-tier order.
	if escapes := escapesPattern.FindStringSubmatch(label); escapes != nil {
		n, _ := strconv.Atoi(escapes[1])
		score += float64(n) * 1000
	}
	if futures := futuresPattern.FindStringSubmatch(label); futures != nil {
		n, _ := strconv.Atoi(futures[1])
		score += float64(n) * 1000
	}
	if strings.Contains(label, "back to zone") {
		score += 2
	}
	if strings.Contains(label, "keeps course") {
		score += 100
	}
	if !strings.Contains(label, "near enemy") {
		score += 10
	}
	if strings.Contains(label, "open space") {
		score += 2
	}
	if strings.Contains(label, "collects ") {
		score += 50
	} else if strings.Contains(label, "toward bomb") || strings.Contains(label, "toward weapon") || strings.Contains(label, "toward missile") {
		score += 5
	}
	if strings.Contains(label, "toward center") {
		score++
	}
	return tier, score
}

func oracleChoice(criteria map[string]any, order []string) string {
	best, bestTier, bestScore := "", 99, -1.0
	for _, key := range order {
		label, _ := criteria[key].(string)
		tier, score := oracleScore(label)
		if tier < bestTier || tier == bestTier && score > bestScore {
			best, bestTier, bestScore = key, tier, score
		}
	}
	return best
}

func (oracleUpstream) Call(_ context.Context, body []byte) (*UpstreamResult, error) {
	var payload struct {
		State     map[string]any `json:"state"`
		Questions map[string]struct {
			Criteria map[string]any `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	answers := map[string]any{}
	path := oracleChoice(payload.Questions["path"].Criteria, PathIDs)
	answers["path"] = map[string]any{"choice": path, "confidence": 1}
	intent := "recover"
	if payload.State["hold_collision"] == true {
		intent = "evade"
	} else if payload.State["inside_center_region"] == true {
		intent = "position"
	}
	answers["intent"] = map[string]any{"choice": intent, "confidence": 1}
	fire := "cease"
	if count, ok := payload.State["enemy_count"].(float64); ok && count > 0 {
		fire = "shoot"
	}
	answers["fire"] = map[string]any{"choice": fire, "confidence": 1}
	if bomb, ok := payload.Questions["bomb"]; ok {
		answers["bomb"] = map[string]any{"choice": oracleBomb(bomb.Criteria), "confidence": 1}
	}
	raw, _ := json.Marshal(map[string]any{"model": "oracle", "answers": answers})
	return okUpstream(raw), nil
}

// oracleBomb follows the bomb question's rule: pick the rank 1 choice.
func oracleBomb(criteria map[string]any) string {
	if label, _ := criteria["detonate"].(string); strings.HasPrefix(label, "Rank 1") {
		return "detonate"
	}
	return "hold"
}

func okUpstream(raw []byte) *UpstreamResult {
	text := string(raw)
	status := 200
	parsed, _ := decodeJSON(raw)
	return &UpstreamResult{Parsed: parsed, RawBody: &text, Status: &status, ElapsedS: 0, Usage: usageFromPayload(parsed)}
}
