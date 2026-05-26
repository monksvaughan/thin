// Package main implements a token-optimizing HTTP proxy for LLM APIs.
//
// Design goals for this weekend prototype:
//   - Zero model calls. All optimizations are pure string/JSON manipulation.
//   - Honest measurement: log token counts before and after each pass so we
//     can tell whether this is actually saving anything.
//   - Minimal latency: every pass should be sub-millisecond; if it isn't,
//     it doesn't ship.
//   - OpenAI Chat Completions wire format on the client side. Most agentic
//     coding tools speak this (or speak it via a shim).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/you/token-proxy/internal/anthropic"
	"github.com/you/token-proxy/internal/metrics"
	"github.com/you/token-proxy/internal/pipeline"
	"github.com/you/token-proxy/internal/session"
	"github.com/you/token-proxy/internal/tokens"
	"github.com/you/token-proxy/internal/usage"
)

// maxRequestBytes caps the body we'll read from a client. Chat-completions
// requests are routinely a few hundred KB (long histories, big tool schemas);
// anything well above that is almost certainly abuse.
const maxRequestBytes = 10 << 20 // 10 MiB

// responseTailBytes is how much of each upstream response we keep around
// for usage-block extraction. Big enough to catch the final SSE chunk on
// any sane response; small enough not to matter for memory.
const responseTailBytes = 8192

func main() {
	var (
		listen            = flag.String("listen", ":8080", "address to listen on")
		upstream          = flag.String("upstream", "https://api.openai.com", "OpenAI-compatible upstream LLM API base URL")
		anthropicUpstream = flag.String("anthropic-upstream", "https://api.anthropic.com", "Anthropic /v1/messages upstream URL")
		protectedToolsCSV = flag.String("protect-tools", "bash,read,edit,write", "comma-separated tool names never pruned (only relevant when -prune-tools is set)")
		enablePruneTools  = flag.Bool("prune-tools", false, "enable the prune_tools pass; off by default because savings are modest and pruned tools cannot be rediscovered within a session")
		dbPath            = flag.String("db", "metrics.db", "SQLite path for metrics")
		pruneLogPath      = flag.String("prune-log", "", "optional JSONL path for per-request pruned tool audit log")
		rewriteLogPath    = flag.String("rewrite-log", "", "optional JSONL path for compact/dedupe rewrite audit log")
		dryRun            = flag.Bool("dry-run", false, "measure but don't actually rewrite requests")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "[proxy] ", log.LstdFlags|log.Lmicroseconds)

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		logger.Fatalf("invalid upstream URL: %v", err)
	}
	anthropicUpstreamValue := *anthropicUpstream
	anthropicUpstreamURL, err := url.Parse(anthropicUpstreamValue)
	if err != nil {
		logger.Fatalf("invalid anthropic upstream URL: %v", err)
	}

	store, err := metrics.Open(*dbPath)
	if err != nil {
		logger.Fatalf("open metrics db: %v", err)
	}
	defer store.Close()

	protectedTools := splitCSV(*protectedToolsCSV)
	sessions := session.NewStore()
	pipe := pipeline.NewWithOptions(sessions, pipeline.Options{ProtectedTools: protectedTools, EnablePruneTools: *enablePruneTools})
	anthropicPipe := anthropic.NewWithOptions(sessions, anthropic.Options{ProtectedTools: protectedTools, EnablePruneTools: *enablePruneTools})
	counter := tokens.New()

	rp := newReverseProxy(upstreamURL)
	anthropicRP := newReverseProxy(anthropicUpstreamURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChatCompletions(w, r, rp, pipe, sessions, counter, store, logger, *dryRun, *pruneLogPath, *rewriteLogPath)
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handleAnthropicMessages(w, r, anthropicRP, anthropicPipe, sessions, counter, store, logger, *dryRun, *pruneLogPath, *rewriteLogPath)
	})
	// Pass-through everything else so the proxy doesn't break other endpoints
	// (models list, embeddings, etc.) while we focus on chat completions.
	mux.Handle("/", rp)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("listening on %s, upstream=%s, anthropic_upstream=%s, dry_run=%v, prune_tools=%v, protected_tools=%v", *listen, *upstream, anthropicUpstreamValue, *dryRun, *enablePruneTools, protectedTools)
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatalf("server: %v", err)
	}
}

