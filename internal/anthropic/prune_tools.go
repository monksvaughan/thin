package anthropic

import (
	"encoding/json"

	"github.com/you/token-proxy/internal/session"
)

const minObservedTurnsForPrune = 3

type PrunedTool struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func PruneTools(req *MessagesRequest, sess *session.Session) int {
	return len(PruneToolsWithReport(req, sess, nil))
}

func PruneToolsWithReport(req *MessagesRequest, sess *session.Session, protectedTools map[string]bool) []PrunedTool {
	if len(req.Tools) == 0 || sess.Turns() < minObservedTurnsForPrune || sess.LastCacheHit() {
		return nil
	}

	usedInRequest := forcedToolChoice(req)
	for _, name := range ToolCallNames(req.Messages) {
		usedInRequest[name] = true
	}

	kept := req.Tools[:0]
	var pruned []PrunedTool
	for _, t := range req.Tools {
		if protectedTools[t.Name] || usedInRequest[t.Name] || sess.HasUsedTool(t.Name) {
			kept = append(kept, t)
			continue
		}
		pruned = append(pruned, PrunedTool{Name: t.Name, Reason: "unused_after_min_observed_turns_and_no_cache_hit"})
	}
	req.Tools = kept
	return pruned
}

func forcedToolChoice(req *MessagesRequest) map[string]bool {
	forced := map[string]bool{}
	if req.Extra == nil {
		return forced
	}
	raw, ok := req.Extra["tool_choice"]
	if !ok {
		return forced
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &choice); err == nil && choice.Type == "tool" && choice.Name != "" {
		forced[choice.Name] = true
	}
	return forced
}
