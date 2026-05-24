// Package session tracks per-session state needed by optimization passes:
// which tools have been called, the message fingerprints of the last
// request (for prefix-stability detection), and turn count.
//
// Session ID strategy: we hash the system prompt + tool set + first user
// message. Same conversation -> same ID across requests, even if the
// client doesn't supply a session identifier. Not perfect, but works for
// the prototype and avoids needing the client to cooperate.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
)

// chatLike is the minimal shape we need for session-ID hashing. We
// re-derive it from the marshaled request to avoid an import cycle with
// the pipeline package.
type chatLike struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// Store holds all live sessions.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{sessions: map[string]*Session{}}
}

// Get returns (and lazily creates) the session for id.
func (s *Store) Get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		sess = &Session{toolsUsed: map[string]int{}}
		s.sessions[id] = sess
	}
	return sess
}

// Session holds optimization state for one logical conversation.
type Session struct {
	mu               sync.Mutex
	turns            int
	toolsUsed        map[string]int // name -> count
	lastFingerprints []uint64
}

// Turns returns the number of times this session has been observed.
func (s *Session) Turns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns
}

// HasUsedTool reports whether the session has ever invoked the named tool.
func (s *Session) HasUsedTool(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolsUsed[name] > 0
}

// ObserveToolCalls increments the turn counter and records every tool-call
// name the caller has extracted from the current request's history. Caller
// (pipeline) does the extraction so this package doesn't have to know about
// the request schema — and so we avoid a JSON re-marshal on the hot path.
func (s *Session) ObserveToolCalls(toolCallNames []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns++
	for _, name := range toolCallNames {
		s.toolsUsed[name]++
	}
}

// LastMessageFingerprints returns the message fingerprints recorded on the
// previous request, or nil if there was none.
func (s *Session) LastMessageFingerprints() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastFingerprints == nil {
		return nil
	}
	out := make([]uint64, len(s.lastFingerprints))
	copy(out, s.lastFingerprints)
	return out
}

// RecordMessageFingerprints stores fingerprints for the current request.
func (s *Session) RecordMessageFingerprints(fps []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFingerprints = fps
}

// IDFor derives a stable session ID from a request. It uses, in order of
// preference: an explicit X-Session-Id header, then a hash of the first
// user message + system prompt + model. This means a fresh conversation
// in Claude Code gets a fresh session ID without the client needing to
// know about us.
func IDFor(r *http.Request, req any) string {
	if h := r.Header.Get("X-Session-Id"); h != "" {
		return h
	}
	// Hash the first system message + first user message + model.
	b, err := json.Marshal(req)
	if err != nil {
		return "anon"
	}
	var chat chatLike
	if err := json.Unmarshal(b, &chat); err != nil {
		return "anon"
	}

	h := sha256.New()
	h.Write([]byte(chat.Model))
	h.Write([]byte{0})
	for _, m := range chat.Messages {
		if m.Role == "system" || m.Role == "user" {
			h.Write([]byte(m.Role))
			h.Write([]byte{0})
			h.Write([]byte(m.Content))
			break
		}
	}
	// Include first user message specifically — system might be shared
	// across many sessions but the first user turn is usually unique.
	for _, m := range chat.Messages {
		if m.Role == "user" {
			h.Write([]byte{1})
			h.Write([]byte(m.Content))
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
