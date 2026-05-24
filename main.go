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
	"time"

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
		listen   = flag.String("listen", ":8080", "address to listen on")
		upstream = flag.String("upstream", "https://api.openai.com", "upstream LLM API base URL")
		dbPath   = flag.String("db", "metrics.db", "SQLite path for metrics")
		dryRun   = flag.Bool("dry-run", false, "measure but don't actually rewrite requests")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "[proxy] ", log.LstdFlags|log.Lmicroseconds)

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		logger.Fatalf("invalid upstream URL: %v", err)
	}

	store, err := metrics.Open(*dbPath)
	if err != nil {
		logger.Fatalf("open metrics db: %v", err)
	}
	defer store.Close()

	sessions := session.NewStore()
	pipe := pipeline.New(sessions)
	counter := tokens.New()

	rp := httputil.NewSingleHostReverseProxy(upstreamURL)
	originalDirector := rp.Director
	rp.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = upstreamURL.Host
	}
	// Stream SSE responses immediately — without this, the reverse proxy
	// can buffer chunks and ruin the interactive feel of a coding agent.
	rp.FlushInterval = -1

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChatCompletions(w, r, rp, pipe, sessions, counter, store, logger, *dryRun)
	})
	// Pass-through everything else so the proxy doesn't break other endpoints
	// (models list, embeddings, etc.) while we focus on chat completions.
	mux.Handle("/", rp)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("listening on %s, upstream=%s, dry_run=%v", *listen, *upstream, *dryRun)
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatalf("server: %v", err)
	}
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

	// Extract the upstream's cache signal from the response tail and record
	// it on the session so the next turn's prune_tools can decide whether
	// to fire. If we couldn't find a signal we leave the previous value in
	// place — fail-open: when we have no signal, the next turn will prune
	// like today (the default lastCacheHit is false on a fresh session).
	sess := sessions.Get(sessionID)
	cacheHit := sess.LastCacheHit()
	if n, ok := usage.ExtractCacheHit(cap.tail); ok {
		cacheHit = n > 0
		sess.RecordCacheHit(cacheHit)
	}

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

	logger.Printf("session=%s model=%s in=%d->%d (-%.1f%%) passes=%v cache_hit=%t pipeline=%s upstream=%s",
		sessionID, req.Model,
		tokensIn, tokensInAfter, savingsPct(tokensIn, tokensInAfter),
		result.PassesApplied,
		cacheHit,
		passLatency, upstreamLatency,
	)
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
