package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The ranking board: one entry per finished run, kept in <runs-dir>/leaderboard.json and ranked by survival time,
// then score. A run can be entered once, and only after the bridge has seen it complete.

const (
	leaderboardKeep  = 100
	leaderboardShown = 10
	maxNameRunes     = 16
)

var leaderboardName = regexp.MustCompile(`^[\p{L}\p{N} ._\-]+$`)

type LeaderboardEntry struct {
	Name   string  `json:"name"`
	TimeS  float64 `json:"time_s"`
	Score  int64   `json:"score"`
	Wave   int64   `json:"wave"`
	Mode   string  `json:"mode"`
	RunID  string  `json:"run_id"`
	Reason string  `json:"reason"`
	At     string  `json:"at"`
}

type leaderboard struct {
	mu   sync.Mutex
	path string
}

func (l *leaderboard) load() ([]LeaderboardEntry, error) {
	raw, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []LeaderboardEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (l *leaderboard) save(entries []LeaderboardEntry) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entries, "", " ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func rankEntries(entries []LeaderboardEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].TimeS != entries[j].TimeS {
			return entries[i].TimeS > entries[j].TimeS
		}
		return entries[i].Score > entries[j].Score
	})
}

func top(entries []LeaderboardEntry) []LeaderboardEntry {
	if len(entries) > leaderboardShown {
		return entries[:leaderboardShown]
	}
	return entries
}

// Leaderboard serves GET /api/leaderboard.
func (s *Server) Leaderboard() (map[string]any, error) {
	s.board.mu.Lock()
	defer s.board.mu.Unlock()
	entries, err := s.board.load()
	if err != nil {
		return nil, err
	}
	rankEntries(entries)
	return map[string]any{"schema_version": SchemaVersion, "entries": top(entries)}, nil
}

// SubmitLeaderboard serves POST /api/leaderboard: {run_id, name, time_s, score, wave, mode, reason}.
func (s *Server) SubmitLeaderboard(body any) (map[string]any, error) {
	object, err := requireObject(body, "body")
	if err != nil {
		return nil, err
	}
	runID, _ := object["run_id"].(string)
	run, err := s.getRun(runID)
	if err != nil {
		return nil, err
	}
	run.eventMu.Lock()
	complete := run.complete
	run.eventMu.Unlock()
	if !complete {
		return nil, conflictError("run_not_complete", "only a finished run can enter the ranking board")
	}
	name, _ := object["name"].(string)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len([]rune(name)) > maxNameRunes || !leaderboardName.MatchString(name) {
		return nil, validationError("name must be 1-%d letters, digits, spaces, dots, dashes or underscores", maxNameRunes)
	}
	_, timeS, err := observedNumber(object, "time_s", "body", false, min0())
	if err != nil {
		return nil, err
	}
	if *timeS > 36000 {
		return nil, validationError("time_s is out of range")
	}
	score, err := nonnegativeInt(object["score"], "body.score")
	if err != nil {
		return nil, err
	}
	wave, err := nonnegativeInt(object["wave"], "body.wave")
	if err != nil {
		return nil, err
	}
	mode, _ := object["mode"].(string)
	if mode != "autopilot" && mode != "manual" {
		return nil, validationError("mode must be autopilot or manual")
	}
	reason, _ := object["reason"].(string)
	if len(reason) > 32 {
		reason = reason[:32]
	}

	s.board.mu.Lock()
	defer s.board.mu.Unlock()
	entries, err := s.board.load()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.RunID == runID {
			return nil, conflictError("already_ranked", "this run is already on the ranking board")
		}
	}
	entry := LeaderboardEntry{Name: name, TimeS: round(*timeS, 1), Score: score, Wave: wave, Mode: mode, RunID: runID, Reason: reason,
		At: time.Now().UTC().Format(time.RFC3339)}
	entries = append(entries, entry)
	rankEntries(entries)
	rank := 0
	for i, e := range entries {
		if e.RunID == runID {
			rank = i + 1
			break
		}
	}
	if len(entries) > leaderboardKeep {
		entries = entries[:leaderboardKeep]
	}
	if err := s.board.save(entries); err != nil {
		return nil, err
	}
	return map[string]any{"schema_version": SchemaVersion, "rank": rank, "entries": top(entries)}, nil
}
