# thin

Thin is a low-latency HTTP proxy for reducing input-token usage in AI coding
workflows.

It sits between agentic coding clients and upstream model APIs, applying fast
local heuristics to supported requests before forwarding them. The design goal
is simple: lower prompt size and cost while adding negligible latency to the
interactive coding loop.

Thin supports both:

- OpenAI-compatible Chat Completions: `/v1/chat/completions`
- Anthropic Messages API: `/v1/messages`

It is intended for coding agents and developer tools that repeatedly send large
conversation histories, tool definitions, command output, file contents, and
other context-heavy payloads.

## Highlights

- **Very low latency** — request processing is designed to complete in a few
  milliseconds or less.
- **Local heuristic processing** — no additional model calls are introduced by
  the proxy.
- **OpenAI-compatible and Anthropic-native routes** — one proxy can front both
  common API shapes.
- **Safe fallback behavior** — requests that cannot be confidently processed are
  forwarded unchanged.
- **Dry-run mode** — measure expected savings without changing upstream traffic.
- **SQLite metrics** — every request can be logged for savings and latency
  analysis.
- **Streaming friendly** — server-sent-event responses are proxied without
  buffering the interactive stream.

## Observed performance

Recent local development logs show the kind of performance Thin is designed
for. This is not a formal benchmark, but it reflects real proxy runs from this
repository's metrics database.

Sample: **130 requests** across **11 sessions**, covering approximately **10.0M
input tokens** before processing.

| Slice | Requests | Input-token reduction | Pipeline latency |
| --- | ---: | ---: | ---: |
| Overall sample | 130 | 36.6% weighted reduction | p50 3.1ms, p95 41.9ms |
| OpenAI-compatible traffic | 114 | 38.0% weighted reduction | p50 3.0ms, p95 9.2ms |
| Anthropic-native traffic | 16 | 31.9% weighted reduction | p50 41.6ms, p95 45.9ms |
| Sessions with 5+ turns | 5 sessions | 32.7% median reduction | — |

Pipeline latency measures Thin's local request processing only. It excludes
network time, upstream model latency, and response streaming time.

## Quick start

Run against OpenAI-compatible traffic:

```bash
go run ./cmd/thin -upstream https://api.openai.com -listen :8080
```

Run with default upstreams:

```bash
go run ./cmd/thin -listen :8080
```

By default:

- `/v1/chat/completions` routes to `https://api.openai.com`
- `/v1/messages` routes to `https://api.anthropic.com`

Point your coding client at:

```text
http://localhost:8080/v1
```

Use your normal API key. Thin forwards authorization headers to the upstream
provider.

## Dry-run mode

Dry-run mode forwards the original request upstream but records what Thin would
have sent after processing. This is useful for validating savings and latency
before enabling active rewriting.

```bash
go run ./cmd/thin \
  -upstream https://api.openai.com \
  -listen :8080 \
  -dry-run
```

## Build

```bash
go build -o ./thin ./cmd/thin
```

Print build information:

```bash
./thin version
```

Release builds can populate version information with Go linker flags.

## Licensing and commercial use

Thin is source-available under the Functional Source License.

It is free for personal, evaluation, and non-commercial use. Commercial licenses
are available for production use, support, redistribution, embedding, or uses
outside the Functional Source License.

Thin does not perform license checks in the request path and does not collect
usage telemetry.

Manage a local commercial license code with:

```bash
thin license status
thin license activate LICENSE_KEY
thin license remove
```

A license key may also be supplied with the `THIN_LICENSE_KEY` environment
variable. Without a commercial license, Thin remains fully functional and prints
a short free-license notice at startup.

## Install

Build from source:

```bash
go build -o ./thin ./cmd/thin
```

Homebrew installation will be available from the project tap after the first
published release:

```bash
brew tap monksvaughan/tap
brew install thin
```

## Operator guide

See [USAGE.md](USAGE.md) for setup examples, Anthropic/Claude Code notes,
monitoring, validation, and operational commands.

See [docs/releasing.md](docs/releasing.md) for versioning, release builds, and
Homebrew publishing.

## Metrics

Thin writes request metrics to SQLite by default. The database records token
counts before and after processing, latency, upstream status, applied processing
categories, byte counts, and cache-signal information where available.

Example savings query:

```sql
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
```

Example latency query:

```sql
SELECT
  COUNT(*) AS n,
  (SELECT pipeline_latency_us FROM requests ORDER BY pipeline_latency_us
   LIMIT 1 OFFSET (SELECT COUNT(*)/2 FROM requests)) AS p50_us,
  (SELECT pipeline_latency_us FROM requests ORDER BY pipeline_latency_us
   LIMIT 1 OFFSET (SELECT COUNT(*) * 95/100 FROM requests)) AS p95_us
FROM requests;
```

## Project layout

```text
cmd/thin/                 Binary entrypoint and HTTP server
internal/anthropic/       Anthropic Messages API request processing
internal/pipeline/        OpenAI-compatible request processing
internal/session/         In-memory session state
internal/usage/           Upstream usage/cache signal extraction
internal/tokens/          Token counting used for metrics
internal/metrics/         SQLite persistence
cmd/replay/               Offline replay and measurement utility
cmd/gentestdata/          Synthetic traffic generator
```

## License

Thin is licensed under the Functional Source License 1.1, ALv2 future license.
Each release converts to Apache 2.0 two years after publication.

See [LICENSE](LICENSE) and [LICENSE-NOTES.md](LICENSE-NOTES.md).
