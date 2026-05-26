package pipeline

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// CompactHistory applies cheap, lossless-or-near-lossless text reductions
// to older messages. No model calls, no semantic compression — just the
// kind of cleanup a careful human would do if asked to fit the conversation
// into a smaller window without changing its meaning.
//
// Recent messages are left alone: the assistant needs them intact to reason.
const recencyWindow = 6

// Long tool outputs (file dumps, build logs) get truncated to head + tail
// once they're outside the recency window. The size threshold and head/tail
// budgets are deliberately conservative for v1 — we'd rather under-save
// than corrupt context.
const (
	toolOutputTruncateThreshold = 4000 // chars
	toolOutputHead              = 800
	toolOutputTail              = 400
)

var (
	// Collapse runs of 3+ blank lines into 2.
	blankLines = regexp.MustCompile(`\n{3,}`)
	// Trailing whitespace on each line.
	trailingWS = regexp.MustCompile(`[ \t]+\n`)
)

// CompactedMessage records one message compacted by CompactHistory.
type CompactedMessage struct {
	MessageIndex int    `json:"message_index"`
	Role         string `json:"role"`
	BeforeBytes  int    `json:"before_bytes"`
	AfterBytes   int    `json:"after_bytes"`
	Truncated    bool   `json:"truncated"`
	Reason       string `json:"reason"`
}

// CompactHistory returns the number of messages it touched.
func CompactHistory(req *ChatRequest) int {
	return len(CompactHistoryWithReport(req))
}

func CompactHistoryWithReport(req *ChatRequest) []CompactedMessage {
	n := len(req.Messages)
	if n <= recencyWindow {
		return nil
	}
	cutoff := n - recencyWindow

	var touched []CompactedMessage
	for i := 0; i < cutoff; i++ {
		m := &req.Messages[i]
		if report, ok := compactMessage(i, m); ok {
			touched = append(touched, report)
		}
	}
	return touched
}

// compactMessage rewrites m.Content in place if useful. Skips messages whose
// content isn't a plain string — we don't want to disturb multimodal blocks or
// cache_control annotations.
func compactMessage(index int, m *Message) (CompactedMessage, bool) {
	if len(m.Content) == 0 {
		return CompactedMessage{}, false
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err != nil {
		// Content is a complex value (parts array). Leave it alone for v1.
		return CompactedMessage{}, false
	}
	original := s
	originalBytes := len(m.Content)
	reasons := []string{}

	// Cheap whitespace cleanup, always safe.
	s = trailingWS.ReplaceAllString(s, "\n")
	s = blankLines.ReplaceAllString(s, "\n\n")
	s = strings.TrimRight(s, " \t\n")
	if s != original {
		reasons = append(reasons, "whitespace_cleanup")
	}

	// Aggressive truncation only for tool-role messages (file dumps, logs).
	truncated := false
	if m.Role == "tool" && len(s) > toolOutputTruncateThreshold {
		head := s[:toolOutputHead]
		tail := s[len(s)-toolOutputTail:]
		omitted := len(s) - toolOutputHead - toolOutputTail
		s = head +
			"\n\n... [" +
			strconv.Itoa(omitted) +
			" chars elided by proxy — old tool output, full version not retained] ...\n\n" +
			tail
		truncated = true
		reasons = append(reasons, "tool_output_head_tail_truncate")
	}

	if s == original {
		return CompactedMessage{}, false
	}

	enc, err := json.Marshal(s)
	if err != nil {
		return CompactedMessage{}, false
	}
	if len(enc) >= originalBytes {
		return CompactedMessage{}, false
	}
	m.Content = enc
	return CompactedMessage{
		MessageIndex: index,
		Role:         m.Role,
		BeforeBytes:  originalBytes,
		AfterBytes:   len(enc),
		Truncated:    truncated,
		Reason:       strings.Join(reasons, ","),
	}, true
}
