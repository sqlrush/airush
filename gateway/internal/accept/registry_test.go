package accept

import "testing"

func TestRegistryKicksOldSession(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	old := &session{connectorID: "c1", drain: make(chan string, 1)}
	r.add(old)

	fresh := &session{connectorID: "c1", drain: make(chan string, 1)}
	r.add(fresh)

	select {
	case reason := <-old.drain:
		if reason == "" {
			t.Fatal("empty drain reason")
		}
	default:
		t.Fatal("old session not signalled on supersede")
	}
}

func TestRegistryRemoveOnlyCurrent(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	old := &session{connectorID: "c1", drain: make(chan string, 1)}
	fresh := &session{connectorID: "c1", drain: make(chan string, 1)}
	r.add(old)
	r.add(fresh)

	// 旧会话 remove 不应删掉当前（fresh）
	r.remove(old)
	r.mu.Lock()
	_, present := r.sessions["c1"]
	r.mu.Unlock()
	if !present {
		t.Fatal("removing superseded session deleted the current one")
	}

	r.remove(fresh)
	r.mu.Lock()
	_, present = r.sessions["c1"]
	r.mu.Unlock()
	if present {
		t.Fatal("current session not removed")
	}
}

func TestDrainAll(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	a := &session{connectorID: "a", drain: make(chan string, 1)}
	b := &session{connectorID: "b", drain: make(chan string, 1)}
	r.add(a)
	r.add(b)
	r.drainAll("bye")
	for _, s := range []*session{a, b} {
		select {
		case <-s.drain:
		default:
			t.Fatalf("session %s not drained", s.connectorID)
		}
	}
}
