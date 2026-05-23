package pipeline

import (
	"encoding/json"

	"github.com/you/token-proxy/internal/session"
	"github.com/you/token-proxy/internal/tokens"
)

// Pipeline applies the optimization passes in order.
type Pipeline struct {
	sessions *session.Store
	dryRun   bool
	counter  *tokens.Counter
}

// New builds a Pipeline.
func New(sessions *session.Store, dryRun bool) *Pipeline {
	return &Pipeline{
		sessions: sessions,
		dryRun:   dryRun,
		counter:  tokens.New(),
	}
}

// Result is what Apply returns. We always return a request — if dryRun is
// set or no pass mutates, it's the original. TokensInOriginal and
// TokensInAfter let us see how much we actually saved.
type Result struct {
	Request          *ChatRequest
	TokensInOriginal int
	TokensInAfter    int
	PassesApplied    []string
}

// Apply runs each pass in order. In dry-run mode we count what we would
// have done but emit the original request to the upstream.
func (p *Pipeline) Apply(sessionID string, req *ChatRequest) Result {
	original := req.Clone()
	before := p.counter.CountRequest(req)

	result := Result{
		Request:          req,
		TokensInOriginal: before,
		PassesApplied:    []string{},
	}

	sess := p.sessions.Get(sessionID)

	// Pass 1: track tool usage from message history. This isn't a mutation;
	// it's bookkeeping that other passes rely on.
	sess.ObserveToolCalls(req.Messages)

	// Pass 2: tool schema pruning.
	// Drop tool definitions that haven't been called in the session and
	// don't appear in recent assistant turns. Conservative — only drops
	// tools we have at least N turns of evidence are unused.
	if pruned := PruneTools(req, sess); pruned > 0 {
		result.PassesApplied = append(result.PassesApplied, "prune_tools")
	}

	// Pass 3: tool-result deduplication.
	// If the same tool call (function + arguments) appears multiple times
	// in history, keep the latest result and replace earlier ones with a
	// stub. Catches the "agent re-read auth.js four times" pattern.
	if deduped := DedupeToolResults(req); deduped > 0 {
		result.PassesApplied = append(result.PassesApplied, "dedupe_tool_results")
	}

	// Pass 4: heuristic history compaction.
	// For messages older than the recency window, strip trailing whitespace,
	// collapse repeated blank lines, and truncate very long tool outputs to
	// head+tail. No model calls.
	if compacted := CompactHistory(req); compacted > 0 {
		result.PassesApplied = append(result.PassesApplied, "compact_history")
	}

	// Pass 5: cache-shape annotation.
	// For Anthropic-compatible upstreams, insert cache_control breakpoints
	// after stable prefix content (system, tool defs). For OpenAI, this is
	// a no-op but we still record what the breakpoint set would be so we
	// can estimate prefix-cache hit rates from history.
	if shaped := ShapeForCache(req, sess); shaped {
		result.PassesApplied = append(result.PassesApplied, "shape_cache")
	}

	after := p.counter.CountRequest(req)
	result.TokensInAfter = after

	if p.dryRun {
		// Emit the original to the upstream but keep measurements.
		result.Request = original
	}

	return result
}

// Clone produces a deep-enough copy of the request to safely revert in
// dry-run mode. We rely on the fact that json.RawMessage and message
// slices are the only mutable parts we touch.
func (r *ChatRequest) Clone() *ChatRequest {
	out := *r
	out.Messages = make([]Message, len(r.Messages))
	copy(out.Messages, r.Messages)
	out.Tools = make([]Tool, len(r.Tools))
	copy(out.Tools, r.Tools)
	if r.Extra != nil {
		out.Extra = make(map[string]json.RawMessage, len(r.Extra))
		for k, v := range r.Extra {
			cp := make(json.RawMessage, len(v))
			copy(cp, v)
			out.Extra[k] = cp
		}
	}
	return &out
}
