package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/you/token-proxy/internal/session"
)

func mustContent(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPruneTools_keepsAllOnEarlyTurns(t *testing.T) {
	sess := session.NewStore().Get("s1")
	// Turn 1: should not prune anything (no observation history).
	req := &ChatRequest{
		Tools: []Tool{
			{Type: "function", Function: ToolFunction{Name: "read_file"}},
			{Type: "function", Function: ToolFunction{Name: "delete_repo"}},
		},
	}
	sess.ObserveToolCalls(ToolCallNames(req.Messages))
	if pruned := PruneTools(req, sess); pruned != 0 {
		t.Fatalf("expected 0 pruned on turn 1, got %d", pruned)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("expected both tools kept, got %d", len(req.Tools))
	}
}

// Adaptive prune: when the upstream is hitting its prompt cache, dropping
// tools would change the cached prefix and re-pay full price. The cache is
// already doing the work pruning would do — leave the tools alone.
func TestPruneTools_skipsWhenCacheHit(t *testing.T) {
	sess := session.NewStore().Get("cache-hit")

	usedCall := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read_file", Arguments: `{"path":"x"}`},
		}},
	}
	// Get past the observation gate (3 turns) with read_file as the only
	// used tool — delete_repo would normally be pruned at turn 4.
	for i := 0; i < minObservedTurnsForPrune+1; i++ {
		req := &ChatRequest{
			Messages: []Message{usedCall},
			Tools: []Tool{
				{Type: "function", Function: ToolFunction{Name: "read_file"}},
				{Type: "function", Function: ToolFunction{Name: "delete_repo"}},
			},
		}
		sess.ObserveToolCalls(ToolCallNames(req.Messages))
		_ = PruneTools(req, sess)
	}
	// Last turn's response was a cache hit — prune_tools should now skip.
	sess.RecordCacheHit(true)
	req := &ChatRequest{
		Messages: []Message{usedCall},
		Tools: []Tool{
			{Type: "function", Function: ToolFunction{Name: "read_file"}},
			{Type: "function", Function: ToolFunction{Name: "delete_repo"}},
		},
	}
	sess.ObserveToolCalls(ToolCallNames(req.Messages))
	if pruned := PruneTools(req, sess); pruned != 0 {
		t.Fatalf("expected 0 pruned with cache hit, got %d", pruned)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("both tools should be kept when cache hit, got %d", len(req.Tools))
	}

	// Cache miss next turn — pruning resumes.
	sess.RecordCacheHit(false)
	req = &ChatRequest{
		Messages: []Message{usedCall},
		Tools: []Tool{
			{Type: "function", Function: ToolFunction{Name: "read_file"}},
			{Type: "function", Function: ToolFunction{Name: "delete_repo"}},
		},
	}
	sess.ObserveToolCalls(ToolCallNames(req.Messages))
	if pruned := PruneTools(req, sess); pruned != 1 {
		t.Fatalf("expected 1 pruned after cache miss, got %d", pruned)
	}
}

func TestPruneTools_dropsUnusedAfterEnoughTurns(t *testing.T) {
	sess := session.NewStore().Get("s2")

	usedCall := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read_file", Arguments: `{"path":"x"}`},
		}},
	}
	// Simulate 4 turns where only read_file is ever called.
	for i := 0; i < minObservedTurnsForPrune+1; i++ {
		req := &ChatRequest{
			Messages: []Message{usedCall},
			Tools: []Tool{
				{Type: "function", Function: ToolFunction{Name: "read_file"}},
				{Type: "function", Function: ToolFunction{Name: "delete_repo"}},
			},
		}
		sess.ObserveToolCalls(ToolCallNames(req.Messages))
		_ = PruneTools(req, sess)
	}
	// Now check the final turn pruned delete_repo.
	req := &ChatRequest{
		Messages: []Message{usedCall},
		Tools: []Tool{
			{Type: "function", Function: ToolFunction{Name: "read_file"}},
			{Type: "function", Function: ToolFunction{Name: "delete_repo"}},
		},
	}
	sess.ObserveToolCalls(ToolCallNames(req.Messages))
	pruned := PruneTools(req, sess)
	if pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d", pruned)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "read_file" {
		t.Fatalf("wrong remaining tools: %+v", req.Tools)
	}
}