func newReverseProxy(upstreamURL *url.URL) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(upstreamURL)
	originalDirector := rp.Director
	rp.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = upstreamURL.Host
	}
	// Stream SSE responses immediately — without this, the reverse proxy
	// can buffer chunks and ruin the interactive feel of a coding agent.
	rp.FlushInterval = -1
	return rp
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func handleChatCompletions(
	w http.ResponseWriter,
	r *http.Request,
	rp *httputil.ReverseProxy,
	pipe *pipeline.Pipeline,
	sessions *session.Store,
	counter *tokens.Counter,
	store *metrics.Store,
	logger *log.Logger,
	dryRun bool,
	pruneLogPath string,
	rewriteLogPath string,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestStart := time.Now()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	var req pipeline.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		// Don't break the user's request if we can't parse it; pass through.
		logger.Printf("parse error, passing through: %v", err)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		rp.ServeHTTP(w, r)
		return
	}

	sessionID := session.IDFor(r, &req)

	// Time the passes alone — this is the number the 5ms p95 target is
	// measured against. Body read, parse, and re-marshal are proxy overhead,
	// not pipeline cost.
	passStart := time.Now()
	result := pipe.Apply(sessionID, &req)
	passLatency := time.Since(passStart)

	// Marshal the optimized form. In dry-run we forward the original bytes
	// but still need outBody for the after-token-count.
	outBody, err := json.Marshal(result.Request)
	if err != nil {
		logger.Printf("marshal error, passing through original: %v", err)
		outBody = body
	}

	forwardBody := outBody
	if dryRun {
		forwardBody = body
	}
	r.Body = io.NopCloser(bytes.NewReader(forwardBody))
	r.ContentLength = int64(len(forwardBody))
	r.Header.Del("Content-Length")

	cap := &captureWriter{
		ResponseWriter: w,
		tail:           make([]byte, 0, responseTailBytes),
	}
	upstreamStart := time.Now()
	rp.ServeHTTP(cap, r)
	upstreamLatency := time.Since(upstreamStart)
	if cap.status >= 400 {
		logger.Printf("upstream non-2xx session=%s status=%d response_tail=%q", sessionID, cap.status, string(cap.tail))
	}

	// Extract the upstream's cache signal from the response tail and record
	// it on the session so the next turn's prune_tools can decide whether
	// to fire. Missing usage is treated as "no observed hit" rather than
	// carrying a stale previous hit forever.
	sess := sessions.Get(sessionID)
	cacheHit := false
	if n, ok := usage.ExtractCacheHit(cap.tail); ok {
		cacheHit = n > 0
	}
	sess.RecordCacheHit(cacheHit)

	// Count tokens off the request path: the client has already received its
	// response by the time we get here, so this latency doesn't reach them.
	tokensIn := counter.CountString(string(body))
	tokensInAfter := counter.CountString(string(outBody))

	record := metrics.Record{
		SessionID:       sessionID,
		Timestamp:       requestStart,
		Model:           req.Model,
		TokensIn:        tokensIn,
		TokensInAfter:   tokensInAfter,
		TokensOutEst:    cap.outputTokensEstimate(),
		PipelineLatency: passLatency,
		UpstreamLatency: upstreamLatency,
		PassesApplied:   result.PassesApplied,
		BytesIn:         len(body),
		BytesOut:        len(outBody),
		StatusCode:      cap.status,
		CacheHit:        cacheHit,
	}
	if err := store.Insert(record); err != nil {
		logger.Printf("metrics insert: %v", err)
	}
	if len(result.PrunedTools) > 0 {
		logger.Printf("pruned tools session=%s status=%d tools=%v", sessionID, cap.status, result.PrunedTools)
		if pruneLogPath != "" {
			if err := appendPruneLog(pruneLogPath, requestStart, sessionID, req.Model, cap.status, pipelinePrunedTools(result.PrunedTools)); err != nil {
				logger.Printf("prune log append: %v", err)
			}
		}
	}
	logRewriteAudit(logger, rewriteLogPath, requestStart, "openai", sessionID, req.Model, cap.status, pipelineDeduped(result.DedupedToolResults), pipelineCompacted(result.CompactedMessages), pipelineRepeated(result.RepeatedToolCalls))

	logger.Printf("session=%s model=%s in=%d->%d (-%.1f%%) passes=%v cache_hit=%t pipeline=%s upstream=%s",
		sessionID, req.Model,
		tokensIn, tokensInAfter, savingsPct(tokensIn, tokensInAfter),
		result.PassesApplied,
		cacheHit,
		passLatency, upstreamLatency,
	)
}

