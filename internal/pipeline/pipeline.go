package pipeline

import (
	"github.com/you/token-proxy/internal/session"
)

// Pipeline applies the optimization passes in order. It is intentionally
// counter-free: token counting is expensive (full request marshal + tiktoken
// encode) and dominates wall time, so callers count off the request path
// after we've handed the rewritten request to the upstream.
type Pipeline struct {
	sessions *session.Store
}

// New builds a Pipeline.
func New(sessions *session.Store) *Pipeline {
	return &Pipeline{sessions: sessions}
}

// Result is what Apply returns. Request is the (potentially mutated)
// request the caller should forward upstream — unless the caller is in
// dry-run mode, in which case the caller forwards the original bytes and
// uses Request only as the "what we would have sent" reference.
type Result struct {
	Request       *ChatRequest
	PassesApplied []string
}

// Apply runs each pass in order, mutating req in place. Dry-run is the
// caller's concern; we always produce the optimized form so the caller can
// measure against it.
func (p *Pipeline) Apply(sessionID string, req *ChatRequest) Result {
	result := Result{Request: req, PassesApplied: []string{}}
	sess := p.sessions.Get(sessionID)

	// Bookkeeping: record tool-call names from history. Other passes
	// (prune_tools) depend on this running first.
	sess.ObserveToolCalls(req.Messages)

	// Drop tool definitions the session has never invoked. Gated by
	// minObservedTurnsForPrune so we never silently break a tool's first use.
	if pruned := PruneTools(req, sess); pruned > 0 {
		result.PassesApplied = append(result.PassesApplied, "prune_tools")
	}

	// When the same (fn, args) tool call appears multiple times in history,
	// replace older results with a one-line stub pointing at the latest.
	if deduped := DedupeToolResults(req); deduped > 0 {
		result.PassesApplied = append(result.PassesApplied, "dedupe_tool_results")
	}

	// Whitespace cleanup on old messages; head+tail truncate big tool
	// outputs older than the recency window.
	if compacted := CompactHistory(req); compacted > 0 {
		result.PassesApplied = append(result.PassesApplied, "compact_history")
	}

	// Measurement-only in v1: record the stable prefix length per session.
	// v2 would insert Anthropic cache_control breakpoints here.
	if shaped := ShapeForCache(req, sess); shaped {
		result.PassesApplied = append(result.PassesApplied, "shape_cache")
	}

	return result
}