func TestDedupeToolResults_replacesOlderDuplicate(t *testing.T) {
	mkCall := func(id, name, args string) Message {
		return Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: id, Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: name, Arguments: args},
			}},
		}
	}
	mkResult := func(t2 *testing.T, callID, content string) Message {
		return Message{Role: "tool", ToolCallID: callID, Content: mustContent(t2, content)}
	}

	req := &ChatRequest{
		Messages: []Message{
			mkCall("c1", "read_file", `{"path":"a.go"}`),
			mkResult(t, "c1", "FULL FILE CONTENTS A"),
			{Role: "assistant", Content: mustContent(t, "ok")},
			mkCall("c2", "read_file", `{"path":"a.go"}`),
			mkResult(t, "c2", "FULL FILE CONTENTS A (re-read)"),
		},
	}
	stubbed := DedupeToolResults(req)
	if stubbed != 1 {
		t.Fatalf("expected 1 stubbed, got %d", stubbed)
	}
	// The first tool result (c1) should be the stub; c2 should be intact.
	var first string
	_ = json.Unmarshal(req.Messages[1].Content, &first)
	if !strings.Contains(first, "deduplicated by proxy") {
		t.Fatalf("expected first result stubbed, got: %s", first)
	}
	var last string
	_ = json.Unmarshal(req.Messages[4].Content, &last)
	if !strings.Contains(last, "FULL FILE CONTENTS A (re-read)") {
		t.Fatalf("expected last result intact, got: %s", last)
	}
}

func TestDedupeToolResults_doesNotDedupeDifferentArgs(t *testing.T) {
	req := &ChatRequest{
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c1", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"a.go"}`},
			}}},
			{Role: "tool", ToolCallID: "c1", Content: mustContent(t, "A")},
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c2", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"b.go"}`},
			}}},
			{Role: "tool", ToolCallID: "c2", Content: mustContent(t, "B")},
		},
	}
	if stubbed := DedupeToolResults(req); stubbed != 0 {
		t.Fatalf("expected 0 stubbed (different args), got %d", stubbed)
	}
}

func TestCompactHistory_truncatesOldToolOutputs(t *testing.T) {
	long := strings.Repeat("x", 6000)
	msgs := make([]Message, 0, recencyWindow+2)
	// Old tool output that should be truncated.
	msgs = append(msgs, Message{Role: "tool", ToolCallID: "old", Content: mustContent(t, long)})
	// Fillers to push it outside the recency window.
	for i := 0; i < recencyWindow+1; i++ {
		msgs = append(msgs, Message{Role: "user", Content: mustContent(t, "filler")})
	}
	req := &ChatRequest{Messages: msgs}

	touched := CompactHistory(req)
	if touched < 1 {
		t.Fatalf("expected at least the old tool message touched, got %d", touched)
	}
	var got string
	_ = json.Unmarshal(req.Messages[0].Content, &got)
	if !strings.Contains(got, "elided by proxy") {
		t.Fatalf("expected truncation marker, got: %s", got[:200])
	}
	if len(got) > toolOutputHead+toolOutputTail+200 {
		t.Fatalf("truncated message too long: %d", len(got))
	}
}

func TestCompactHistory_leavesRecentMessagesAlone(t *testing.T) {
	long := strings.Repeat("y", 6000)
	// Only 3 messages total: nothing should be outside recency.
	req := &ChatRequest{Messages: []Message{
		{Role: "tool", ToolCallID: "k", Content: mustContent(t, long)},
		{Role: "assistant", Content: mustContent(t, "ack")},
		{Role: "user", Content: mustContent(t, "next")},
	}}
	if touched := CompactHistory(req); touched != 0 {
		t.Fatalf("expected 0 touched (all recent), got %d", touched)
	}
}

func TestShapeForCache_detectsStablePrefix(t *testing.T) {
	store := session.NewStore()
	sess := store.Get("shape-test")

	build := func(userMsg string) *ChatRequest {
		return &ChatRequest{
			Messages: []Message{
				{Role: "system", Content: mustContent(t, "you are a helpful coding assistant")},
				{Role: "user", Content: mustContent(t, "first turn")},
				{Role: "assistant", Content: mustContent(t, "sure")},
				{Role: "user", Content: mustContent(t, userMsg)},
			},
		}
	}

	r1 := build("turn 2")
	if ShapeForCache(r1, sess) {
		t.Fatal("first call should report no stable prefix yet")
	}
	r2 := build("turn 3")
	if !ShapeForCache(r2, sess) {
		t.Fatal("second call should detect stable prefix")
	}
}
