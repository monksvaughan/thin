package anthropic

import "github.com/monksvaughan/thin/internal/session"

func ShapeForCache(req *MessagesRequest, sess *session.Session) bool {
	prev := sess.LastMessageFingerprints()
	cur := fingerprintRequest(req)
	if len(prev) == 0 {
		sess.RecordMessageFingerprints(cur)
		return false
	}
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

func fingerprintRequest(req *MessagesRequest) []uint64 {
	out := make([]uint64, 0, len(req.Messages)+1)
	out = append(out, fnv64("system", string(req.System)))
	for _, m := range req.Messages {
		out = append(out, fnv64(m.Role, string(m.Content)))
	}
	return out
}

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
