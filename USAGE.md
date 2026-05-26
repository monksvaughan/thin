# Usage

Operator's guide for the proxy. For project context and the savings/latency
targets, see [README.md](README.md).

## License status

Without a commercial license, Thin remains fully functional and prints a short
free-license notice at startup. Commercial licenses are available for production
use, support, redistribution, embedding, or uses outside the Functional Source
License.

```bash
thin license status
thin license activate LICENSE_KEY
thin license remove
```

You can also provide a license key with the `THIN_LICENSE_KEY` environment
variable. License checks are not performed in the request path.

## Build

```bash
go build -o ./thin ./cmd/thin
```

Single static binary, ~15 MB. No CGo (SQLite is `modernc.org/sqlite`).

## Run

Start in dry-run for the first few sessions. Dry-run forwards the
*original* bytes upstream but still records what would have been saved —
no risk to the conversation, real numbers in `metrics.db`.

```bash
./thin \
  -listen :8080 \
  -upstream https://api.openai.com \
  -db ./metrics.db \
  -dry-run
```

When you trust the numbers, drop `-dry-run`.

By default, `/v1/chat/completions` routes to `https://api.openai.com` and
`/v1/messages` routes to `https://api.anthropic.com`. Override with `-upstream`
and `-anthropic-upstream` as needed.

Common upstreams:

| Upstream                | URL                                                          |
| ----------------------- | ------------------------------------------------------------ |
| OpenAI                  | `https://api.openai.com`                                     |
| Gemini (OpenAI-compat)  | `https://generativelanguage.googleapis.com/v1beta/openai`    |
| Anthropic native        | `https://api.anthropic.com`                                  |
| Anthropic via shim      | `http://<your-litellm-or-similar>:<port>`                    |
| Local llama.cpp / Ollama | `http://localhost:<port>`                                   |

The proxy forwards `Authorization` and every other header unchanged, so
your client sends its normal API key as if it were talking to the
upstream directly.

## Claude Code / Anthropic native mode

The proxy also rewrites Anthropic's native `/v1/messages` API, so Claude Code
can use it directly if your Claude Code version lets you set the Anthropic base
URL. Run against Anthropic:

```bash
./thin \
  -listen :8080 \
  -anthropic-upstream https://api.anthropic.com \
  -db ./metrics.db \
  -prune-log ./prune.log
```

Then configure Claude Code's Anthropic base URL to `http://localhost:8080` and
use your normal Anthropic API key. The exact environment variable/setting is
client-version dependent.

## Point a client at the proxy

Wherever the client lets you configure the model endpoint, change the
base URL:

```
https://api.openai.com/v1   →   http://localhost:8080/v1
```

Keep the same API key. The proxy rewrites `/v1/chat/completions` and
`/v1/messages`; other paths (`/v1/models`, `/v1/embeddings`, etc.) pass
through untouched, so nothing else in the client should break.

## Sanity checks before plugging in a real client

```bash
# Liveness
curl -s http://localhost:8080/healthz                    # → ok

# Round-trip a tiny completion
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

The proxy logs one line per request. If you see a "parse error, passing
through" line, the safety net worked — the upstream still got the
(original) request and your client got a real response.

## Monitor

### Live log

The proxy prints one line per request to stdout:

```
session=abc123def model=gpt-4o-mini in=5264->2454 (-53.4%) passes=[prune_tools dedupe_tool_results compact_history shape_cache] pipeline=221µs upstream=348ms
```

To background and tail:

```bash
./thin ... > ./proxy.log 2>&1 &
tail -f ./proxy.log
```

### Aggregate snapshot

Save this as `summary.sh` alongside `metrics.db`:

```bash
#!/usr/bin/env bash
sqlite3 ./metrics.db <<'SQL'
.mode column
.headers on

-- Per-session savings (top 15 by activity)
SELECT
  substr(session_id,1,12)                                              AS session,
  COUNT(*)                                                             AS turns,
  SUM(tokens_in)                                                       AS before,
  SUM(tokens_in_after)                                                 AS after,
  ROUND(100.0*(SUM(tokens_in)-SUM(tokens_in_after))/SUM(tokens_in),1)  AS save_pct,
  ROUND(100.0*SUM(cache_hit)/COUNT(*),0)                               AS cache_pct,
  ROUND(AVG(pipeline_latency_us)/1000.0,2)                             AS pipe_ms
FROM requests
GROUP BY session_id
ORDER BY turns DESC
LIMIT 15;

-- Overall
SELECT
  COUNT(*)                                                                          AS total_requests,
  ROUND(100.0*(SUM(tokens_in)-SUM(tokens_in_after))/NULLIF(SUM(tokens_in),0),1)     AS overall_save_pct,
  ROUND(100.0*SUM(cache_hit)/COUNT(*),0)                                            AS overall_cache_pct,
  (SELECT pipeline_latency_us FROM requests
    ORDER BY pipeline_latency_us
    LIMIT 1 OFFSET (SELECT COUNT(*)*95/100 FROM requests))                          AS p95_pipeline_us
FROM requests;
SQL
```

The `cache_pct` column shows the share of requests in each session that
hit the upstream's prompt cache. When it's high, `prune_tools` is
intentionally skipped to preserve the cached prefix — expect lower
`save_pct` and that's correct: the cache discount on the cached portion
is bigger than the local prune savings would be.

On demand:

```bash
bash summary.sh
```

Live refresh every 30s:

```bash
watch -n 30 bash summary.sh
```

More queries (pass effectiveness, latency percentiles) live in
[README.md](README.md#how-to-read-the-metrics).

## Stop

```bash
pkill -f /thin   # if backgrounded
```

Or `Ctrl-C` if running in the foreground. Session state lives only in
memory and is discarded on shutdown; `metrics.db` is preserved.
