# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project context

Weekend prototype of an OpenAI-compatible HTTP proxy that rewrites `/v1/chat/completions` request bodies to cut input tokens for agentic coding clients. **Hard constraint: no model calls anywhere in the request path.** Every optimization is pure string/JSON manipulation. If a change would introduce an upstream call, embedding lookup, or LLM-based summarization, it does not belong in this codebase — that is explicitly v2 territory (see README.md "What it deliberately does NOT do").

Pass/fail bar for the prototype: ≥25% median token savings on real sessions with ≥5 turns, p95 pipeline latency <5ms, zero broken requests vs. dry-run.

## Commands

```bash
# Run the proxy
go run . -upstream https://api.openai.com -listen :8080
go run . -upstream https://api.openai.com -dry-run        # measure only, forward original

# Tests
go test ./...
go test ./internal/pipeline -run TestPruneTools_dropsUnusedAfterEnoughTurns -v

# Generate synthetic traffic and run it through the pipeline offline (no upstream call)
go run ./cmd/gentestdata > test.jsonl
cat test.jsonl | go run ./cmd/replay

# Inspect metrics — every request is logged to metrics.db (SQLite). See README.md for queries.
```

## Architecture

Request lifecycle (`main.go:handleChatCompletions`):

1. Read body → unmarshal into `pipeline.ChatRequest`. Parse failures fall through to the reverse proxy unchanged.
2. Derive session ID (`session.IDFor`): prefers `X-Session-Id` header, else hashes model + first system + first user message. Same conversation gets the same ID without client cooperation.
3. `pipeline.Apply` runs the passes in order, mutating the request in place. In `-dry-run`, the original is restored before forwarding but metrics still reflect what *would* have changed.
4. Re-serialize, forward via `httputil.ReverseProxy` (FlushInterval=-1 so SSE isn't buffered), capture status + byte count via `captureWriter`.
5. Insert one row into `metrics.db`.

Pass pipeline (`internal/pipeline/pipeline.go`, order is load-bearing):

1. `session.ObserveToolCalls` — bookkeeping; records which tool names appeared in this turn's history. Other passes depend on this.
2. `PruneTools` — drops tool schemas the session has never called. Gated by `minObservedTurnsForPrune = 3`; never prunes on early turns because a tool used for the first time would be silently lost.
3. `DedupeToolResults` — when the same `(function name, arguments)` pair appears multiple times in history, replaces older results with a one-line stub. Walks newest-to-oldest so the latest occurrence is kept intact.
4. `CompactHistory` — whitespace cleanup on all old messages; head+tail truncation only on `role=tool` messages over 4KB. Leaves the last `recencyWindow = 6` messages untouched.
5. `ShapeForCache` — **measurement-only in v1, does not mutate**. Identifies the stable prefix across turns via per-message FNV fingerprints stored on the session. v2 would insert Anthropic `cache_control` here, but that field is rejected by OpenAI upstreams and v1 must run against both.

### Things that are easy to break

- **`ChatRequest.Extra` round-trips unknown top-level fields.** Custom `UnmarshalJSON`/`MarshalJSON` in `internal/pipeline/types.go` siphon anything not in `knownTopLevelFields` into `Extra` and write it back out. If you add a new top-level field to the struct, also add it to `knownTopLevelFields` or it'll get double-serialized.
- **`Message.Content` is `json.RawMessage`.** It can be a string OR a multimodal/parts array OR Anthropic-style blocks with `cache_control`. Passes that need string content (e.g. `compact_history`) must `json.Unmarshal` into a string and skip on error — do not assume string content.
- **`session.ObserveToolCalls` takes `any` and re-marshals.** This is deliberate to dodge an import cycle with `pipeline`. Don't tighten the type — it'll create the cycle.
- **Session state is in-memory only.** Restart = forget. The `prune_tools` gate at 3 turns means a restart causes a few unpruned-but-correct turns, not breakage.
- **Token counting uses `cl100k_base` for everything.** Deltas are trustworthy; absolute counts for Anthropic models are off by a constant factor. Don't lean on absolute counts in tests or analyses; compare before/after on the same tokenizer.
- **Latency budget is tight.** p95 <5ms across the whole pipeline. New passes need to stay sub-millisecond or they don't ship; this is product policy, not a soft guideline.
- **Fall-through on parse failure is intentional.** If we can't confidently parse a request, we forward it untouched rather than risk breaking the user's agent loop. Preserve this behavior in any new pass.

### Module layout

- `main.go` — HTTP server, request lifecycle, reverse proxy, metrics capture.
- `internal/pipeline/` — pass implementations + `ChatRequest`/`Message`/`Tool` schema. All passes are pure functions over `*ChatRequest` (plus session state for `prune_tools`/`shape_cache`).
- `internal/session/` — per-session tool-usage counters, message fingerprints, ID derivation. Thread-safe via `sync.Mutex`.
- `internal/tokens/` — `tiktoken-go` wrapper with a byte-count fallback so a tokenizer load failure can't take the proxy down.
- `internal/metrics/` — SQLite (`modernc.org/sqlite`, pure Go) persistence. WAL mode, single writer.
- `cmd/replay/` — runs JSONL of recorded requests through the pipeline offline, prints per-session and aggregate savings. Use this to validate changes against real traffic without making upstream calls.
- `cmd/gentestdata/` — synthesizes a 10-turn coding-agent conversation with the waste patterns the passes target (30 tools / 3 used, repeated file reads, long stack traces). Pipe into `cmd/replay` for a smoke test.
