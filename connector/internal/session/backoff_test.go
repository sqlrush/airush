package session

import (
	"testing"
	"time"
)

func TestBackoffSequenceAndCap(t *testing.T) {
	t.Parallel()
	b := newBackoff(10 * time.Second)

	// 序列递增：1s,2s,4s,8s 后封顶 10s（叠加 jitter < 1s）
	prev := time.Duration(0)
	for i := 0; i < 8; i++ {
		d := b.next()
		if d > 11*time.Second {
			t.Fatalf("step %d = %v exceeds cap+jitter", i, d)
		}
		if d < prev-time.Second {
			t.Fatalf("step %d = %v regressed from %v", i, d, prev)
		}
		prev = d
	}
}

func TestBackoffReset(t *testing.T) {
	t.Parallel()
	b := newBackoff(time.Minute)
	b.next()
	b.next()
	b.next()
	b.reset()
	if d := b.next(); d >= 3*time.Second {
		t.Fatalf("after reset = %v, want near base", d)
	}
}
