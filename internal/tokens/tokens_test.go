package tokens

import (
	"strings"
	"sync"
	"testing"
)

func TestCountString_EmptyIsZero(t *testing.T) {
	c := New()
	if got := c.CountString(""); got != 0 {
		t.Fatalf("expected 0 for empty string, got %d", got)
	}
}

// The whole product question is whether deltas are honest. A longer string
// must yield more tokens or the entire savings story is suspect.
func TestCountString_MonotonicWithLength(t *testing.T) {
	c := New()
	short := c.CountString("hi")
	long := c.CountString("hi there my name is claude and I will count many tokens for you")
	if long <= short {
		t.Fatalf("longer string should yield more tokens; short=%d long=%d", short, long)
	}
}

// Same input → same count, every time. If the tokenizer ever becomes
// non-deterministic, our before/after deltas become meaningless.
func TestCountString_Deterministic(t *testing.T) {
	c := New()
	s := "the quick brown fox jumps over the lazy dog"
	first := c.CountString(s)
	for i := 0; i < 5; i++ {
		if got := c.CountString(s); got != first {
			t.Fatalf("same input yielded different counts: %d vs %d", first, got)
		}
	}
}

// Counter is shared across all proxy goroutines; tiktoken-go claims thread
// safety on the encoder. A race here would corrupt the metrics column.
func TestCountString_ConcurrentSafe(t *testing.T) {
	c := New()
	s := strings.Repeat("token ", 100)
	expected := c.CountString(s)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := c.CountString(s); got != expected {
				t.Errorf("concurrent count differed: got %d expected %d", got, expected)
			}
		}()
	}
	wg.Wait()
}
