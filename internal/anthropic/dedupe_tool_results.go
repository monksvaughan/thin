package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type DedupedToolResult struct {
	MessageIndex int    `json:"message_index"`
	ToolUseID    string `json:"tool_use_id"`
	BeforeBytes  int    `json:"before_bytes"`
	AfterBytes   int    `json:"after_bytes"`
	Reason       string `json:"reason"`
}

func DedupeToolResults(req *MessagesRequest) int {
	return len(DedupeToolResultsWithReport(req))
}

func DedupeToolResultsWithReport(req *MessagesRequest) []DedupedToolResult {
	hashByToolUseID := map[string]string{}
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		blocks, ok := contentBlocks(m.Content)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" {
				hashByToolUseID[b.ID] = hashToolUse(b.Name, b.Input)
			}
		}
	}

	seenContentByCall := map[string]map[string]bool{}
	var stubbed []DedupedToolResult
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := &req.Messages[i]
		if m.Role != "user" {
			continue
		}
		blocks, ok := contentBlocks(m.Content)
		if !ok {
			continue
		}
		changed := false
		for j := range blocks {
			b := &blocks[j]
			if b.Type != "tool_result" || b.ToolUseID == "" {
				continue
			}
			callHash, ok := hashByToolUseID[b.ToolUseID]
			if !ok {
				continue
			}
			contentHash := hashRaw(b.Content)
			seenContent, seenCall := seenContentByCall[callHash]
			if !seenCall {
				seenContentByCall[callHash] = map[string]bool{contentHash: true}
				continue
			}
			if !seenContent[contentHash] {
				seenContent[contentHash] = true
				continue
			}
			stub, _ := json.Marshal("[deduplicated by proxy: identical tool call output appears later in this conversation]")
			beforeBytes := len(b.Content)
			b.Content = stub
			stubbed = append(stubbed, DedupedToolResult{
				MessageIndex: i,
				ToolUseID:    b.ToolUseID,
				BeforeBytes:  beforeBytes,
				AfterBytes:   len(stub),
				Reason:       "identical_tool_call_and_output_appears_later",
			})
			changed = true
		}
		if changed {
			enc, err := json.Marshal(blocks)
			if err == nil {
				m.Content = enc
			}
		}
	}
	return stubbed
}

func hashToolUse(name string, input json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write(input)
	return hex.EncodeToString(h.Sum(nil))
}

func hashRaw(raw json.RawMessage) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
