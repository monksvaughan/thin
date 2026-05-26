# thin (weekend prototype)

An OpenAI-compatible and Anthropic Messages API HTTP proxy that tries to cut
the input-token bill of agentic coding clients (Claude Code, Cursor, OpenCode,
etc.) **without calling any model itself**. Pure string and JSON manipulation.

The goal of this prototype is **not** to be production-ready. It's to
answer one question honestly:

> If we add zero model calls and effectively zero latency, can we still
> save 25–40% on input tokens for a real coding workload?

If the answer is yes, this is a product. If it's 8%, it isn't, and we'll
have saved ourselves a lot of LLMLingua-shaped pain.

## What it does

Four passes, applied in order to every supported request (`/v1/chat/completions` for OpenAI-compatible clients and `/v1/messages` for Anthropic-native clients):

| Pass | What it does | Risk |
| --- | --- | --- |
| `prune_tools` | Drops function-tool schemas the session has never invoked, after 3+ observed turns. | Low. Conservative gating means we only drop tools with strong evidence of disuse. |
| `dedupe_tool_results` | If the same tool call (function + args) appears multiple times in history, replaces the older results with a 1-line stub. | Low–medium. Catches the agent-re-reads-the-same-file pattern. The stub points the model at the later, intact result. |
| `compact_history` | Whitespace cleanup + head/tail truncation of tool outputs that fall outside the recency window (last 6 messages). | Low for whitespace; medium for truncation. The threshold (4KB) is conservative. |
| `shape_cache` | Detects the stable prefix vs. last turn. Currently measurement-only — does NOT mutate the request. Future v2 would insert Anthropic `cache_control` breakpoints. | None (no mutation). |

Anything we can't parse confidently we pass through untouched.

## What it deliberately does NOT do

- **No model calls.** No LLMLingua, no embedding-based semantic cache, no
  summarization LLM. That's a v2 question once we know v1 pays for itself.
- **No lossy compression of code.** Whitespace cleanup only on code-bearing
  messages. Truncation only on tool outputs (file reads, build logs).
- **No reordering of messages.** Tempting for cache shaping but risky —
  some agent loops depend on positional cues.

## Running it

```bash
# Against OpenAI
go run ./cmd/thin -upstream https://api.openai.com -listen :8080

# Anthropic native /v1/messages defaults to https://api.anthropic.com
# while OpenAI-compatible /v1/chat/completions defaults to https://api.openai.com.
go run ./cmd/thin -listen :8080

# Measurement-only mode: emit the original request upstream, but log
# what we WOULD have saved. Use this for the first few sessions to build
# confidence before flipping to active mode.
go run ./cmd/thin -upstream https://api.openai.com -dry-run
```

Point your coding client at `http://localhost:8080/v1` with your normal
API key (the proxy forwards `Authorization` unchanged).

See [USAGE.md](USAGE.md) for the full operator's guide: OpenAI-compatible
client setup, Anthropic/Claude Code setup, monitoring, and validation.

## How to read the metrics

Every request appends a row to `metrics.db` (SQLite). Useful queries:

```sql
-- Per-session savings
SELECT
  session_id,
  COUNT(*)                              AS turns,
  SUM(tokens_in)                        AS tokens_before,
  SUM(tokens_in_after)                  AS tokens_after,
  ROUND(100.0 * (SUM(tokens_in) - SUM(tokens_in_after)) / SUM(tokens_in), 1)
                                        AS savings_pct,
  ROUND(AVG(pipeline_latency_us) / 1000.0, 2)
                                        AS avg_pipeline_ms
FROM requests
GROUP BY session_id
ORDER BY turns DESC;

-- Which passes are pulling their weight
SELECT passes, COUNT(*) AS n, AVG(tokens_in - tokens_in_after) AS avg_saved
FROM requests
WHERE passes != ''
GROUP BY passes
ORDER BY n DESC;

-- p50 / p95 pipeline latency. If p95 is above ~5ms we have a problem.
SELECT
  COUNT(*) AS n,
  (SELECT pipeline_latency_us FROM requests ORDER BY pipeline_latency_us
   LIMIT 1 OFFSET (SELECT COUNT(*)/2 FROM requests))   AS p50_us,
  (SELECT pipeline_latency_us FROM requests ORDER BY pipeline_latency_us
   LIMIT 1 OFFSET (SELECT COUNT(*) * 95/100 FROM requests)) AS p95_us
FROM requests;
```

## Pass / fail criteria for the weekend

After running for a few days against your own Claude Code or Cursor usage:

- **Median savings ≥ 25%** across non-trivial sessions (turns ≥ 5). If
  most sessions show < 10%, the product isn't there.
- **p95 pipeline latency < 5ms.** If we're adding noticeable overhead, the
  trade is bad regardless of savings.
- **Zero broken requests.** A single misbehaving agent loop kills the
  trust story. Watch the `status` column for 4xx/5xx that don't appear
  in dry-run.

## Project layout

```
cmd/thin/main.go                 HTTP server, request lifecycle / binary entrypoint
internal/pipeline/
  types.go                       Chat request schema (OpenAI-compat)
  pipeline.go                    Pass orchestrator + Result type
  prune_tools.go                 Pass 1
  dedupe_tool_results.go         Pass 2
  compact_history.go             Pass 3
  shape_cache.go                 Pass 4
  pipeline_test.go               Unit tests for each pass
internal/session/
  session.go                     Per-session tool-usage + fingerprint tracking
internal/tokens/
  tokens.go                      tiktoken-based counter
internal/metrics/
  metrics.go                     SQLite persistence
```

## License

Thin is licensed under the Functional Source License 1.1, ALv2 future license.
Permitted uses include internal business use and non-production use. Commercial
competing use requires a separate license. Each release converts to Apache 2.0
two years after publication.

See [LICENSE](LICENSE) and [LICENSE-NOTES.md](LICENSE-NOTES.md).

## Known weaknesses (call them out before HN does)

1. **Token counting is approximate.** We use cl100k_base for both OpenAI
   and Anthropic. Deltas are honest; absolute counts for Anthropic are off
   by some constant factor. Production: call the upstream's own counter.
2. **No streaming-response usage parsing.** Output-token numbers are
   byte-count estimates. To fix: parse the SSE `usage` chunk if the client
   asked for it.
3. **Session affinity is implicit.** We hash system prompt + first user
   message. Multi-tenant deployments need an explicit `X-Session-Id` header
   (already supported, just not required).
4. **No retry / failure handling around upstream errors.** The reverse
   proxy will surface them as-is. Good enough for measurement; not good
   enough to charge for.
5. **In-memory session state.** Restart = forget. Production: Redis.