func handleAnthropicMessages(
	w http.ResponseWriter,
	r *http.Request,
	rp *httputil.ReverseProxy,
	pipe *anthropic.Pipeline,
	sessions *session.Store,
	counter *tokens.Counter,
	store *metrics.Store,
	logger *log.Logger,
	dryRun bool,
	pruneLogPath string,
	rewriteLogPath string,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestStart := time.Now()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	var req anthropic.MessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		logger.Printf("anthropic parse error, passing through: %v", err)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		rp.ServeHTTP(w, r)
		return
	}

	sessionID := session.IDFor(r, &req)

	passStart := time.Now()
	result := pipe.Apply(sessionID, &req)
	passLatency := time.Since(passStart)

	outBody, err := json.Marshal(result.Request)
	if err != nil {
		logger.Printf("anthropic marshal error, passing through original: %v", err)
		outBody = body
	}

	forwardBody := outBody
	if dryRun {
		forwardBody = body
	}
	r.Body = io.NopCloser(bytes.NewReader(forwardBody))
	r.ContentLength = int64(len(forwardBody))
	r.Header.Del("Content-Length")

	cap := &captureWriter{
		ResponseWriter: w,
		tail:           make([]byte, 0, responseTailBytes),
	}
	upstreamStart := time.Now()
	rp.ServeHTTP(cap, r)
	upstreamLatency := time.Since(upstreamStart)
	if cap.status >= 400 {
		logger.Printf("anthropic upstream non-2xx session=%s status=%d response_tail=%q", sessionID, cap.status, string(cap.tail))
	}

	sess := sessions.Get(sessionID)
	cacheHit := false
	if n, ok := usage.ExtractCacheHit(cap.tail); ok {
		cacheHit = n > 0
	}
	sess.RecordCacheHit(cacheHit)

	tokensIn := counter.CountString(string(body))
	tokensInAfter := counter.CountString(string(outBody))

	record := metrics.Record{
		SessionID:       sessionID,
		Timestamp:       requestStart,
		Model:           req.Model,
		TokensIn:        tokensIn,
		TokensInAfter:   tokensInAfter,
		TokensOutEst:    cap.outputTokensEstimate(),
		PipelineLatency: passLatency,
		UpstreamLatency: upstreamLatency,
		PassesApplied:   result.PassesApplied,
		BytesIn:         len(body),
		BytesOut:        len(outBody),
		StatusCode:      cap.status,
		CacheHit:        cacheHit,
	}
	if err := store.Insert(record); err != nil {
		logger.Printf("metrics insert: %v", err)
	}
	if len(result.PrunedTools) > 0 {
		logger.Printf("anthropic pruned tools session=%s status=%d tools=%v", sessionID, cap.status, result.PrunedTools)
		if pruneLogPath != "" {
			if err := appendPruneLog(pruneLogPath, requestStart, sessionID, req.Model, cap.status, anthropicPrunedTools(result.PrunedTools)); err != nil {
				logger.Printf("prune log append: %v", err)
			}
		}
	}
	logRewriteAudit(logger, rewriteLogPath, requestStart, "anthropic", sessionID, req.Model, cap.status, anthropicDeduped(result.DedupedToolResults), anthropicCompacted(result.CompactedMessages), anthropicRepeated(result.RepeatedToolCalls))

	logger.Printf("anthropic session=%s model=%s in=%d->%d (-%.1f%%) passes=%v cache_hit=%t pipeline=%s upstream=%s",
		sessionID, req.Model,
		tokensIn, tokensInAfter, savingsPct(tokensIn, tokensInAfter),
		result.PassesApplied,
		cacheHit,
		passLatency, upstreamLatency,
	)
}

