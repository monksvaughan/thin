package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// DedupeToolResults finds tool-result messages whose corresponding call
// (function name + arguments) appears multiple times in history, and
// replaces the older ones with a tiny stub pointing to the latest result.
//
// This catches the "agent re-read the same file four times" pattern that
// the obviousworks.ch post called out as a major waste driver.
//
// Strategy:
//  1. Walk messages, build a map of tool_call_id -> (fn_name, args_hash).
//  2. For each tool result message (role=tool, tool_call_id=X), find the
//     hash of that call.
//  3. If we've seen the same hash with a later tool result already, replace
//     this older message's content with a stub.
//
// We process from newest to oldest so the latest occurrence is the one
// that gets kept in full.
//
// DedupedToolResult records one older tool result stubbed by DedupeToolResults.
type DedupedToolResult struct {
	MessageIndex int    `json:"message_index"`
	ToolCallID   string `json:"tool_call_id"`
	BeforeBytes  int    `json:"before_bytes"`
	AfterBytes   int    `json:"after_bytes"`
	Reason       string `json:"reason"`
}

// Returns the number of messages stubbed.
func DedupeToolResults(req *ChatRequest) int {
	return len(DedupeToolResultsWithReport(req))
}

func DedupeToolResultsWithReport(req *ChatRequest) []DedupedToolResult {
	// Index tool_call_id -> hash via the assistant messages.
	hashByCallID := map[string]string{}
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			hashByCallID[tc.ID] = hashCall(tc.Function.Name, tc.Function.Arguments)
		}
	}

	// Walk tool messages in reverse order; first sighting per call hash wins.
	// Only stub older results when the output is byte-for-byte identical too:
	// repeated calls such as run_tests{} or read_file(path) can legitimately
	// change over time, and removing that delta would corrupt the history.
	seenContentByCall := map[string]map[string]bool{}
	var stubbed []DedupedToolResult
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := &req.Messages[i]
		if m.Role != "tool" || m.ToolCallID == "" {
			continue
		}
		callHash, ok := hashByCallID[m.ToolCallID]
		if !ok {
			continue
		}
		contentHash := hashContent(m.Content)
		seenContent, seenCall := seenContentByCall[callHash]
		if !seenCall {
			seenContentByCall[callHash] = map[string]bool{contentHash: true}
			continue
		}
		if !seenContent[contentHash] {
			seenContent[contentHash] = true
			continue
		}
		// Older duplicate with identical output. Replace content with a stub.
		stub, _ := json.Marshal(fmt.Sprintf(
			"[deduplicated by proxy: identical tool call output appears later in this conversation]",
		))
		beforeBytes := len(m.Content)
		m.Content = stub
		stubbed = append(stubbed, DedupedToolResult{
			MessageIndex: i,
			ToolCallID:   m.ToolCallID,
			BeforeBytes:  beforeBytes,
			AfterBytes:   len(stub),
			Reason:       "identical_tool_call_and_output_appears_later",
		})
	}
	return stubbed
}

func hashCall(name, args string) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(args))
	return hex.EncodeToString(h.Sum(nil))
}

func hashContent(content json.RawMessage) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}
