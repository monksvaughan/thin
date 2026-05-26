package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monksvaughan/thin/internal/session"
)

func rawString(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

func TestTypes_roundTripUnknownFields(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-4",
		"max_tokens":1024,
		"thinking":{"type":"enabled","budget_tokens":1024},
		"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}],"custom_msg":true}],
		"tools":[{"name":"Read","description":"read files","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
		"tool_choice":{"type":"tool","name":"Read"}
	}`)
	var req MessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"max_tokens", "thinking", "custom_msg", "cache_control", "tool_choice"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("round-trip output missing %q: %s", want, out)
		}
	}
}

func TestToolCallNames_extractsAssistantToolUse(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: rawJSON(`[{"type":"tool_result","tool_use_id":"u1","content":"x"}]`)},
		{Role: "assistant", Content: rawJSON(`[{"type":"text","text":"ok"},{"type":"tool_use","id":"u1","name":"Read","input":{"file":"a"}}]`)},
	}
	got := ToolCallNames(msgs)
	if len(got) != 1 || got[0] != "Read" {
		t.Fatalf("ToolCallNames = %#v", got)
	}
}

func TestPruneTools_dropsUnusedAfterEnoughTurns(t *testing.T) {
	sess := session.NewStore().Get("s")
	for i := 0; i < 3; i++ {
		sess.ObserveToolCalls([]string{"Read"})
	}
	req := &MessagesRequest{Tools: []Tool{{Name: "Read"}, {Name: "DeleteRepo"}}}
	pruned := PruneToolsWithReport(req, sess, nil)
	if len(pruned) != 1 || pruned[0].Name != "DeleteRepo" {
		t.Fatalf("pruned = %#v", pruned)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "Read" {
		t.Fatalf("tools = %#v", req.Tools)
	}
}

func TestPruneTools_keepsForcedToolChoice(t *testing.T) {
	sess := session.NewStore().Get("s")
	for i := 0; i < 3; i++ {
		sess.ObserveToolCalls([]string{"Read"})
	}
	req := &MessagesRequest{
		Tools: []Tool{{Name: "Read"}, {Name: "Write"}},
		Extra: map[string]json.RawMessage{"tool_choice": rawJSON(`{"type":"tool","name":"Write"}`)},
	}
	if pruned := PruneTools(req, sess); pruned != 0 {
		t.Fatalf("expected no pruned tools, got %d", pruned)
	}
}

func TestCompactHistory_truncatesOldToolResultAndPreservesRecent(t *testing.T) {
	long := strings.Repeat("a", toolOutputTruncateThreshold+100)
	req := &MessagesRequest{}
	req.Messages = append(req.Messages, Message{Role: "user", Content: rawJSON(`[{"type":"tool_result","tool_use_id":"u1","content":` + string(rawString(t, long)) + `,"cache_control":{"type":"ephemeral"}}]`)})
	for i := 0; i < recencyWindow; i++ {
		req.Messages = append(req.Messages, Message{Role: "user", Content: rawString(t, "recent   \n\n\n")})
	}
	if touched := CompactHistory(req); touched != 1 {
		t.Fatalf("touched = %d", touched)
	}
	out := string(req.Messages[0].Content)
	if !strings.Contains(out, "elided by proxy") || !strings.Contains(out, "cache_control") {
		t.Fatalf("old tool result not truncated/preserved: %s", out)
	}
	var recent string
	if err := json.Unmarshal(req.Messages[len(req.Messages)-1].Content, &recent); err != nil {
		t.Fatal(err)
	}
	if recent != "recent   \n\n\n" {
		t.Fatalf("recent message changed: %q", recent)
	}
}

func TestDedupeToolResults_stubsOlderIdenticalResult(t *testing.T) {
	req := &MessagesRequest{Messages: []Message{
		{Role: "assistant", Content: rawJSON(`[{"type":"tool_use","id":"u1","name":"Read","input":{"file":"a"}}]`)},
		{Role: "user", Content: rawJSON(`[{"type":"tool_result","tool_use_id":"u1","content":"same","is_error":false}]`)},
		{Role: "assistant", Content: rawJSON(`[{"type":"tool_use","id":"u2","name":"Read","input":{"file":"a"}}]`)},
		{Role: "user", Content: rawJSON(`[{"type":"tool_result","tool_use_id":"u2","content":"same","is_error":false}]`)},
	}}
	if stubbed := DedupeToolResults(req); stubbed != 1 {
		t.Fatalf("stubbed = %d", stubbed)
	}
	if !strings.Contains(string(req.Messages[1].Content), "deduplicated by proxy") {
		t.Fatalf("older result not stubbed: %s", req.Messages[1].Content)
	}
	if strings.Contains(string(req.Messages[3].Content), "deduplicated by proxy") {
		t.Fatalf("latest result was stubbed: %s", req.Messages[3].Content)
	}
}

func TestShapeForCache_usesSystemAndMessages(t *testing.T) {
	sess := session.NewStore().Get("s")
	req := &MessagesRequest{System: rawString(t, "sys"), Messages: []Message{{Role: "user", Content: rawString(t, "hi")}}}
	if ShapeForCache(req, sess) {
		t.Fatal("first request should not shape")
	}
	if !ShapeForCache(req, sess) {
		t.Fatal("second identical request should detect stable prefix")
	}
}