type rewriteLogDeduped struct {
	MessageIndex int    `json:"message_index"`
	ToolID       string `json:"tool_id"`
	BeforeBytes  int    `json:"before_bytes"`
	AfterBytes   int    `json:"after_bytes"`
	Reason       string `json:"reason"`
}

type rewriteLogCompacted struct {
	MessageIndex int    `json:"message_index"`
	Role         string `json:"role"`
	BeforeBytes  int    `json:"before_bytes"`
	AfterBytes   int    `json:"after_bytes"`
	Truncated    bool   `json:"truncated"`
	Reason       string `json:"reason"`
}

type rewriteLogRepeated struct {
	Name         string `json:"name"`
	Occurrences  int    `json:"occurrences"`
	FirstMessage int    `json:"first_message_index"`
	LastMessage  int    `json:"last_message_index"`
}

func pipelineDeduped(items []pipeline.DedupedToolResult) []rewriteLogDeduped {
	out := make([]rewriteLogDeduped, len(items))
	for i, item := range items {
		out[i] = rewriteLogDeduped{MessageIndex: item.MessageIndex, ToolID: item.ToolCallID, BeforeBytes: item.BeforeBytes, AfterBytes: item.AfterBytes, Reason: item.Reason}
	}
	return out
}

func anthropicDeduped(items []anthropic.DedupedToolResult) []rewriteLogDeduped {
	out := make([]rewriteLogDeduped, len(items))
	for i, item := range items {
		out[i] = rewriteLogDeduped{MessageIndex: item.MessageIndex, ToolID: item.ToolUseID, BeforeBytes: item.BeforeBytes, AfterBytes: item.AfterBytes, Reason: item.Reason}
	}
	return out
}

func pipelineCompacted(items []pipeline.CompactedMessage) []rewriteLogCompacted {
	out := make([]rewriteLogCompacted, len(items))
	for i, item := range items {
		out[i] = rewriteLogCompacted{MessageIndex: item.MessageIndex, Role: item.Role, BeforeBytes: item.BeforeBytes, AfterBytes: item.AfterBytes, Truncated: item.Truncated, Reason: item.Reason}
	}
	return out
}

func anthropicCompacted(items []anthropic.CompactedMessage) []rewriteLogCompacted {
	out := make([]rewriteLogCompacted, len(items))
	for i, item := range items {
		out[i] = rewriteLogCompacted{MessageIndex: item.MessageIndex, Role: item.Role, BeforeBytes: item.BeforeBytes, AfterBytes: item.AfterBytes, Truncated: item.Truncated, Reason: item.Reason}
	}
	return out
}

func pipelineRepeated(items []pipeline.RepeatedToolCall) []rewriteLogRepeated {
	out := make([]rewriteLogRepeated, len(items))
	for i, item := range items {
		out[i] = rewriteLogRepeated{Name: item.Name, Occurrences: item.Occurrences, FirstMessage: item.FirstMessage, LastMessage: item.LastMessage}
	}
	return out
}

func anthropicRepeated(items []anthropic.RepeatedToolCall) []rewriteLogRepeated {
	out := make([]rewriteLogRepeated, len(items))
	for i, item := range items {
		out[i] = rewriteLogRepeated{Name: item.Name, Occurrences: item.Occurrences, FirstMessage: item.FirstMessage, LastMessage: item.LastMessage}
	}
	return out
}

func logRewriteAudit(logger *log.Logger, path string, ts time.Time, api, sessionID, model string, status int, deduped []rewriteLogDeduped, compacted []rewriteLogCompacted, repeated []rewriteLogRepeated) {
	if len(deduped) == 0 && len(compacted) == 0 && len(repeated) == 0 {
		return
	}
	var dedupeSaved, compactSaved int
	var truncated int
	for _, d := range deduped {
		dedupeSaved += d.BeforeBytes - d.AfterBytes
	}
	for _, c := range compacted {
		compactSaved += c.BeforeBytes - c.AfterBytes
		if c.Truncated {
			truncated++
		}
	}
	logger.Printf("rewrite audit api=%s session=%s status=%d deduped=%d dedupe_bytes_saved=%d compacted=%d compact_bytes_saved=%d truncated=%d repeated_tool_calls=%d", api, sessionID, status, len(deduped), dedupeSaved, len(compacted), compactSaved, truncated, len(repeated))
	if path == "" {
		return
	}
	if err := appendRewriteLog(path, ts, api, sessionID, model, status, deduped, compacted, repeated); err != nil {
		logger.Printf("rewrite log append: %v", err)
	}
}

