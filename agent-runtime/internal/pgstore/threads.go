package pgstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/libs/apierror"
)

// ThreadStore 是 codexgo threadstore.ThreadStore 的 PG 实现（spec-1.8 D2）。
// 事件在 AppendItems 时即落库（同事务推进 last_seq），Persist/Flush/Shutdown/Discard 为无操作：
// PG 就是持久层，没有"物化"阶段；resume 只校验线程存在。
// sections / occurrence 搜索显式 Unsupported（UnimplementedStore 默认）。
type ThreadStore struct {
	threadstore.UnimplementedStore
	s *Store
}

var _ threadstore.ThreadStore = (*ThreadStore)(nil)

// Threads 返回 ThreadStore 视图。
func (s *Store) Threads() *ThreadStore { return &ThreadStore{s: s} }

// DefaultHistoryMode 报 paginated：PG 存储的耐久契约是分页的（事件按 seq）。
func (t *ThreadStore) DefaultHistoryMode() protocol.ThreadHistoryMode {
	return protocol.ThreadHistoryModePaginated
}

// SupportsPaginatedHistoryLists 为真：ListItems / ListTurns 有实现。
func (t *ThreadStore) SupportsPaginatedHistoryLists() bool { return true }

// CreateThread 建线程行（idle，model 用缺省，agent_id 空；runtime 随后 SetThreadAttributes）。
// 重复 id 报 InvalidRequest。
func (t *ThreadStore) CreateThread(ctx context.Context, params threadstore.CreateThreadParams) error {
	if params.ThreadID == (protocol.ThreadID{}) {
		return threadstore.NewInvalidRequestError("thread id is required")
	}
	meta := metaFromCreate(params, t.s.opts.DefaultModel)
	rawMeta, err := marshalMeta(meta)
	if err != nil {
		return threadstore.NewInternalError(err, "encode thread metadata")
	}
	var parent *string
	if params.ParentThreadID != nil {
		s := params.ParentThreadID.String()
		parent = &s
	}
	return t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO agent_threads (tenant_id, id, parent_thread_id, status, model, metadata)
			VALUES ($1, $2, $3, 'idle', $4, $5) ON CONFLICT (tenant_id, id) DO NOTHING`,
			tenantIDFrom(ctx), params.ThreadID.String(), parent, t.s.opts.DefaultModel, rawMeta)
		if err != nil {
			return storeErr(err, "create thread %s", params.ThreadID)
		}
		if tag.RowsAffected() == 0 {
			return threadstore.NewInvalidRequestError("thread %s already exists", params.ThreadID)
		}
		return nil
	})
}

// ResumeThread 校验线程存在（未删除）；PG 无 live writer 需要重开。
func (t *ThreadStore) ResumeThread(ctx context.Context, params threadstore.ResumeThreadParams) error {
	return t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := t.s.currentSeq(ctx, tx, params.ThreadID)
		return err
	})
}

// AppendItems 追加事件（同事务推进 last_seq）。
func (t *ThreadStore) AppendItems(ctx context.Context, params threadstore.AppendThreadItemsParams) error {
	if len(params.Items) == 0 {
		return nil
	}
	return t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := t.s.appendEvents(ctx, tx, params.ThreadID, params.Items)
		return err
	})
}

// PersistThread / FlushThread / ShutdownThread / DiscardThread：事件已耐久，仅校验线程存在。
func (t *ThreadStore) PersistThread(ctx context.Context, threadID protocol.ThreadID) error {
	return t.ensureExists(ctx, threadID)
}

// FlushThread 见 PersistThread。
func (t *ThreadStore) FlushThread(ctx context.Context, threadID protocol.ThreadID) error {
	return t.ensureExists(ctx, threadID)
}

// ShutdownThread 见 PersistThread。
func (t *ThreadStore) ShutdownThread(ctx context.Context, threadID protocol.ThreadID) error {
	return t.ensureExists(ctx, threadID)
}

// DiscardThread 见 PersistThread（已耐久的数据保留）。
func (t *ThreadStore) DiscardThread(ctx context.Context, threadID protocol.ThreadID) error {
	return t.ensureExists(ctx, threadID)
}

func (t *ThreadStore) ensureExists(ctx context.Context, threadID protocol.ThreadID) error {
	return t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := t.s.currentSeq(ctx, tx, threadID)
		return err
	})
}

// LoadHistory 读取全部事件并还原为 rollout 项（重放顺序）。
func (t *ThreadStore) LoadHistory(ctx context.Context, params threadstore.LoadThreadHistoryParams) (threadstore.StoredThreadHistory, error) {
	var out threadstore.StoredThreadHistory
	err := t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := t.readRow(ctx, tx, params.ThreadID, params.IncludeArchived); err != nil {
			return err
		}
		events, err := t.s.loadEvents(ctx, tx, params.ThreadID, 0, 0)
		if err != nil {
			return err
		}
		out = threadstore.StoredThreadHistory{ThreadID: params.ThreadID, Items: decodeRolloutItems(events)}
		return nil
	})
	return out, err
}

// LoadLatestModelContext 只读最近一次压缩（compacted_item）起的后缀（0.147 targeted read；
// 无压缩则全量）——resume 不再重放整段历史（spec-1.8 T12）。
func (t *ThreadStore) LoadLatestModelContext(ctx context.Context, params threadstore.LoadThreadHistoryParams) (threadstore.StoredModelContext, error) {
	var out threadstore.StoredModelContext
	err := t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := t.readRow(ctx, tx, params.ThreadID, params.IncludeArchived); err != nil {
			return err
		}
		var from int64
		err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM agent_rollout_events
			WHERE thread_id = $1 AND event_type = $2`, params.ThreadID.String(), EventTypeCompacted).Scan(&from)
		if err != nil {
			return storeErr(err, "locate latest compaction for %s", params.ThreadID)
		}
		events, err := t.s.loadEvents(ctx, tx, params.ThreadID, from, 0)
		if err != nil {
			return err
		}
		out = threadstore.StoredModelContext{ThreadID: params.ThreadID, Items: decodeRolloutItems(events)}
		return nil
	})
	return out, err
}

