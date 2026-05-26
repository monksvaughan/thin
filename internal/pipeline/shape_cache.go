package pipeline

import "github.com/monksvaughan/thin/internal/session"

// ShapeForCache is a measurement-and-annotation pass.
//
// For the prototype we keep this conservative: we identify the "stable
// prefix" of the request — the part that doesn't change turn-to-turn —
// and record its length in the session for later analysis. We deliberately
// do NOT mutate the request to add Anthropic-specific cache_control
// breakpoints in v1, because that field is rejected by OpenAI-compatible
// upstreams and we want one binary that works against both.
//
// In v2, an Anthropic-aware mode would insert cache_control on the last
// message of the stable prefix to maximize prefix-cache hit rate.
//
// Returns true if it identified a non-trivial stable prefix worth reporting.
func ShapeForCache(req *ChatRequest, sess *session.Session) bool {
	// The stable prefix is whatever was identical to last turn's prefix.
	// Compute the index of the first message that differs from the last
	// observed request's messages.
	prev := sess.LastMessageFingerprints()
	if len(prev) == 0 {
		// First turn — record fingerprints, nothing to shape against yet.
		sess.RecordMessageFingerprints(fingerprintMessages(req.Messages))
		return false
	}

	cur := fingerprintMessages(req.Messages)
	stable := 0
	for i := 0; i < len(prev) && i < len(cur); i++ {
		if prev[i] != cur[i] {
			break
		}
		stable++
	}
	sess.RecordMessageFingerprints(cur)

	return stable > 0
}

func fingerprintMessages(msgs []Message) []uint64 {
	out := make([]uint64, len(msgs))
	for i, m := range msgs {
		out[i] = fnv64(m.Role, string(m.Content), m.Name, m.ToolCallID)
	}
	return out
}

// Tiny fnv1a over a sequence of strings. We don't need cryptographic
// strength here — false collisions would just make us slightly less
// aggressive about claiming a stable prefix.
func fnv64(parts ...string) uint64 {
	const offset = 1469598103934665603
	const prime = 1099511628211
	h := uint64(offset)
	for _, p := range parts {
		for i := 0; i < len(p); i++ {
			h ^= uint64(p[i])
			h *= prime
		}
		h ^= 0
		h *= prime
	}
	return h
}
