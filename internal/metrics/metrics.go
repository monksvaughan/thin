// Package metrics persists per-request measurements so we can analyze
// savings over a real workload. Pure SQLite for the prototype — single
// file, no daemons, easy to ship to a duckdb / pandas notebook later.
package metrics

import (
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Record is one row in the requests table.
type Record struct {
	SessionID       string
	Timestamp       time.Time
	Model           string
	TokensIn        int
	TokensInAfter   int
	TokensOutEst    int
	PipelineLatency time.Duration
	UpstreamLatency time.Duration
	PassesApplied   []string
	BytesIn         int
	BytesOut        int
	StatusCode      int
}

// Store wraps a *sql.DB.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at path and ensures the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite single-writer assumption: keep a small pool, enable WAL.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Insert writes one record.
func (s *Store) Insert(r Record) error {
	_, err := s.db.Exec(`
		INSERT INTO requests (
			session_id, ts, model,
			tokens_in, tokens_in_after, tokens_out_est,
			pipeline_latency_us, upstream_latency_us,
			passes, bytes_in, bytes_out, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		r.SessionID, r.Timestamp.UnixNano(), r.Model,
		r.TokensIn, r.TokensInAfter, r.TokensOutEst,
		r.PipelineLatency.Microseconds(), r.UpstreamLatency.Microseconds(),
		strings.Join(r.PassesApplied, ","),
		r.BytesIn, r.BytesOut, r.StatusCode,
	)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
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
);
CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
`