// PrepareFork 冻结源线程的历史位置（last_seq）与模型上下文（最近压缩起的后缀）。
// PG 无需保留（无文件可被删）：Release 为空操作。
func (t *ThreadStore) PrepareFork(ctx context.Context, params threadstore.PrepareForkParams) (threadstore.PreparedFork, error) {
	if params.Boundary.Kind != "" && params.Boundary.Kind != threadstore.ForkBoundaryLatest {
		return threadstore.PreparedFork{}, threadstore.NewUnsupportedError("prepare_fork:" + string(params.Boundary.Kind))
	}
	var out threadstore.PreparedFork
	err := t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := t.readRow(ctx, tx, params.ThreadID, true)
		if err != nil {
			return err
		}
		mc, err := t.latestModelContextTx(ctx, tx, params.ThreadID)
		if err != nil {
			return err
		}
		out = threadstore.PreparedFork{
			SourceThreadID: params.ThreadID,
			HistoryBase:    &protocol.HistoryPosition{ThreadID: params.ThreadID, EndOrdinalExclusive: seqToOrdinal(row.LastSeq) + 1},
			ModelContext:   mc,
			Release:        func() {},
		}
		return nil
	})
	return out, err
}

func (t *ThreadStore) latestModelContextTx(ctx context.Context, tx pgx.Tx, threadID protocol.ThreadID) ([]rollout.RolloutItem, error) {
	var from int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM agent_rollout_events
		WHERE thread_id = $1 AND event_type = $2`, threadID.String(), EventTypeCompacted).Scan(&from); err != nil {
		return nil, storeErr(err, "locate latest compaction for %s", threadID)
	}
	events, err := t.s.loadEvents(ctx, tx, threadID, from, 0)
	if err != nil {
		return nil, err
	}
	return decodeRolloutItems(events), nil
}

// ReadThread 读线程摘要（可带历史）；deleted 行视为不存在；archived 需 IncludeArchived。
func (t *ThreadStore) ReadThread(ctx context.Context, params threadstore.ReadThreadParams) (threadstore.StoredThread, error) {
	var out threadstore.StoredThread
	err := t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := t.readRow(ctx, tx, params.ThreadID, params.IncludeArchived)
		if err != nil {
			return err
		}
		out = row.storedThread()
		if params.IncludeHistory {
			events, err := t.s.loadEvents(ctx, tx, params.ThreadID, 0, 0)
			if err != nil {
				return err
			}
			out.History = &threadstore.StoredThreadHistory{ThreadID: params.ThreadID, Items: decodeRolloutItems(events)}
		}
		return nil
	})
	return out, err
}

// ReadThreadByRolloutPath 显式 Unsupported：PG 存储没有 rollout 路径。
func (t *ThreadStore) ReadThreadByRolloutPath(context.Context, threadstore.ReadThreadByRolloutPathParams) (threadstore.StoredThread, error) {
	return threadstore.StoredThread{}, threadstore.NewUnsupportedError("read_thread_by_rollout_path")
}

// readRow 读一行；不存在/已删除 → ThreadNotFound；archived 且不含归档 → ThreadNotFound。
func (t *ThreadStore) readRow(ctx context.Context, tx pgx.Tx, threadID protocol.ThreadID, includeArchived bool) (threadRow, error) {
	var (
		row     threadRow
		rawMeta []byte
	)
	err := tx.QueryRow(ctx, `SELECT id, agent_id, parent_thread_id, title, status, model, last_seq, created_at, updated_at, archived_at, metadata
		FROM agent_threads WHERE id = $1 AND status <> 'deleted'`, threadID.String()).
		Scan(&row.ID, &row.AgentID, &row.ParentID, &row.Title, &row.Status, &row.Model, &row.LastSeq, &row.CreatedAt, &row.UpdatedAt, &row.ArchivedAt, &rawMeta)
	if errors.Is(err, pgx.ErrNoRows) {
		return threadRow{}, threadstore.NewThreadNotFoundError(threadID)
	}
	if err != nil {
		return threadRow{}, storeErr(err, "read thread %s", threadID)
	}
	if row.Status == "archived" && !includeArchived {
		return threadRow{}, threadstore.NewThreadNotFoundError(threadID)
	}
	if row.Meta, err = unmarshalMeta(rawMeta); err != nil {
		return threadRow{}, threadstore.NewInternalError(err, "decode thread metadata for %s", threadID)
	}
	return row, nil
}

// listCursor 是 keyset 游标（updated_at, id）。
type listCursor struct {
	UpdatedAt time.Time `json:"u"`
	ID        string    `json:"i"`
}

func encodeCursor(c listCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s *string) (*listCursor, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(*s)
	if err != nil {
		return nil, threadstore.NewInvalidRequestError("invalid cursor")
	}
	var c listCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, threadstore.NewInvalidRequestError("invalid cursor")
	}
	return &c, nil
}

func normalizePageSize(n int) int {
	switch {
	case n <= 0:
		return 25
	case n > 200:
		return 200
	default:
		return n
	}
}

// ListThreads 按 updated_at DESC, id DESC keyset 分页；Archived 选归档集合；SearchTerm 在
// title/preview 上 ILIKE；ModelProviders 过滤 metadata->>'model_provider'。
func (t *ThreadStore) ListThreads(ctx context.Context, params threadstore.ListThreadsParams) (threadstore.ThreadPage, error) {
	page, err := t.list(ctx, params.PageSize, params.Cursor, params.Archived, params.SearchTerm, params.ModelProviders, params.SortDirection)
	if err != nil {
		return threadstore.ThreadPage{}, err
	}
	return page, nil
}

// SearchThreads 是带 term 的 ListThreads，snippet 取命中的 title 或 preview。
func (t *ThreadStore) SearchThreads(ctx context.Context, params threadstore.SearchThreadsParams) (threadstore.ThreadSearchPage, error) {
	term := strings.TrimSpace(params.SearchTerm)
	if term == "" {
		return threadstore.ThreadSearchPage{}, threadstore.NewInvalidRequestError("search term is required")
	}
	page, err := t.list(ctx, params.PageSize, params.Cursor, params.Archived, &term, nil, params.SortDirection)
	if err != nil {
		return threadstore.ThreadSearchPage{}, err
	}
	out := threadstore.ThreadSearchPage{NextCursor: page.NextCursor}
	lowered := strings.ToLower(term)
	for _, th := range page.Items {
		snippet := th.Preview
		if th.Name != nil && strings.Contains(strings.ToLower(*th.Name), lowered) {
			snippet = *th.Name
		}
		out.Items = append(out.Items, threadstore.StoredThreadSearchResult{Thread: th, Snippet: snippet})
	}
	return out, nil
}

// listFilter 是线程列表 / 搜索的过滤条件。
type listFilter struct {
	archived  bool
	term      *string
	providers *[]string
	dir       threadstore.SortDirection
}

func (t *ThreadStore) list(ctx context.Context, pageSize int, cursor *string, archived bool, term *string, providers *[]string, dir threadstore.SortDirection) (threadstore.ThreadPage, error) {
	size := normalizePageSize(pageSize)
	anchor, err := decodeCursor(cursor)
	if err != nil {
		return threadstore.ThreadPage{}, err
	}
	q, args := listQuery(listFilter{archived: archived, term: term, providers: providers, dir: dir}, anchor, size)
	var out threadstore.ThreadPage
	err = t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return storeErr(err, "list threads")
		}
		defer rows.Close()
		page, err := scanThreadPage(rows, size)
		if err != nil {
			return err
		}
		out = page
		return nil
	})
	return out, err
}

// listQuery 拼 keyset 分页的线程查询（多取一行判断有无下一页）。
func listQuery(f listFilter, anchor *listCursor, size int) (string, []any) {
	var (
		where []string
		args  []any
	)
	if f.archived {
		where = append(where, "status = 'archived'")
	} else {
		where = append(where, "status NOT IN ('archived', 'deleted')")
	}
	if f.term != nil && *f.term != "" {
		args = append(args, "%"+*f.term+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(title ILIKE $%d OR metadata->>'preview' ILIKE $%d OR metadata->>'name' ILIKE $%d)", n, n, n))
	}
	if f.providers != nil && len(*f.providers) > 0 {
		args = append(args, *f.providers)
		where = append(where, fmt.Sprintf("metadata->>'model_provider' = ANY($%d)", len(args)))
	}
	order, cmp := "DESC", "<"
	if f.dir == threadstore.SortDirectionAsc {
		order, cmp = "ASC", ">"
	}
	if anchor != nil {
		args = append(args, anchor.UpdatedAt, anchor.ID)
		n := len(args)
		where = append(where, fmt.Sprintf("(updated_at, id) %s ($%d, $%d)", cmp, n-1, n))
	}
	args = append(args, size+1)
	q := fmt.Sprintf(`SELECT id, agent_id, parent_thread_id, title, status, model, last_seq, created_at, updated_at, archived_at, metadata
		FROM agent_threads WHERE %s ORDER BY updated_at %s, id %s LIMIT $%d`, strings.Join(where, " AND "), order, order, len(args))
	return q, args
}

// scanThreadPage 把线程行装成页；第 size+1 行只用来给出游标。
func scanThreadPage(rows pgx.Rows, size int) (threadstore.ThreadPage, error) {
	var (
		out  threadstore.ThreadPage
		last threadRow
	)
	for rows.Next() {
		var (
			row     threadRow
			rawMeta []byte
			err     error
		)
		if err = rows.Scan(&row.ID, &row.AgentID, &row.ParentID, &row.Title, &row.Status, &row.Model, &row.LastSeq, &row.CreatedAt, &row.UpdatedAt, &row.ArchivedAt, &rawMeta); err != nil {
			return threadstore.ThreadPage{}, storeErr(err, "scan thread row")
		}
		if row.Meta, err = unmarshalMeta(rawMeta); err != nil {
			return threadstore.ThreadPage{}, threadstore.NewInternalError(err, "decode thread metadata")
		}
		if len(out.Items) == size {
			c := encodeCursor(listCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
			out.NextCursor = &c
			break
		}
		out.Items = append(out.Items, row.storedThread())
		last = row
	}
	return out, storeErr(rows.Err(), "iterate threads")
}

// UpdateThreadMetadata 把 patch 合并进 metadata（并把 name/preview 同步到 title 列），返回新摘要。
func (t *ThreadStore) UpdateThreadMetadata(ctx context.Context, params threadstore.UpdateThreadMetadataParams) (threadstore.StoredThread, error) {
	var out threadstore.StoredThread
	err := t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := t.readRow(ctx, tx, params.ThreadID, params.IncludeArchived)
		if err != nil {
			return err
		}
		if params.Patch.IsEmpty() {
			out = row.storedThread()
			return nil
		}
		row.Meta = row.Meta.applyPatch(params.Patch)
		rawMeta, err := marshalMeta(row.Meta)
		if err != nil {
			return threadstore.NewInternalError(err, "encode thread metadata")
		}
		title := row.Title
		if row.Meta.Name != nil {
			title = *row.Meta.Name
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_threads SET metadata = $2, title = $3, updated_at = now() WHERE id = $1`,
			params.ThreadID.String(), rawMeta, title); err != nil {
			return storeErr(err, "update thread metadata %s", params.ThreadID)
		}
		row, err = t.readRow(ctx, tx, params.ThreadID, true)
		if err != nil {
			return err
		}
		out = row.storedThread()
		return nil
	})
	return out, err
}

