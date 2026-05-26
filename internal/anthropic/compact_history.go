package anthropic

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const recencyWindow = 6

const (
	toolOutputTruncateThreshold = 4000
	toolOutputHead              = 800
	toolOutputTail              = 400
)

var (
	blankLines = regexp.MustCompile(`\n{3,}`)
	trailingWS = regexp.MustCompile(`[ \t]+\n`)
)

type CompactedMessage struct {
	MessageIndex int    `json:"message_index"`
	Role         string `json:"role"`
	BeforeBytes  int    `json:"before_bytes"`
	AfterBytes   int    `json:"after_bytes"`
	Truncated    bool   `json:"truncated"`
	Reason       string `json:"reason"`
}

func CompactHistory(req *MessagesRequest) int {
	return len(CompactHistoryWithReport(req))
}

func CompactHistoryWithReport(req *MessagesRequest) []CompactedMessage {
	n := len(req.Messages)
	if n <= recencyWindow {
		return nil
	}
	cutoff := n - recencyWindow
	var touched []CompactedMessage
	for i := 0; i < cutoff; i++ {
		if report, ok := compactMessage(i, &req.Messages[i]); ok {
			touched = append(touched, report)
		}
	}
	return touched
}

func compactMessage(index int, m *Message) (CompactedMessage, bool) {
	if len(m.Content) == 0 {
		return CompactedMessage{}, false
	}
	beforeBytes := len(m.Content)
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		compact := compactText(s)
		if compact == s {
			return CompactedMessage{}, false
		}
		enc, err := json.Marshal(compact)
		if err != nil {
			return CompactedMessage{}, false
		}
		if len(enc) >= beforeBytes {
			return CompactedMessage{}, false
		}
		m.Content = enc
		return CompactedMessage{MessageIndex: index, Role: m.Role, BeforeBytes: beforeBytes, AfterBytes: len(enc), Reason: "whitespace_cleanup"}, true
	}

	blocks, ok := contentBlocks(m.Content)
	if !ok {
		return CompactedMessage{}, false
	}
	changed := false
	truncated := false
	reasons := map[string]bool{}
	for i := range blocks {
		b := &blocks[i]
		switch b.Type {
		case "text":
			compact := compactText(b.Text)
			if compact != b.Text {
				b.Text = compact
				changed = true
				reasons["whitespace_cleanup"] = true
			}
		case "tool_result":
			if didTruncate, ok := compactToolResult(b); ok {
				changed = true
				reasons["tool_result_cleanup"] = true
				if didTruncate {
					truncated = true
					reasons["tool_output_head_tail_truncate"] = true
				}
			}
		}
	}
	if !changed {
		return CompactedMessage{}, false
	}
	enc, err := json.Marshal(blocks)
	if err != nil {
		return CompactedMessage{}, false
	}
	if len(enc) >= beforeBytes {
		return CompactedMessage{}, false
	}
	m.Content = enc
	return CompactedMessage{MessageIndex: index, Role: m.Role, BeforeBytes: beforeBytes, AfterBytes: len(enc), Truncated: truncated, Reason: joinReasons(reasons)}, true
}

func compactToolResult(b *ContentBlock) (bool, bool) {
	if len(b.Content) == 0 {
		return false, false
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err != nil {
		return false, false
	}
	original := s
	s = compactText(s)
	truncated := false
	if len(s) > toolOutputTruncateThreshold {
		head := s[:toolOutputHead]
		tail := s[len(s)-toolOutputTail:]
		omitted := len(s) - toolOutputHead - toolOutputTail
		s = head + "\n\n... [" + strconv.Itoa(omitted) + " chars elided by proxy — old tool output, full version not retained] ...\n\n" + tail
		truncated = true
	}
	if s == original {
		return false, false
	}
	enc, err := json.Marshal(s)
	if err != nil {
		return false, false
	}
	b.Content = enc
	return truncated, true
}

func joinReasons(reasons map[string]bool) string {
	out := []string{}
	for reason := range reasons {
		out = append(out, reason)
	}
	return strings.Join(out, ",")
}

func compactText(s string) string {
	s = trailingWS.ReplaceAllString(s, "\n")
	s = blankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimRight(s, " \t\n")
}