func appendRewriteLog(path string, ts time.Time, api, sessionID, model string, status int, deduped []rewriteLogDeduped, compacted []rewriteLogCompacted, repeated []rewriteLogRepeated) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	entry := struct {
		Timestamp string                `json:"timestamp"`
		API       string                `json:"api"`
		SessionID string                `json:"session_id"`
		Model     string                `json:"model"`
		Status    int                   `json:"status"`
		Deduped   []rewriteLogDeduped   `json:"deduped_tool_results,omitempty"`
		Compacted []rewriteLogCompacted `json:"compacted_messages,omitempty"`
		Repeated  []rewriteLogRepeated  `json:"repeated_tool_calls,omitempty"`
	}{
		Timestamp: ts.Format(time.RFC3339Nano),
		API:       api,
		SessionID: sessionID,
		Model:     model,
		Status:    status,
		Deduped:   deduped,
		Compacted: compacted,
		Repeated:  repeated,
	}
	return json.NewEncoder(f).Encode(entry)
}

type pruneLogTool struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func pipelinePrunedTools(tools []pipeline.PrunedTool) []pruneLogTool {
	out := make([]pruneLogTool, len(tools))
	for i, t := range tools {
		out[i] = pruneLogTool{Name: t.Name, Reason: t.Reason}
	}
	return out
}

func anthropicPrunedTools(tools []anthropic.PrunedTool) []pruneLogTool {
	out := make([]pruneLogTool, len(tools))
	for i, t := range tools {
		out[i] = pruneLogTool{Name: t.Name, Reason: t.Reason}
	}
	return out
}

func appendPruneLog(path string, ts time.Time, sessionID, model string, status int, tools []pruneLogTool) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	entry := struct {
		Timestamp string         `json:"timestamp"`
		SessionID string         `json:"session_id"`
		Model     string         `json:"model"`
		Status    int            `json:"status"`
		Tools     []pruneLogTool `json:"tools"`
	}{
		Timestamp: ts.Format(time.RFC3339Nano),
		SessionID: sessionID,
		Model:     model,
		Status:    status,
		Tools:     tools,
	}
	return json.NewEncoder(f).Encode(entry)
}

func savingsPct(before, after int) float64 {
	if before == 0 {
		return 0
	}
	return float64(before-after) / float64(before) * 100
}

// captureWriter wraps http.ResponseWriter so we can read the status code,
// approximate output token count, and the tail of the body. The tail lets
// us extract the upstream's usage block (in particular cached_tokens) after
// rp.ServeHTTP returns. Streaming bodies pass through to the client
// unchanged; we just tee for measurement.
type captureWriter struct {
	http.ResponseWriter
	status    int
	bodyBytes int
	tail      []byte // sliding window of the last responseTailBytes bytes
}

func (c *captureWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.bodyBytes += len(b)
	// Maintain a sliding tail of the last responseTailBytes bytes. We don't
	// care about the head — usage blocks live near the end (final SSE chunk
	// for streaming; whole body for non-streaming, which is typically small).
	if len(b) >= responseTailBytes {
		c.tail = append(c.tail[:0], b[len(b)-responseTailBytes:]...)
	} else {
		c.tail = append(c.tail, b...)
		if len(c.tail) > responseTailBytes {
			excess := len(c.tail) - responseTailBytes
			copy(c.tail, c.tail[excess:])
			c.tail = c.tail[:responseTailBytes]
		}
	}
	return c.ResponseWriter.Write(b)
}

// outputTokensEstimate gives a rough output-token estimate from byte count.
// For an honest production system we'd parse the final SSE chunk's usage
// block (OpenAI sends it when stream_options.include_usage=true). For the
// prototype, ~4 bytes/token is good enough to spot the order of magnitude.
func (c *captureWriter) outputTokensEstimate() int {
	return c.bodyBytes / 4
}

// Implement http.Flusher so streaming responses still stream.
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
