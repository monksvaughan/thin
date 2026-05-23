package pipeline

import (
	"encoding/json"
	"regexp"
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

// CompactHistory returns the number of messages it touched.
func CompactHistory(req *ChatRequest) int {
	n := len(req.Messages)
	if n <= recencyWindow {
		return 0
	}
	cutoff := n - recencyWindow

	touched := 0
	for i := 0; i < cutoff; i++ {
		m := &req.Messages[i]
		if compactMessage(m) {
			touched++
		}
	}
	return touched
}

// compactMessage rewrites m.Content in place if useful. Returns true if it
// actually changed anything. Skips messages whose content isn't a plain
// string — we don't want to disturb multimodal blocks or cache_control
// annotations.
func compactMessage(m *Message) bool {
	if len(m.Content) == 0 {
		return false
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err != nil {
		// Content is a complex value (parts array). Leave it alone for v1.
		return false
	}
	original := s

	// Cheap whitespace cleanup, always safe.
	s = trailingWS.ReplaceAllString(s, "\n")
	s = blankLines.ReplaceAllString(s, "\n\n")
	s = strings.TrimRight(s, " \t\n")

	// Aggressive truncation only for tool-role messages (file dumps, logs).
	if m.Role == "tool" && len(s) > toolOutputTruncateThreshold {
		head := s[:toolOutputHead]
		tail := s[len(s)-toolOutputTail:]
		omitted := len(s) - toolOutputHead - toolOutputTail
		s = head +
			"\n\n... [" +
			itoa(omitted) +
			" chars elided by proxy — old tool output, full version not retained] ...\n\n" +
			tail
	}

	if s == original {
		return false
	}

	enc, err := json.Marshal(s)
	if err != nil {
		return false
	}
	m.Content = enc
	return true
}

func itoa(n int) string {
	// Tiny helper, avoids strconv import here.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
