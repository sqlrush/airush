//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gomigrate "github.com/golang-migrate/migrate/v4"
	// pgx5 数据库驱动注册 "pgx5" URL scheme。
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sqlrush/codexgo/pkg/protocol"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/console/migrations"
	"github.com/sqlrush/airush/libs/llm"
	"github.com/sqlrush/airush/libs/tenancy"
	"github.com/sqlrush/airush/testkit"
)

// 包级共享 PG（同 pgstore 集成测试的形态：一个容器 + 全量迁移；用例隔离 = 每用例一个租户）。
var (
	testPool  *pgxpool.Pool
	testStore *pgstore.Store
)

func TestMain(m *testing.M) {
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		return 1
	}
	defer func() { _ = pg.Terminate(context.Background()) }()
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrations: %v\n", err)
		return 1
	}
	mig, err := gomigrate.NewWithSourceInstance("iofs", src, "pgx5://"+strings.TrimPrefix(pg.ConnString, "postgres://"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrator: %v\n", err)
		return 1
	}
	if err := mig.Up(); err != nil && !errors.Is(err, gomigrate.ErrNoChange) {
		fmt.Fprintf(os.Stderr, "migrate up: %v\n", err)
		return 1
	}
	_, _ = mig.Close()
	pool, err := pgxpool.New(ctx, pg.ConnString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pool: %v\n", err)
		return 1
	}
	defer pool.Close()
	testPool = pool
	testStore = pgstore.New(pool, pgstore.Options{})
	return m.Run()
}

// newTenant 建租户主档，返回租户 ctx 与 id。
func newTenant(t *testing.T) (context.Context, string) {
	t.Helper()
	id := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $3)`, id, "t-"+id[:8], "t-"+id[:8]); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	return tenancy.WithTenant(context.Background(), id), id
}

// newAgent 建一个 agent 行（instruction_doc / default_model），返回 id。
func newAgent(t *testing.T, tenantID, model, doc string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agents (tenant_id, id, name, kind, instruction_doc, default_model)
		VALUES ($1, $2, $3, 'assistant', $4, NULLIF($5, ''))`, tenantID, id, "agent-"+id[:8], doc, model); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// Responses API 假供应商（进程内）：每个请求回一条 assistant 消息 + response.completed（带 usage）；
// 可配置慢回复（steer/中断/排水用例）与工具调用回合。
// ---------------------------------------------------------------------------

type fakeLLM struct {
	srv      *httptest.Server
	Requests atomic.Int64
	// Delay 让每次回复前等一会（可被请求 ctx 取消）。
	Delay time.Duration
	// Reply 生成回复文本（nil = "mock reply <n>"）。
	Reply func(n int64, req map[string]any) string
	// ToolCall 非空 → 第一次请求回一个 function_call（工具名），第二次回消息。
	ToolCall string
	// ToolCallFn 更细的控制：按请求序号决定回哪个工具调用（返回空名 = 回普通消息）。
	ToolCallFn func(n int64, req map[string]any) (name, args string)
	mu         sync.Mutex
	seen       []map[string]any
	// Hold 阻塞回复直到被关闭（模拟长 turn）。
	Hold chan struct{}
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) URL() string { return f.srv.URL + "/v1" }

func (f *fakeLLM) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.seen...)
}

func (f *fakeLLM) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/responses") {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	f.seen = append(f.seen, req)
	f.mu.Unlock()
	n := f.Requests.Add(1)
	if f.Hold != nil {
		select {
		case <-f.Hold:
		case <-r.Context().Done():
			return
		}
	}
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	var item string
	toolName, toolArgs := "", `{"q":"x"}`
	if f.ToolCallFn != nil {
		toolName, toolArgs = f.ToolCallFn(n, req)
	} else if f.ToolCall != "" && n == 1 {
		toolName = f.ToolCall
	}
	if toolName != "" {
		argsJSON, _ := json.Marshal(toolArgs)
		item = fmt.Sprintf(`{"type":"function_call","name":%q,"call_id":"call-%d","arguments":%s}`, toolName, n, argsJSON)
	} else {
		text := fmt.Sprintf("mock reply %d", n)
		if f.Reply != nil {
			text = f.Reply(n, req)
		}
		content, _ := json.Marshal(text)
		item = fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":%s}]}`, content)
	}
	_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":%s}\n\n", item)
	_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-%d\",\"usage\":{\"input_tokens\":11,\"output_tokens\":3,\"total_tokens\":14}}}\n\n", n)
	if fl != nil {
		fl.Flush()
	}
}

// meterStubs：配额门恒放行；记账进内存（断言用）。
type meterStubs struct {
	mu       sync.Mutex
	records  []llm.Usage
	tenants  []string
	checkErr error
}

func (m *meterStubs) Check(context.Context, string) error { return m.checkErr }
func (m *meterStubs) Record(_ context.Context, tenantID string, u llm.Usage, _ string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, u)
	m.tenants = append(m.tenants, tenantID)
	return nil
}

func (m *meterStubs) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// newEngine 装配一个指向假供应商的 Engine（pod 名可选）。
func newEngine(t *testing.T, llmSrv *fakeLLM, pod string, opts ...func(*Config)) (*Engine, *meterStubs) {
	t.Helper()
	stubs := &meterStubs{}
	meter := llm.NewMeter(nil, stubs, stubs, llm.WithMasterKey("test-key"))
	cfg := Config{
		Store: testStore, LLMBaseURL: llmSrv.URL(), LLMTransport: meter, PodName: pod,
		HeartbeatInterval: 200 * time.Millisecond,
		Logger:            testLogger(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e, stubs
}

// waitStatus 等线程状态变成 want。
func waitStatus(t *testing.T, ctx context.Context, threadID string, want pgstore.ThreadStatus, d time.Duration) pgstore.ThreadInfo {
	t.Helper()
	deadline := time.Now().Add(d)
	var last pgstore.ThreadInfo
	for time.Now().Before(deadline) {
		info, err := testStore.GetThreadInfo(ctx, protocol.NewThreadID(threadID))
		if err == nil && info.Status == want {
			return info
		}
		last = info
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("thread %s status = %s, want %s (within %s)", threadID, last.Status, want, d)
	return last
}

// eventTypes 读线程全部事件类型。
func eventTypes(t *testing.T, ctx context.Context, threadID string) []string {
	t.Helper()
	evs, err := testStore.ReadEvents(ctx, protocol.NewThreadID(threadID), 0, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.EventType)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func textInput(s string) TurnInput {
	return TurnInput{Items: []protocol.UserInput{{Type: protocol.UserInputKindText, Text: s}}}
}

// testLogger：AIRUSH_TEST_DEBUG=1 时把运行时日志打到 stderr，否则丢弃。
func testLogger() *slog.Logger {
	if os.Getenv("AIRUSH_TEST_DEBUG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
