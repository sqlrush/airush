//go:build integration

package pgstore

import (
	"context"
	"testing"

	"github.com/sqlrush/codexgo/pkg/agentgraph"
	"github.com/sqlrush/codexgo/pkg/agentgraph/agentgraphtest"
	"github.com/sqlrush/codexgo/pkg/threadstore"
	"github.com/sqlrush/codexgo/pkg/threadstore/contracttest"
)

// TestThreadStoreContract spec-1.8 T2：pgstore 通过 codexgo threadstore/contracttest 全套
// （create/read/append/history/lifecycle/metadata patch/list/archive/delete/optional ops/history mode）。
// PG 是分页耐久存储：删除真实现、父线程与 recency 水位、archived_at 都持久化，四个能力位全开。
func TestThreadStoreContract(t *testing.T) {
	contracttest.Run(t, contracttest.Config{
		NewStore: func(t *testing.T) threadstore.ThreadStore {
			_ = tenantFor(t)
			return testStore.Threads()
		},
		Context:                tenantCtx,
		SupportsDelete:         true,
		PersistsParentThreadID: true,
		TracksRecency:          true,
		TracksArchivedAt:       true,
	})
}

// TestGraphStoreSuite spec-1.8 D2：agentgraph.AgentGraphStore 的 PG 实现通过 codexgo
// agentgraphtest 行为全套（in-memory / SQLite / PG 三实现同一套）。
func TestGraphStoreSuite(t *testing.T) {
	agentgraphtest.RunSuiteWithContext(t,
		func(t *testing.T) (agentgraph.AgentGraphStore, func()) {
			_ = tenantFor(t)
			return testStore.Graph(), func() {}
		},
		func(t *testing.T) context.Context { return tenantCtx(t) },
	)
}
