package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/libs/tenancy"
)

func TestTenantLimiter(t *testing.T) {
	l := NewTenantLimiter(2)
	first, second, third := l.TryAcquire("a"), l.TryAcquire("a"), l.TryAcquire("a")
	if !first || !second || third {
		t.Fatalf("limit 2 not enforced: %v %v %v", first, second, third)
	}
	if !l.TryAcquire("b") {
		t.Fatal("tenants must be independent")
	}
	if l.InFlight("a") != 2 || l.InFlight("b") != 1 {
		t.Fatalf("inflight = %d/%d", l.InFlight("a"), l.InFlight("b"))
	}
	l.Release("a")
	if !l.TryAcquire("a") {
		t.Fatal("release must free a slot")
	}
	l.Release("a")
	l.Release("a")
	l.Release("a") // 多还不为负
	if l.InFlight("a") != 0 {
		t.Fatalf("inflight after over-release = %d", l.InFlight("a"))
	}
	unlimited := NewTenantLimiter(0)
	for i := 0; i < 100; i++ {
		if !unlimited.TryAcquire("x") {
			t.Fatal("unlimited must always admit")
		}
	}
	unlimited.Release("x")
}

type fakeLister struct {
	pending []pgstore.PendingThread
	err     error
}

func (f fakeLister) ThreadsWithPendingInputs(context.Context) ([]pgstore.PendingThread, error) {
	return f.pending, f.err
}

type fakeDispatcher struct {
	mu       sync.Mutex
	got      []string
	tenants  []string
	draining bool
	err      error
}

func (d *fakeDispatcher) Dispatch(ctx context.Context, threadID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tid, _ := tenancy.FromContext(ctx)
	d.got = append(d.got, threadID)
	d.tenants = append(d.tenants, tid)
	return d.err
}

func (d *fakeDispatcher) Draining() bool { return d.draining }

func TestSweeperDispatchesPendingWithTenantCtx(t *testing.T) {
	lister := fakeLister{pending: []pgstore.PendingThread{{TenantID: "t1", ThreadID: "a"}, {TenantID: "t2", ThreadID: "b"}}}
	d := &fakeDispatcher{err: errors.New("boom")} // 单条失败不影响其它
	NewSweeper(lister, d, 0, nil).Sweep(context.Background())
	if len(d.got) != 2 || d.got[0] != "a" || d.tenants[0] != "t1" || d.tenants[1] != "t2" {
		t.Fatalf("dispatched = %v tenants = %v", d.got, d.tenants)
	}
	d.draining = true
	NewSweeper(lister, d, 0, nil).Sweep(context.Background())
	if len(d.got) != 2 {
		t.Fatal("draining sweeper must not dispatch")
	}
	d.draining = false
	NewSweeper(fakeLister{err: errors.New("db down")}, d, 0, nil).Sweep(context.Background())
	if len(d.got) != 2 {
		t.Fatal("list failure must not dispatch")
	}
}

func TestSweeperRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewSweeper(fakeLister{}, &fakeDispatcher{}, 0, nil).Run(ctx)
		close(done)
	}()
	cancel()
	<-done
}
