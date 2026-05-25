package session

import (
	"net/http/httptest"
	"testing"
)

func TestIDFor_ExplicitHeaderWins(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Session-Id", "explicit-id")
	got := IDFor(r, map[string]any{"model": "m", "messages": []any{}})
	if got != "explicit-id" {
		t.Fatalf("expected explicit-id, got %q", got)
	}
}

// Session affinity is the entire point of the hash — same conversation must
// resolve to the same ID across turns, or prune_tools never builds enough
// history to fire and dedupe_tool_results can't find duplicates.
func TestIDForRequest_MatchesImplicitIDFor(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	req := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]any{
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": "hello"},
		},
	}
	if got, want := IDForRequest(req), IDFor(r, req); got != want {
		t.Fatalf("IDForRequest must match implicit live ID: got %q want %q", got, want)
	}
}

func TestIDFor_SameConversationSameHash(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	req := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]any{
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": "hello"},
		},
	}
	a := IDFor(r, req)
	b := IDFor(r, req)
	if a != b {
		t.Fatalf("expected identical IDs, got %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("expected 16-char ID, got %d", len(a))
	}
}

// Two different conversations must NOT collide. If they do, the tool-usage
// history of one bleeds into the other and prune_tools can drop a tool the
// second session actually needs.
func TestIDFor_DifferentFirstUserDifferentHash(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	mk := func(user string) map[string]any {
		return map[string]any{
			"model": "gpt-4",
			"messages": []map[string]any{
				{"role": "system", "content": "shared system"},
				{"role": "user", "content": user},
			},
		}
	}
	if IDFor(r, mk("a")) == IDFor(r, mk("b")) {
		t.Fatal("different first user messages must produce different IDs")
	}
}

// Even with no header and a malformed payload, IDFor must return something
// usable rather than panicking — the proxy's whole error story is "never
// break the client's request."
func TestIDFor_FallsBackOnUnmarshalable(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	got := IDFor(r, make(chan int)) // channels don't marshal to JSON
	if got == "" {
		t.Fatal("IDFor returned empty string on unmarshalable input")
	}
}

func TestObserveToolCalls_TracksUsageAndTurns(t *testing.T) {
	sess := NewStore().Get("s")
	if sess.Turns() != 0 {
		t.Fatalf("fresh session should have 0 turns, got %d", sess.Turns())
	}
	sess.ObserveToolCalls([]string{"read_file", "read_file", "grep"})
	sess.ObserveToolCalls([]string{"read_file"})
	if sess.Turns() != 2 {
		t.Fatalf("expected 2 turns, got %d", sess.Turns())
	}
	if !sess.HasUsedTool("read_file") || !sess.HasUsedTool("grep") {
		t.Fatal("observed tools should be marked used")
	}
	if sess.HasUsedTool("delete_repo") {
		t.Fatal("unobserved tool should not be marked used")
	}
}

func TestStore_GetReturnsSameSessionPerId(t *testing.T) {
	s := NewStore()
	a := s.Get("x")
	b := s.Get("x")
	if a != b {
		t.Fatal("expected same pointer for same id")
	}
	if s.Get("y") == a {
		t.Fatal("expected different sessions for different ids")
	}
}

// Cache-hit state defaults to false (fail-open: prune normally when we
// have no signal). After RecordCacheHit(true), LastCacheHit reports it;
// recording false flips it back. Adaptive prune_tools relies on this.
func TestCacheHit_DefaultsFalseAndRoundTrips(t *testing.T) {
	sess := NewStore().Get("s")
	if sess.LastCacheHit() {
		t.Fatal("fresh session should default to no cache hit")
	}
	sess.RecordCacheHit(true)
	if !sess.LastCacheHit() {
		t.Fatal("expected true after RecordCacheHit(true)")
	}
	sess.RecordCacheHit(false)
	if sess.LastCacheHit() {
		t.Fatal("expected false after RecordCacheHit(false)")
	}
}

// The returned slice must be a copy — callers can't be allowed to mutate
// per-session state through the read API.
func TestRecordMessageFingerprints_ReturnsCopy(t *testing.T) {
	sess := NewStore().Get("s")
	if got := sess.LastMessageFingerprints(); got != nil {
		t.Fatalf("expected nil initially, got %v", got)
	}
	sess.RecordMessageFingerprints([]uint64{1, 2, 3})
	got := sess.LastMessageFingerprints()
	got[0] = 999
	again := sess.LastMessageFingerprints()
	if again[0] != 1 {
		t.Fatal("LastMessageFingerprints must return a copy, not internal state")
	}
}
