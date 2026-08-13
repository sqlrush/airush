package metrics

import (
	"context"
	"sync"
)

// Sink 是指标批次的落点契约（spec-1.3 §2.4）。spec-1.5 以 TimescaleDB 实现；
// 本 spec 提供 BufferSink（内存环形）验证链路，不引入存储依赖。
type Sink interface {
	Publish(ctx context.Context, batch Batch) error
}

// SnapshotSink 是快照的落点契约（spec-1.4 §2.4）。与 Sink 并列而非合并：两者载荷
// 与落库形态不同（指标是时序单值、快照是行结构），接口隔离让 spec-1.5 可以分别
// 实现而消费方只依赖自己用得到的那个。BufferSink 同时实现两者。
type SnapshotSink interface {
	PublishSnapshot(ctx context.Context, snapshot Snapshot) error
}

// BufferSink 是内存环形 Sink（测试与链路验证用）：指标批与快照各留最近 capacity 份，
// 各自计数总收讫。
type BufferSink struct {
	mu            sync.Mutex
	capacity      int
	batches       []Batch
	total         int
	snapshots     []Snapshot
	snapshotTotal int
}

// 编译期断言：BufferSink 同时是指标与快照的落点。
var (
	_ Sink         = (*BufferSink)(nil)
	_ SnapshotSink = (*BufferSink)(nil)
)

// NewBufferSink 构造容量为 capacity 的环形 Sink。
func NewBufferSink(capacity int) *BufferSink {
	if capacity <= 0 {
		capacity = 128
	}
	return &BufferSink{capacity: capacity}
}

// Publish 记录一批（环形淘汰最旧）。
func (b *BufferSink) Publish(_ context.Context, batch Batch) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total++
	b.batches = append(b.batches, batch)
	if len(b.batches) > b.capacity {
		b.batches = b.batches[len(b.batches)-b.capacity:]
	}
	return nil
}

// Total 返回累计收讫批数。
func (b *BufferSink) Total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Latest 返回最近一批（无则 ok=false）。
func (b *BufferSink) Latest() (Batch, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.batches) == 0 {
		return Batch{}, false
	}
	return b.batches[len(b.batches)-1], true
}

// PublishSnapshot 记录一份快照（环形淘汰最旧）。
func (b *BufferSink) PublishSnapshot(_ context.Context, snapshot Snapshot) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshotTotal++
	b.snapshots = append(b.snapshots, snapshot)
	if len(b.snapshots) > b.capacity {
		b.snapshots = b.snapshots[len(b.snapshots)-b.capacity:]
	}
	return nil
}

// SnapshotTotal 返回累计收讫快照数。
func (b *BufferSink) SnapshotTotal() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotTotal
}

// LatestSnapshotOf 返回该 kind 最近一份快照（无则 ok=false）。
func (b *BufferSink) LatestSnapshotOf(kind string) (Snapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.snapshots) - 1; i >= 0; i-- {
		if b.snapshots[i].Kind == kind {
			return b.snapshots[i], true
		}
	}
	return Snapshot{}, false
}
