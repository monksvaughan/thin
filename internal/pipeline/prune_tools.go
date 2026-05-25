package pipeline

import (
	"encoding/json"

	"github.com/you/token-proxy/internal/session"
)

// PruneTools removes function tool definitions for tools the session has
// never actually called and that don't appear in recent assistant turns.
// Returns the number of tools pruned.
//
// Why this works: agentic coding clients (Claude Code, Cursor, OpenCode)
// often register 20-40 MCP tools per request. JSON schemas for unused tools
// add 10-15KB per turn. If the session has gone N turns without invoking
// `delete_repository` or `transfer_issue`, it almost certainly won't this
// turn either.
//
// Safety: we keep any tool whose name appears in *any* assistant
// tool_call in the current request's message history. We only drop tools
// that have ZERO evidence of use across the whole session.
//
// Conservatism knob: minObservedTurns. Don't prune on turn 1 because we
// haven't seen anything yet — that would silently break a request whose
// very first action is to call a tool we dropped.
const minObservedTurnsForPrune = 3

func PruneTools(req *ChatRequest, sess *session.Session) int {
	if len(req.Tools) == 0 {
		return 0
	}
	if sess.Turns() < minObservedTurnsForPrune {
		return 0
	}
	// Adaptive: if the previous response showed a prompt-cache hit, dropping
	// tools now would change the cached prefix and bust the cache, re-paying
	// full price for the new shape. The cache is already doing the work we'd
	// otherwise do here; don't undermine it.
	if sess.LastCacheHit() {
		return 0
	}

	// Collect tools called in this exact request (belt + braces; the
	// session bookkeeping already covers history but be defensive). Also keep
	// any tool explicitly named by tool_choice; pruning it while preserving the
	// forced tool_choice would make the upstream reject the request.
	usedInRequest := forcedToolChoice(req)
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			usedInRequest[tc.Function.Name] = true
		}
	}

	kept := req.Tools[:0]
	pruned := 0
	for _, t := range req.Tools {
		name := t.Function.Name
		if usedInRequest[name] || sess.HasUsedTool(name) {
			kept = append(kept, t)
			continue
		}
		pruned++
	}
	req.Tools = kept
	return pruned
}

func forcedToolChoice(req *ChatRequest) map[string]bool {
	forced := map[string]bool{}
	if req.Extra == nil {
		return forced
	}
	raw, ok := req.Extra["tool_choice"]
	if !ok {
		return forced
	}

	// String forms ("auto", "none", "required") do not name a specific tool.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return forced
	}

	var choice struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &choice); err == nil && choice.Function.Name != "" {
		forced[choice.Function.Name] = true
	}
	return forced
}
