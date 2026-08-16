package tenancy

import (
	"context"
	"testing"
)

func TestWithTenantRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ctx    context.Context
		wantID string
		wantOK bool
	}{
		{"注入后取回", WithTenant(context.Background(), "t-1"), "t-1", true},
		{"未注入", context.Background(), "", false},
		{"注入空串视为缺失", WithTenant(context.Background(), ""), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, ok := FromContext(tt.ctx)
			if id != tt.wantID || ok != tt.wantOK {
				t.Fatalf("FromContext = (%q, %v), want (%q, %v)", id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestWithTenantDoesNotMutateParent(t *testing.T) {
	t.Parallel()
	parent := context.Background()
	_ = WithTenant(parent, "t-1")
	if _, ok := FromContext(parent); ok {
		t.Fatal("parent ctx mutated by WithTenant")
	}
}
