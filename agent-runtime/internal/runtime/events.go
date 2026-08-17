package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"

	"github.com/sqlrush/airush/agent-runtime/internal/tenantctx"
	"github.com/sqlrush/airush/libs/tenancy"
)

// notifier 是本 pod 内"某线程事件流有新写入"的广播（降低本地 tail 延迟；跨 pod 靠轮询）。
type notifier struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func newNotifier() *notifier { return &notifier{subs: map[string]map[chan struct{}]struct{}{}} }

// Subscribe 返回订阅通道与退订函数；通道容量 1，通知合并。
func (n *notifier) Subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.subs[key] == nil {
		n.subs[key] = map[chan struct{}]struct{}{}
	}
	n.subs[key][ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.subs[key], ch)
		if len(n.subs[key]) == 0 {
			delete(n.subs, key)
		}
		n.mu.Unlock()
	}
}

// Notify 唤醒 key 的全部订阅者（非阻塞）。
func (n *notifier) Notify(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// eventPollInterval 是跨 pod 事件 tail 的轮询周期（本地写入靠 notifier 立即唤醒）。
const eventPollInterval = 400 * time.Millisecond

// eventBatch 是一次 tail 读取的最大条数。
const eventBatch = 200

// Events 订阅线程事件：先回放 seq ≥ fromSeq 的持久事件，再持续 tail（PG 是 SSOT，turn 可能
// 在别的 pod 上跑）。通道在 ctx 结束时关闭；线程不存在 → 立即报错。
func (e *Engine) Events(ctx context.Context, threadID string, fromSeq int64) (<-chan Event, error) {
	tid := protocol.NewThreadID(threadID)
	if _, err := e.store.GetThreadInfo(ctx, tid); err != nil {
		return nil, err
	}
	out := make(chan Event, 64)
	go e.tail(ctx, tid, fromSeq, out)
	return out, nil
}

func (e *Engine) tail(ctx context.Context, tid protocol.ThreadID, fromSeq int64, out chan<- Event) {
	defer close(out)
	wake, unsub := e.notifier.Subscribe(tid.String())
	defer unsub()
	next := fromSeq
	if next < 1 {
		next = 1
	}
	ticker := time.NewTicker(eventPollInterval)
	defer ticker.Stop()
	for {
		n, err := e.forward(ctx, tid, next, out)
		if err != nil {
			return
		}
		next += n
		if n == eventBatch {
			continue // 可能还有，立刻再读
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}

// forward 读 [from, from+batch) 并推送；返回推送条数。
func (e *Engine) forward(ctx context.Context, tid protocol.ThreadID, from int64, out chan<- Event) (int64, error) {
	rows, err := e.store.ReadEvents(ctx, tid, from, eventBatch)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, r := range rows {
		ev := Event{Seq: r.Seq, Type: r.EventType, Payload: r.Payload}
		if r.TurnID != nil {
			ev.TurnID = *r.TurnID
		}
		if r.PayloadRef != nil {
			ev.PayloadRef = *r.PayloadRef
		}
		select {
		case out <- ev:
			n++
		case <-ctx.Done():
			return n, ctx.Err()
		}
	}
	return n, nil
}

// tenantIDFrom 是 tenancy.FromContext 的本包别名（tenantctx 的会话 ctx 同样满足）。
func tenantIDFrom(ctx context.Context) (string, bool) {
	if info, ok := tenantctx.FromContext(ctx); ok {
		return info.TenantID, true
	}
	return tenancy.FromContext(ctx)
}
