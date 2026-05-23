// Package tokens provides a fast token-count estimator for chat requests.
//
// We use cl100k_base via tiktoken-go. For OpenAI models this is exact for
// gpt-3.5/4-class models and close enough for gpt-4o/gpt-5; for Anthropic
// the tokenizer is different but the *delta* between two cl100k counts of
// the same text is a usable proxy for the delta in real Anthropic tokens.
// The prototype optimizes for delta accuracy, not absolute accuracy.
//
// A production version should call out to the upstream's token-counting
// endpoint when available (Anthropic exposes /v1/messages/count_tokens).
package tokens

import (
	"encoding/json"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// Counter is a reusable token counter. The underlying tiktoken Encoding
// is safe for concurrent use.
type Counter struct {
	enc  *tiktoken.Tiktoken
	once sync.Once
	err  error
}

// New constructs a Counter. The encoding is loaded lazily on first Count.
func New() *Counter {
	return &Counter{}
}

func (c *Counter) ensure() error {
	c.once.Do(func() {
		enc, err := tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			c.err = err
			return
		}
		c.enc = enc
	})
	return c.err
}

// CountString returns the token count for a single string.
func (c *Counter) CountString(s string) int {
	if err := c.ensure(); err != nil {
		// Fall back to a byte-based estimate so we never block the proxy
		// on a tokenizer load failure.
		return len(s) / 4
	}
	return len(c.enc.Encode(s, nil, nil))
}

// CountRequest sums tokens across messages, tool definitions, and the
// model name. It deliberately ignores the small per-message framing
// overhead (~3 tokens per message) — that's a fixed offset and doesn't
// affect deltas, which is what we care about.
func (c *Counter) CountRequest(req any) int {
	b, err := json.Marshal(req)
	if err != nil {
		return 0
	}
	return c.CountString(string(b))
}
