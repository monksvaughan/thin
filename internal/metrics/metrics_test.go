package metrics

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Round-trip every column of a Record through Insert and a fresh SELECT.
// If the schema and the parameter order in Insert ever drift apart, the
// metrics rows are silently wrong and every README query lies.
func TestStore_OpenInsertRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ts := time.Unix(1700000000, 0)
	want := Record{
		SessionID:       "sess-a",
		Timestamp:       ts,
		Model:           "gpt-4",
		TokensIn:        100,
		TokensInAfter:   80,
		TokensOutEst:    20,
		PipelineLatency: 250 * time.Microsecond,
		UpstreamLatency: 3 * time.Millisecond,
		PassesApplied:   []string{"prune_tools", "compact_history"},
		BytesIn:         5000,
		BytesOut:        4000,
		StatusCode:      200,
		CacheHit:        true,
	}
	if err := s.Insert(want); err != nil {
		t.Fatalf("insert: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var (
		sid, model, passes                                       string
		tsNs, pipelineUs, upstreamUs                             int64
		tIn, tInAfter, tOut, bIn, bOut, status, cacheHit         int
	)
	row := db.QueryRow(`SELECT session_id, ts, model,
		tokens_in, tokens_in_after, tokens_out_est,
		pipeline_latency_us, upstream_latency_us, passes,
		bytes_in, bytes_out, status, cache_hit FROM requests`)
	if err := row.Scan(&sid, &tsNs, &model, &tIn, &tInAfter, &tOut,
		&pipelineUs, &upstreamUs, &passes, &bIn, &bOut, &status, &cacheHit); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if sid != want.SessionID {
		t.Errorf("session_id: got %q want %q", sid, want.SessionID)
	}
	if tsNs != ts.UnixNano() {
		t.Errorf("ts: got %d want %d", tsNs, ts.UnixNano())
	}
	if model != want.Model {
		t.Errorf("model: got %q want %q", model, want.Model)
	}
	if tIn != want.TokensIn {
		t.Errorf("tokens_in: got %d want %d", tIn, want.TokensIn)
	}
	if tInAfter != want.TokensInAfter {
		t.Errorf("tokens_in_after: got %d want %d", tInAfter, want.TokensInAfter)
	}
	if tOut != want.TokensOutEst {
		t.Errorf("tokens_out_est: got %d want %d", tOut, want.TokensOutEst)
	}
	if pipelineUs != 250 {
		t.Errorf("pipeline_latency_us: got %d want 250", pipelineUs)
	}
	if upstreamUs != 3000 {
		t.Errorf("upstream_latency_us: got %d want 3000", upstreamUs)
	}
	if passes != "prune_tools,compact_history" {
		t.Errorf("passes: got %q want %q", passes, "prune_tools,compact_history")
	}
	if bIn != want.BytesIn || bOut != want.BytesOut || status != want.StatusCode {
		t.Errorf("bytes_in/bytes_out/status: got %d/%d/%d want %d/%d/%d",
			bIn, bOut, status, want.BytesIn, want.BytesOut, want.StatusCode)
	}
	if cacheHit != 1 {
		t.Errorf("cache_hit: got %d want 1", cacheHit)
	}
}

// An existing metrics.db created before cache_hit was added must migrate
// cleanly: Open should add the column, existing rows default to 0, and
// new inserts populate it.
func TestStore_MigratesLegacyDBWithoutCacheHit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// Stand up the old schema directly — what an older proxy would have left.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("legacy open: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		ts INTEGER NOT NULL,
		model TEXT NOT NULL,
		tokens_in INTEGER NOT NULL,
		tokens_in_after INTEGER NOT NULL,
		tokens_out_est INTEGER NOT NULL,
		pipeline_latency_us INTEGER NOT NULL,
		upstream_latency_us INTEGER NOT NULL,
		passes TEXT NOT NULL,
		bytes_in INTEGER NOT NULL,
		bytes_out INTEGER NOT NULL,
		status INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO requests (
		session_id, ts, model,
		tokens_in, tokens_in_after, tokens_out_est,
		pipeline_latency_us, upstream_latency_us, passes,
		bytes_in, bytes_out, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"old-sess", time.Now().UnixNano(), "old-model",
		10, 8, 2, 100, 200, "", 500, 400, 200)
	if err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	legacy.Close()

	// Now Open through the metrics package — migration should add cache_hit.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("post-migration open: %v", err)
	}
	defer s.Close()

	// Legacy row should still be there, cache_hit defaulted to 0.
	var legacyCacheHit int
	if err := s.db.QueryRow(
		`SELECT cache_hit FROM requests WHERE session_id = 'old-sess'`,
	).Scan(&legacyCacheHit); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if legacyCacheHit != 0 {
		t.Errorf("legacy row cache_hit should default to 0, got %d", legacyCacheHit)
	}

	// New insert with CacheHit=true should land correctly.
	if err := s.Insert(Record{
		SessionID: "new-sess", Timestamp: time.Now(), Model: "m", CacheHit: true,
	}); err != nil {
		t.Fatalf("post-migration insert: %v", err)
	}
	var newCacheHit int
	if err := s.db.QueryRow(
		`SELECT cache_hit FROM requests WHERE session_id = 'new-sess'`,
	).Scan(&newCacheHit); err != nil {
		t.Fatalf("read new row: %v", err)
	}
	if newCacheHit != 1 {
		t.Errorf("new row cache_hit should be 1, got %d", newCacheHit)
	}
}

// The proxy may be restarted against an existing metrics.db. Re-opening
// must not error and must not clobber existing data.
func TestStore_OpenIsIdempotentAndPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := s1.Insert(Record{SessionID: "x", Timestamp: time.Now(), Model: "m"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()

	var n int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row after reopen, got %d", n)
	}
}

// A request that triggered no passes still produces a row; the empty
// passes string must be valid (it's NOT NULL in the schema).
func TestStore_InsertWithNoPasses(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.Insert(Record{SessionID: "s", Timestamp: time.Now(), Model: "m"}); err != nil {
		t.Fatalf("insert with empty passes: %v", err)
	}
}
