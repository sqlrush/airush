package metrics

import (
	"context"
	"testing"
)

// TestBufferSinkCountsAndLatest spec-1.3 T8：收讫计数与产出批数一致 + Latest 取最近。
func TestBufferSinkCountsAndLatest(t *testing.T) {
	t.Parallel()
	s := NewBufferSink(8)
	if _, ok := s.Latest(); ok {
		t.Fatal("empty sink Latest should be ok=false")
	}
	for i := 0; i < 5; i++ {
		if err := s.Publish(context.Background(), Batch{DatasourceID: "ds", Metrics: []Metric{{Name: "m", Value: float64(i)}}}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if s.Total() != 5 {
		t.Fatalf("total = %d, want 5", s.Total())
	}
	last, ok := s.Latest()
	if !ok || last.Metrics[0].Value != 4 {
		t.Fatalf("latest = %+v ok=%v", last, ok)
	}
}

// TestBufferSinkRingEviction spec-1.3 T8：环形满 → 淘汰最旧，计数仍累计。
func TestBufferSinkRingEviction(t *testing.T) {
	t.Parallel()
	s := NewBufferSink(3)
	for i := 0; i < 6; i++ {
		_ = s.Publish(context.Background(), Batch{DatasourceID: "ds", Metrics: []Metric{{Name: "m", Value: float64(i)}}})
	}
	if s.Total() != 6 {
		t.Fatalf("total = %d, want 6 (cumulative)", s.Total())
	}
	last, _ := s.Latest()
	if last.Metrics[0].Value != 5 {
		t.Fatalf("latest after eviction = %v, want 5", last.Metrics[0].Value)
	}
}

// TestNewBufferSinkDefaultCapacity 非正容量回落默认。
func TestNewBufferSinkDefaultCapacity(t *testing.T) {
	t.Parallel()
	s := NewBufferSink(0)
	if s.capacity != 128 {
		t.Fatalf("default capacity = %d, want 128", s.capacity)
	}
}
