package main

import (
	"net/http"
	"strconv"
	"sync"
)

// exchangeCapacity bounds the in-memory history served to the event-log detail view.
const exchangeCapacity = 256

// Exchange is one upstream POST /v1/systemone call exactly as sent and received.
type Exchange struct {
	RunID        string  `json:"run_id"`
	DecisionID   string  `json:"decision_id"`
	Sequence     int64   `json:"sequence"`
	Endpoint     string  `json:"endpoint"`
	RecordedAt   string  `json:"recorded_at_utc"`
	RequestBody  string  `json:"request_body"`
	Status       any     `json:"status"`
	ResponseBody any     `json:"response_body"`
	Error        any     `json:"error"`
	LatencyMs    float64 `json:"latency_ms"`
	Pending      bool    `json:"pending"`
}

type exchangeLog struct {
	mu    sync.Mutex
	items []Exchange
}

func (l *exchangeLog) add(item Exchange) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
	if len(l.items) > exchangeCapacity {
		l.items = append([]Exchange(nil), l.items[len(l.items)-exchangeCapacity:]...)
	}
}

// complete fills in the response of the exchange recorded when its request was sent.
func (l *exchangeLog) complete(runID string, sequence int64, status, body, callErr any, latencyMs float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.items) - 1; i >= 0; i-- {
		if l.items[i].RunID == runID && l.items[i].Sequence == sequence {
			item := &l.items[i]
			item.Status, item.ResponseBody, item.Error, item.LatencyMs, item.Pending = status, body, callErr, latencyMs, false
			return
		}
	}
}

func (l *exchangeLog) find(runID string, sequence int64) (Exchange, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.items) - 1; i >= 0; i-- {
		if l.items[i].RunID == runID && l.items[i].Sequence == sequence {
			return l.items[i], true
		}
	}
	return Exchange{}, false
}

// serveExchange handles GET /api/exchange?run_id=...&sequence=N.
func (s *Server) serveExchange(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sequence, err := strconv.ParseInt(query.Get("sequence"), 10, 64)
	if err != nil || query.Get("run_id") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"schema_version": SchemaVersion, "error": "run_id and integer sequence are required"})
		return
	}
	item, ok := s.exchanges.find(query.Get("run_id"), sequence)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"schema_version": SchemaVersion, "error": "exchange not found; only the most recent calls are kept"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "exchange": item})
}
