package usage

import "testing"

func TestExtractCacheHit_OpenAINonStreaming(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-x",
		"choices": [{"message": {"content": "hi"}}],
		"usage": {
			"prompt_tokens": 1100,
			"completion_tokens": 5,
			"prompt_tokens_details": {"cached_tokens": 1024}
		}
	}`)
	n, ok := ExtractCacheHit(body)
	if !ok {
		t.Fatal("expected to find a cache signal")
	}
	if n != 1024 {
		t.Fatalf("expected 1024, got %d", n)
	}
}

func TestExtractCacheHit_OpenAIStreamingTail(t *testing.T) {
	// Realistic tail of an SSE stream: many content chunks, then a final
	// usage chunk, then [DONE].
	body := []byte(`data: {"id":"x","choices":[{"delta":{"content":"the"}}]}

data: {"id":"x","choices":[{"delta":{"content":" end"}}]}

data: {"id":"x","choices":[{"finish_reason":"stop"}]}

data: {"id":"x","choices":[],"usage":{"prompt_tokens":2000,"prompt_tokens_details":{"cached_tokens":1900}}}

data: [DONE]

`)
	n, ok := ExtractCacheHit(body)
	if !ok {
		t.Fatal("expected to find cache signal in SSE tail")
	}
	if n != 1900 {
		t.Fatalf("expected 1900, got %d", n)
	}
}

func TestExtractCacheHit_OpenAIZeroCachedTokens(t *testing.T) {
	// Usage present, no cache hit. Should return (0, true) — we have a
	// signal, it just says no hit.
	body := []byte(`{"usage":{"prompt_tokens_details":{"cached_tokens":0}}}`)
	n, ok := ExtractCacheHit(body)
	if !ok {
		t.Fatal("expected signal present")
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestExtractCacheHit_AnthropicShape(t *testing.T) {
	body := []byte(`{"usage":{"cache_read_input_tokens":4096,"input_tokens":4200}}`)
	n, ok := ExtractCacheHit(body)
	if !ok {
		t.Fatal("expected to find Anthropic-shape signal")
	}
	if n != 4096 {
		t.Fatalf("expected 4096, got %d", n)
	}
}

func TestExtractCacheHit_NoUsageBlock(t *testing.T) {
	// Common case: client didn't request usage in the SSE stream.
	body := []byte(`data: {"choices":[{"delta":{"content":"hello"}}]}

data: [DONE]

`)
	_, ok := ExtractCacheHit(body)
	if ok {
		t.Fatal("expected no signal when usage block is absent")
	}
}

func TestExtractCacheHit_EmptyBody(t *testing.T) {
	_, ok := ExtractCacheHit(nil)
	if ok {
		t.Fatal("expected no signal for empty body")
	}
}

// If the body has multiple usage blocks (very unusual but possible with
// some proxies that include intermediate stats), we should report the LAST
// one — that's the most recent / final usage state.
func TestExtractCacheHit_TakesLastOccurrence(t *testing.T) {
	body := []byte(`{"early":"usage","cached_tokens": 100, "final":{"cached_tokens": 500}}`)
	n, ok := ExtractCacheHit(body)
	if !ok {
		t.Fatal("expected signal")
	}
	if n != 500 {
		t.Fatalf("expected last occurrence (500), got %d", n)
	}
}

// Truncated tail: the needle is present but the value is cut off. We
// should report no signal rather than misparse.
func TestExtractCacheHit_TruncatedValue(t *testing.T) {
	body := []byte(`...usage":{"cached_tokens":`)
	_, ok := ExtractCacheHit(body)
	if ok {
		t.Fatal("expected no signal for truncated value")
	}
}