// ArchiveThread 标 archived。
func (t *ThreadStore) ArchiveThread(ctx context.Context, params threadstore.ArchiveThreadParams) error {
	return t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := t.readRow(ctx, tx, params.ThreadID, true); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_threads SET status = 'archived', archived_at = COALESCE(archived_at, now()), updated_at = now()
			WHERE id = $1 AND status <> 'deleted'`, params.ThreadID.String())
		return storeErr(err, "archive thread %s", params.ThreadID)
	})
}

// ArchiveThreads 按 Rust 默认顺序语义。
func (t *ThreadStore) ArchiveThreads(ctx context.Context, params threadstore.ArchiveThreadsParams) ([]protocol.ThreadID, error) {
	return threadstore.ArchiveThreadsSequentially(ctx, t, params, nil)
}

// UnarchiveThread 回 idle 并返回摘要。
func (t *ThreadStore) UnarchiveThread(ctx context.Context, params threadstore.ArchiveThreadParams) (threadstore.StoredThread, error) {
	var out threadstore.StoredThread
	err := t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := t.readRow(ctx, tx, params.ThreadID, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_threads SET status = 'idle', archived_at = NULL, updated_at = now()
			WHERE id = $1 AND status = 'archived'`, params.ThreadID.String()); err != nil {
			return storeErr(err, "unarchive thread %s", params.ThreadID)
		}
		row, err := t.readRow(ctx, tx, params.ThreadID, true)
		if err != nil {
			return err
		}
		out = row.storedThread()
		return nil
	})
	return out, err
}

