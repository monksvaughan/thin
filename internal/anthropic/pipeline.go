package anthropic

import "github.com/you/token-proxy/internal/session"

type Pipeline struct {
	sessions         *session.Store
	protectedTools   map[string]bool
	enablePruneTools bool
}

type Options struct {
	ProtectedTools   []string
	EnablePruneTools bool
}

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

type Result struct {
	Request            *MessagesRequest
	PassesApplied      []string
	PrunedTools        []PrunedTool
	DedupedToolResults []DedupedToolResult
	CompactedMessages  []CompactedMessage
	RepeatedToolCalls  []RepeatedToolCall
}

func (p *Pipeline) Apply(sessionID string, req *MessagesRequest) Result {
	result := Result{Request: req, PassesApplied: []string{}}
	sess := p.sessions.Get(sessionID)

	sess.ObserveToolCalls(ToolCallNames(req.Messages))
	result.RepeatedToolCalls = RepeatedToolCalls(req)

	if p.enablePruneTools {
		if pruned := PruneToolsWithReport(req, sess, p.protectedTools); len(pruned) > 0 {
			result.PassesApplied = append(result.PassesApplied, "prune_tools")
			result.PrunedTools = pruned
		}
	}
	if deduped := DedupeToolResultsWithReport(req); len(deduped) > 0 {
		result.PassesApplied = append(result.PassesApplied, "dedupe_tool_results")
		result.DedupedToolResults = deduped
	}
	if compacted := CompactHistoryWithReport(req); len(compacted) > 0 {
		result.PassesApplied = append(result.PassesApplied, "compact_history")
		result.CompactedMessages = compacted
	}
	if shaped := ShapeForCache(req, sess); shaped {
		result.PassesApplied = append(result.PassesApplied, "shape_cache")
	}
	return result
}

func ToolCallNames(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		blocks, ok := contentBlocks(m.Content)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name != "" {
				out = append(out, b.Name)
			}
		}
	}
	return out
}
