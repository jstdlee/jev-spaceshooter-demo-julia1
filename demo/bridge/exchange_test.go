package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExchangeLogServesRecentCallsByRunAndSequence(t *testing.T) {
	s := &Server{}
	for i := int64(0); i < exchangeCapacity+5; i++ {
		s.exchanges.add(Exchange{RunID: "run-a", Sequence: i, RequestBody: `{"q":1}`, ResponseBody: `{"a":2}`})
	}
	if len(s.exchanges.items) != exchangeCapacity {
		t.Fatalf("kept %d exchanges, want %d", len(s.exchanges.items), exchangeCapacity)
	}
	rec := httptest.NewRecorder()
	s.serveExchange(rec, httptest.NewRequest(http.MethodGet, "/api/exchange?run_id=run-a&sequence=100", nil))
	var body struct{ Exchange Exchange }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Exchange.Sequence != 100 || body.Exchange.RequestBody != `{"q":1}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	for _, url := range []string{"/api/exchange?run_id=run-a&sequence=0", "/api/exchange?run_id=run-b&sequence=100"} {
		rec = httptest.NewRecorder()
		s.serveExchange(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d, want 404", url, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	s.serveExchange(rec, httptest.NewRequest(http.MethodGet, "/api/exchange?sequence=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad query: got %d", rec.Code)
	}
}

func TestExchangeLogCompletesPendingRequest(t *testing.T) {
	var l exchangeLog
	l.add(Exchange{RunID: "r", Sequence: 7, RequestBody: "{}", Pending: true})
	l.complete("r", 7, 200, `{"ok":true}`, nil, 12.5)
	got, ok := l.find("r", 7)
	if !ok || got.Pending || got.Status != 200 || got.ResponseBody != `{"ok":true}` || got.LatencyMs != 12.5 {
		t.Fatalf("got %+v", got)
	}
}