// DeleteThread 标 deleted 并级联子线程与队列；若有集合外的子线程仍引用则拒绝
// （AR_AGENT_THREAD_IN_USE）。0.147 delete_thread：单线程删除时子线程都在"集合外"，
// 故有活子线程即拒；DeleteThreads 把整批当集合处理。
func (t *ThreadStore) DeleteThread(ctx context.Context, params threadstore.DeleteThreadParams) error {
	return t.DeleteThreads(ctx, threadstore.DeleteThreadsParams{ThreadIDs: []protocol.ThreadID{params.ThreadID}})
}

// DeleteThreads 批量删除（同一事务）：缺失成员视为已删；集合外仍被引用 → AR_AGENT_THREAD_IN_USE。
func (t *ThreadStore) DeleteThreads(ctx context.Context, params threadstore.DeleteThreadsParams) error {
	if len(params.ThreadIDs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(params.ThreadIDs))
	for _, id := range params.ThreadIDs {
		ids = append(ids, id.String())
	}
	return t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// 至少一个成员必须存在（单删语义：不存在 → ThreadNotFound）。
		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_threads WHERE id = ANY($1) AND status <> 'deleted'`, ids).Scan(&existing); err != nil {
			return storeErr(err, "count threads to delete")
		}
		if existing == 0 && len(ids) == 1 {
			return threadstore.NewThreadNotFoundError(params.ThreadIDs[0])
		}
		// 集合外的活子线程仍引用集合内线程 → 拒绝。
		var inUse int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_threads
			WHERE parent_thread_id = ANY($1) AND NOT (id = ANY($1)) AND status <> 'deleted'`, ids).Scan(&inUse); err != nil {
			return storeErr(err, "check thread references")
		}
		if inUse > 0 {
			return apierror.New(apierror.CodeAgentThreadInUse)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agent_thread_queue WHERE thread_id = ANY($1)`, ids); err != nil {
			return storeErr(err, "delete thread queue")
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_threads SET status = 'deleted', updated_at = now() WHERE id = ANY($1)`, ids); err != nil {
			return storeErr(err, "delete threads")
		}
		return nil
	})
}
