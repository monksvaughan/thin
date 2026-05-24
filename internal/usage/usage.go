// Package usage extracts prompt-cache signals from upstream LLM responses.
//
// The proxy doesn't generally parse upstream bodies — it just forwards them.
// The one exception is the usage block, which tells us whether the upstream
// hit its prompt cache for this request. We use that to make prune_tools
// adaptive: when the cache is hitting, changing the tools array would bust
// the cached prefix and re-pay full price, so we skip the prune.
//
// We scan rather than JSON-parse for two reasons:
//   1. For streaming SSE bodies, we may only have a tail of the stream — not
//      a complete JSON document.
//   2. We don't care about anything else in the body, so paying to parse the
//      whole thing would be wasteful.
//
// We look for two keys, in order:
//   - OpenAI: usage.prompt_tokens_details.cached_tokens
//   - Anthropic (via OpenAI-compat shims that pass it through): cache_read_input_tokens
//
// If neither is present we report "no signal" — the caller decides what to do
// (today: fall back to today's pruning behavior).
package usage

import "bytes"

// openAIKey and anthropicKey are the JSON keys we scan for. We require the
// trailing colon so we don't match similarly-named keys inside content
// strings (unlikely but possible).
var (
	openAIKey    = []byte(`"cached_tokens":`)
	anthropicKey = []byte(`"cache_read_input_tokens":`)
)

// ExtractCacheHit scans body for a cache-hit token count. body may be a
// complete JSON response or just the tail of an SSE stream. It returns
// (cachedTokens, true) on the LAST occurrence of either recognized key,
// or (0, false) if no usage signal was found.
//
// Callers should treat (0, true) as "we saw usage info but it wasn't a hit"
// and (0, false) as "no signal — make no claim either way."
func ExtractCacheHit(body []byte) (int, bool) {
	if n, ok := scanIntAfter(body, openAIKey); ok {
		return n, true
	}
	if n, ok := scanIntAfter(body, anthropicKey); ok {
		return n, true
	}
	return 0, false
}

// scanIntAfter finds the LAST occurrence of needle in body and parses the
// non-negative integer that follows (skipping whitespace). Returns the
// parsed value and true on success.
func scanIntAfter(body, needle []byte) (int, bool) {
	idx := bytes.LastIndex(body, needle)
	if idx < 0 {
		return 0, false
	}
	rest := body[idx+len(needle):]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
		i++
	}
	if i == len(rest) || rest[i] < '0' || rest[i] > '9' {
		return 0, false
	}
	n := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		n = n*10 + int(rest[i]-'0')
		i++
	}
	return n, true
}
