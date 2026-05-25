package pipeline

import (
	"encoding/json"
	"testing"
)

// jsonEqual reports whether two JSON byte slices are equivalent ignoring
// key order and whitespace. We can't string-compare because Go map iteration
// order isn't stable across marshals.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("invalid JSON a: %v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("invalid JSON b: %v\n%s", err, b)
	}
	an, _ := json.Marshal(av)
	bn, _ := json.Marshal(bv)
	return string(an) == string(bn)
}

// TestChatRequest_RoundTripPreservesUnknownTopLevelFields covers the existing
// Extra-map mechanism on the request struct. Any field we don't model
// explicitly (response_format, reasoning_effort, etc.) must come out the
// other side untouched, or we silently mangle upstream behavior.
func TestChatRequest_RoundTripPreservesUnknownTopLevelFields(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [{"role":"user","content":"hi"}],
		"response_format": {"type":"json_object"},
		"reasoning_effort": "medium",
		"temperature": 0.2
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("round-trip diverged\nin:  %s\nout: %s", in, out)
	}
}

// TestTool_RoundTripPreservesStrictField is the canonical failure case:
// OpenAI's structured-outputs feature relies on `strict: true` on the
// ToolFunction. If we drop it, the upstream falls back to non-strict mode
// and the client's structured outputs silently break.
func TestTool_RoundTripPreservesStrictField(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "lookup",
				"description": "look something up",
				"parameters": {"type":"object","properties":{}},
				"strict": true
			}
		}]
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("strict field dropped on round-trip\nin:  %s\nout: %s", in, out)
	}
}

// TestTool_RoundTripPreservesUnknownToolLevelFields covers fields at the
// Tool level (one above function). Anthropic and some shims add fields here.
func TestTool_RoundTripPreservesUnknownToolLevelFields(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"type": "function",
			"function": {"name":"t","parameters":{}},
			"cache_control": {"type":"ephemeral"}
		}]
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("tool-level field dropped\nin:  %s\nout: %s", in, out)
	}
}

// TestMessage_RoundTripPreservesPartsContent covers the multimodal/parts
// case that compact_history is documented to skip. The content here is a
// JSON array, not a string — we must not corrupt it.
func TestMessage_RoundTripPreservesUnknownMessageFields(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [{
			"role":"assistant",
			"content":"thinking",
			"cache_control": {"type":"ephemeral"},
			"recipient": "tool_namespace"
		}]
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("message-level field dropped\nin:  %s\nout: %s", in, out)
	}
}

func TestMessage_RoundTripPreservesUnknownToolCallFields(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [{
			"role":"assistant",
			"tool_calls": [{
				"id":"call_1",
				"type":"function",
				"function":{"name":"lookup","arguments":"{}"},
				"index": 0
			}]
		}]
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("tool-call-level field dropped\nin:  %s\nout: %s", in, out)
	}
}

func TestMessage_RoundTripPreservesPartsContent(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o-mini",
		"messages": [
			{"role":"user","content":[
				{"type":"text","text":"describe this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}
			]}
		]
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("parts content corrupted\nin:  %s\nout: %s", in, out)
	}
}
