// Command replay reads JSONL of recorded chat-completion requests from
// stdin and runs them through the optimization pipeline without actually
// calling any upstream. It prints a per-request and aggregate report.
//
// This is how you validate the prototype before deploying: capture a few
// days of your own Claude Code traffic with `mitmproxy`, save the request
// bodies as JSONL, and pipe them in here. You'll get a savings estimate
// without paying for the real calls.
//
// Usage:
//   cat requests.jsonl | go run ./cmd/replay
//
// JSONL format: one request body per line. The proxy logs raw bodies if
// you set -log-level=debug, or you can capture them with any HTTP recorder.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/you/token-proxy/internal/pipeline"
	"github.com/you/token-proxy/internal/session"
)

type sessionAgg struct {
	turns        int
	tokensBefore int
	tokensAfter  int
	passCounts   map[string]int
	maxLatency   time.Duration
	sumLatency   time.Duration
}

func main() {
	sessions := session.NewStore()
	pipe := pipeline.New(sessions, false)

	bySession := map[string]*sessionAgg{}
	totalReq := 0
	totalBefore := 0
	totalAfter := 0
	var totalLatency time.Duration

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // allow big requests
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req pipeline.ChatRequest
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "line %d: parse error: %v\n", lineNo, err)
			continue
		}

		// In replay we synthesize a session ID from request shape, same as
		// the live proxy would.
		sid := synthSessionID(&req)
		start := time.Now()
		result := pipe.Apply(sid, &req)
		dur := time.Since(start)

		agg, ok := bySession[sid]
		if !ok {
			agg = &sessionAgg{passCounts: map[string]int{}}
			bySession[sid] = agg
		}
		agg.turns++
		agg.tokensBefore += result.TokensInOriginal
		agg.tokensAfter += result.TokensInAfter
		agg.sumLatency += dur
		if dur > agg.maxLatency {
			agg.maxLatency = dur
		}
		for _, p := range result.PassesApplied {
			agg.passCounts[p]++
		}

		totalReq++
		totalBefore += result.TokensInOriginal
		totalAfter += result.TokensInAfter
		totalLatency += dur
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
	}

	// Print per-session table.
	fmt.Println("=== per session ===")
	fmt.Printf("%-18s %6s %12s %12s %8s %10s\n",
		"session", "turns", "before", "after", "saved%", "avg_us")
	for sid, agg := range bySession {
		savedPct := 0.0
		if agg.tokensBefore > 0 {
			savedPct = 100 * float64(agg.tokensBefore-agg.tokensAfter) / float64(agg.tokensBefore)
		}
		avgUs := int64(0)
		if agg.turns > 0 {
			avgUs = agg.sumLatency.Microseconds() / int64(agg.turns)
		}
		fmt.Printf("%-18s %6d %12d %12d %7.1f%% %10d\n",
			truncate(sid, 18), agg.turns, agg.tokensBefore, agg.tokensAfter, savedPct, avgUs)
	}

	// Aggregate.
	fmt.Println()
	fmt.Println("=== aggregate ===")
	totalPct := 0.0
	if totalBefore > 0 {
		totalPct = 100 * float64(totalBefore-totalAfter) / float64(totalBefore)
	}
	avgUs := int64(0)
	if totalReq > 0 {
		avgUs = totalLatency.Microseconds() / int64(totalReq)
	}
	fmt.Printf("requests:           %d\n", totalReq)
	fmt.Printf("tokens before:      %d\n", totalBefore)
	fmt.Printf("tokens after:       %d\n", totalAfter)
	fmt.Printf("savings:            %.1f%%\n", totalPct)
	fmt.Printf("avg pipeline us:    %d\n", avgUs)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func synthSessionID(req *pipeline.ChatRequest) string {
	// Re-use the live proxy's hash via a stub http request. Simpler: hash
	// model + first system + first user content directly here. We accept a
	// small duplication of logic to keep replay independent of net/http.
	for _, m := range req.Messages {
		if m.Role == "user" {
			s := req.Model + ":" + string(m.Content)
			if len(s) > 32 {
				s = s[:32]
			}
			return s
		}
	}
	return req.Model
}
