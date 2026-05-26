package pipeline

import (
	"github.com/monksvaughan/thin/internal/session"
)

// Pipeline applies the optimization passes in order. It is intentionally
// counter-free: token counting is expensive (full request marshal + tiktoken
// encode) and dominates wall time, so callers count off the request path
// after we've handed the rewritten request to the upstream.
type Pipeline struct {
	sessions         *session.Store
	protectedTools   map[string]bool
	enablePruneTools bool
}

// Options configures optional passes.
type Options struct {
	ProtectedTools   []string
	EnablePruneTools bool
}

// New builds a Pipeline with default (safe) options: prune_tools disabled.
func New(sessions *session.Store) *Pipeline {
	return NewWithOptions(sessions, Options{})
}

func NewWithProtectedTools(sessions *session.Store, protectedTools []string) *Pipeline {
	return NewWithOptions(sessions, Options{ProtectedTools: protectedTools})
}

func NewWithOptions(sessions *session.Store, opts Options) *Pipeline {
	protected := map[string]bool{}
	for _, name := range opts.ProtectedTools {
		if name != "" {
			protected[name] = true
		}
	}
	return &Pipeline{sessions: sessions, protectedTools: protected, enablePruneTools: opts.EnablePruneTools}
}

// Result is what Apply returns. Request is the (potentially mutated)
// request the caller should forward upstream — unless the caller is in
// dry-run mode, in which case the caller forwards the original bytes and
// uses Request only as the "what we would have sent" reference.
type Result struct {
	Request            *ChatRequest
	PassesApplied      []string
	PrunedTools        []PrunedTool
	DedupedToolResults []DedupedToolResult
	CompactedMessages  []CompactedMessage
	RepeatedToolCalls  []RepeatedToolCall
}

// Apply runs each pass in order, mutating req in place. Dry-run is the
// caller's concern; we always produce the optimized form so the caller can
// measure against it.
func (p *Pipeline) Apply(sessionID string, req *ChatRequest) Result {
	result := Result{Request: req, PassesApplied: []string{}}
	sess := p.sessions.Get(sessionID)

	// Bookkeeping: record tool-call names from history. Other passes
	// (prune_tools) depend on this running first.
	sess.ObserveToolCalls(ToolCallNames(req.Messages))
	result.RepeatedToolCalls = RepeatedToolCalls(req)

	// Drop tool definitions the session has never invoked. Gated by
	// minObservedTurnsForPrune so we never silently break a tool's first use.
	if p.enablePruneTools {
		if pruned := PruneToolsWithReport(req, sess, p.protectedTools); len(pruned) > 0 {
			result.PassesApplied = append(result.PassesApplied, "prune_tools")
			result.PrunedTools = pruned
		}
	}

	// When the same (fn, args) tool call appears multiple times in history,
	// replace older results with a one-line stub pointing at the latest.
	if deduped := DedupeToolResultsWithReport(req); len(deduped) > 0 {
		result.PassesApplied = append(result.PassesApplied, "dedupe_tool_results")
		result.DedupedToolResults = deduped
	}

	// Whitespace cleanup on old messages; head+tail truncate big tool
	// outputs older than the recency window.
	if compacted := CompactHistoryWithReport(req); len(compacted) > 0 {
		result.PassesApplied = append(result.PassesApplied, "compact_history")
		result.CompactedMessages = compacted
	}

	// Measurement-only in v1: record the stable prefix length per session.
	// v2 would insert Anthropic cache_control breakpoints here.
	if shaped := ShapeForCache(req, sess); shaped {
		result.PassesApplied = append(result.PassesApplied, "shape_cache")
	}

	return result
}

// ToolCallNames returns every tool-call name across msgs, in message order.
// Exposed for tests that drive session bookkeeping directly.
func ToolCallNames(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			out = append(out, tc.Function.Name)
		}
	}
	return out
}
