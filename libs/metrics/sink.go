package metrics

import (
	"context"
	"sync"
)

// Sink 是采集批次的落点契约（spec-1.3 §2.4）。spec-1.5 以 TimescaleDB 实现；
// 本 spec 提供 BufferSink（内存环形）验证链路，不引入存储依赖。
type Sink interface {
	Publish(ctx context.Context, batch Batch) error
}

// BufferSink 是内存环形 Sink（测试与链路验证用）：保留最近 capacity 批，计数总收讫。
type BufferSink struct {
	mu       sync.Mutex
	capacity int
	batches  []Batch
	total    int
}

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
