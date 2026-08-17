package pgstore

import (
	"encoding/json"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"
)

// threadMeta 是 agent_threads.metadata 里的 codexgo 线程元数据（StoredThread 的可变部分）。
// 列上只放 runtime 自己要查询的字段（status/model/agent_id/parent/last_seq/heartbeat…），
// codexgo 侧的观测型元数据整体存 jsonb，避免每次 0.147 加字段就动 schema。
type threadMeta struct {
	ForkedFromID      *string                     `json:"forked_from_id,omitempty"`
	Preview           string                      `json:"preview,omitempty"`
	Name              *string                     `json:"name,omitempty"`
	ModelProvider     string                      `json:"model_provider,omitempty"`
	Model             *string                     `json:"model,omitempty"`
	ReasoningEffort   *protocol.ReasoningEffort   `json:"reasoning_effort,omitempty"`
	RecencyAt         *time.Time                  `json:"recency_at,omitempty"`
	Cwd               string                      `json:"cwd,omitempty"`
	CliVersion        string                      `json:"cli_version,omitempty"`
	Source            *rollout.SessionSource      `json:"source,omitempty"`
	HistoryMode       protocol.ThreadHistoryMode  `json:"history_mode,omitempty"`
	ThreadSource      *rollout.ThreadSource       `json:"thread_source,omitempty"`
	AgentNickname     *string                     `json:"agent_nickname,omitempty"`
	AgentRole         *string                     `json:"agent_role,omitempty"`
	AgentPath         *string                     `json:"agent_path,omitempty"`
	GitInfo           *protocol.GitInfo           `json:"git_info,omitempty"`
	ApprovalMode      *protocol.AskForApproval    `json:"approval_mode,omitempty"`
	PermissionProfile *protocol.PermissionProfile `json:"permission_profile,omitempty"`
	TokenUsage        *protocol.TokenUsage        `json:"token_usage,omitempty"`
	FirstUserMessage  *string                     `json:"first_user_message,omitempty"`
	MemoryMode        *protocol.ThreadMemoryMode  `json:"memory_mode,omitempty"`
	Originator        string                      `json:"originator,omitempty"`
	InitialWindowID   string                      `json:"initial_window_id,omitempty"`
	HistoryBase       *protocol.HistoryPosition   `json:"history_base,omitempty"`
	DynamicTools      []protocol.DynamicToolSpec  `json:"dynamic_tools,omitempty"`
	BaseInstructions  *rollout.BaseInstructions   `json:"base_instructions,omitempty"`
}

// metaFromCreate 从 CreateThreadParams 派生初始元数据。
func metaFromCreate(p threadstore.CreateThreadParams, defaultProvider string) threadMeta {
	m := threadMeta{
		ModelProvider:   p.Metadata.ModelProvider,
		HistoryMode:     p.HistoryMode,
		Originator:      p.Originator,
		InitialWindowID: p.InitialWindowID,
		HistoryBase:     p.HistoryBase,
		DynamicTools:    p.DynamicTools,
	}
	if m.ModelProvider == "" {
		m.ModelProvider = defaultProvider
	}
	if m.HistoryMode == "" {
		m.HistoryMode = protocol.ThreadHistoryModePaginated
	}
	if p.ForkedFromID != nil {
		s := p.ForkedFromID.String()
		m.ForkedFromID = &s
	}
	if p.Metadata.Cwd != nil {
		m.Cwd = *p.Metadata.Cwd
	}
	src := p.Source
	m.Source = &src
	m.ThreadSource = p.ThreadSource
	m.AgentNickname = cloneStr(p.Source.Nickname())
	m.AgentRole = cloneStr(p.Source.AgentRole())
	if ap := p.Source.AgentPath(); ap != nil {
		s := ap.String()
		m.AgentPath = &s
	}
	if p.Metadata.MemoryMode != "" {
		mm := p.Metadata.MemoryMode
		m.MemoryMode = &mm
	}
	bi := p.BaseInstructions
	m.BaseInstructions = &bi
	return m
}

// applyPatch 把 ThreadMetadataPatch 按字段存在语义合并进元数据（Rust apply_metadata_update）。
// 返回新值，不改入参。
func (m threadMeta) applyPatch(p threadstore.ThreadMetadataPatch) threadMeta {
	return m.applyIdentity(p).applyAgentFields(p).applyRuntimeFields(p)
}

// applyIdentity 合并名称 / 预览 / 模型 / recency 一类"身份"字段。
func (m threadMeta) applyIdentity(p threadstore.ThreadMetadataPatch) threadMeta {
	out := m
	if p.Name.IsSome() {
		out.Name = cloneStr(threadstore.Flatten(p.Name))
	}
	if p.Preview != nil {
		out.Preview = *p.Preview
	}
	if p.Title != nil {
		out.Name = cloneStr(p.Title)
	}
	if p.ModelProvider != nil {
		out.ModelProvider = *p.ModelProvider
	}
	if p.Model != nil {
		out.Model = cloneStr(p.Model)
	}
	if p.ReasoningEffort.IsSome() {
		out.ReasoningEffort = cloneEffort(threadstore.Flatten(p.ReasoningEffort))
	}
	if p.AdvanceRecencyAt != nil {
		t := p.AdvanceRecencyAt.UTC()
		if out.RecencyAt == nil || t.After(*out.RecencyAt) {
			out.RecencyAt = &t
		}
	}
	if p.FirstUserMessage != nil {
		out.FirstUserMessage = cloneStr(p.FirstUserMessage)
	}
	return out
}

// applyAgentFields 合并来源 / 子 agent 身份字段。
func (m threadMeta) applyAgentFields(p threadstore.ThreadMetadataPatch) threadMeta {
	out := m
	if p.Source != nil {
		src := *p.Source
		out.Source = &src
	}
	if p.ThreadSource.IsSome() {
		out.ThreadSource = threadstore.Flatten(p.ThreadSource)
	}
	if p.AgentNickname.IsSome() {
		out.AgentNickname = cloneStr(threadstore.Flatten(p.AgentNickname))
	}
	if p.AgentRole.IsSome() {
		out.AgentRole = cloneStr(threadstore.Flatten(p.AgentRole))
	}
	if p.AgentPath.IsSome() {
		out.AgentPath = cloneStr(threadstore.Flatten(p.AgentPath))
	}
	return out
}

// applyRuntimeFields 合并 cwd / 版本 / 审批 / 权限 / token / git / memory 字段。
func (m threadMeta) applyRuntimeFields(p threadstore.ThreadMetadataPatch) threadMeta {
	out := m
	if p.Cwd != nil {
		out.Cwd = *p.Cwd
	}
	if p.CliVersion != nil {
		out.CliVersion = *p.CliVersion
	}
	if p.ApprovalMode != nil {
		am := *p.ApprovalMode
		out.ApprovalMode = &am
	}
	if p.PermissionProfile != nil {
		pp := *p.PermissionProfile
		out.PermissionProfile = &pp
	}
	if p.TokenUsage != nil {
		tu := *p.TokenUsage
		out.TokenUsage = &tu
	}
	if p.GitInfo != nil {
		out.GitInfo = mergeGitInfo(out.GitInfo, *p.GitInfo)
	}
	if p.MemoryMode != nil {
		mm := *p.MemoryMode
		out.MemoryMode = &mm
	}
	return out
}

// mergeGitInfo 用 GitInfoPatch 更新 GitInfo（clearable 语义），全空返回 nil。
func mergeGitInfo(cur *protocol.GitInfo, p threadstore.GitInfoPatch) *protocol.GitInfo {
	var out protocol.GitInfo
	if cur != nil {
		out = *cur
	}
	if p.SHA.IsSome() {
		if v := threadstore.Flatten(p.SHA); v != nil {
			sha := protocol.NewGitSha(*v)
			out.CommitHash = &sha
		} else {
			out.CommitHash = nil
		}
	}
	if p.Branch.IsSome() {
		out.Branch = cloneStr(threadstore.Flatten(p.Branch))
	}
	if p.OriginURL.IsSome() {
		out.RepositoryURL = cloneStr(threadstore.Flatten(p.OriginURL))
	}
	if out.CommitHash == nil && out.Branch == nil && out.RepositoryURL == nil {
		return nil
	}
	return &out
}

// threadRow 是 agent_threads 一行的列值（runtime 侧关心的字段）。
type threadRow struct {
	ID         string
	AgentID    *string
	ParentID   *string
	Title      string
	Status     string
	Model      string
	LastSeq    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
	Meta       threadMeta
}

// storedThread 把行 + 元数据投影成 codexgo StoredThread。
func (r threadRow) storedThread() threadstore.StoredThread {
	m := r.Meta
	st := threadstore.StoredThread{
		ThreadID:         protocol.NewThreadID(r.ID),
		Preview:          m.Preview,
		Name:             cloneStr(m.Name),
		ModelProvider:    m.ModelProvider,
		Model:            cloneStr(m.Model),
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		RecencyAt:        r.UpdatedAt,
		ArchivedAt:       r.ArchivedAt,
		Cwd:              m.Cwd,
		CliVersion:       m.CliVersion,
		HistoryMode:      m.HistoryMode,
		ThreadSource:     m.ThreadSource,
		AgentNickname:    cloneStr(m.AgentNickname),
		AgentRole:        cloneStr(m.AgentRole),
		AgentPath:        cloneStr(m.AgentPath),
		GitInfo:          m.GitInfo,
		TokenUsage:       m.TokenUsage,
		FirstUserMessage: cloneStr(m.FirstUserMessage),
		ReasoningEffort:  cloneEffort(m.ReasoningEffort),
	}
	if m.Name == nil && r.Title != "" {
		st.Name = cloneStr(&r.Title)
	}
	if m.Model == nil && r.Model != "" {
		st.Model = cloneStr(&r.Model)
	}
	if m.RecencyAt != nil && m.RecencyAt.After(st.RecencyAt) {
		st.RecencyAt = *m.RecencyAt
	}
	if m.ForkedFromID != nil {
		id := protocol.NewThreadID(*m.ForkedFromID)
		st.ForkedFromID = &id
	}
	if r.ParentID != nil {
		id := protocol.NewThreadID(*r.ParentID)
		st.ParentThreadID = &id
	}
	return m.fillDefaults(st)
}

// fillDefaults 给未持久化的 Source / ApprovalMode / PermissionProfile / HistoryMode 补契约缺省。
func (m threadMeta) fillDefaults(st threadstore.StoredThread) threadstore.StoredThread {
	st.Source = rollout.DefaultSessionSource()
	if m.Source != nil {
		st.Source = *m.Source
	}
	st.ApprovalMode = threadstore.OnRequestApproval()
	if m.ApprovalMode != nil {
		st.ApprovalMode = *m.ApprovalMode
	}
	st.PermissionProfile = threadstore.ReadOnlyPermissionProfile()
	if m.PermissionProfile != nil {
		st.PermissionProfile = *m.PermissionProfile
	}
	if st.HistoryMode == "" {
		st.HistoryMode = protocol.ThreadHistoryModePaginated
	}
	return st
}

func cloneStr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneEffort(p *protocol.ReasoningEffort) *protocol.ReasoningEffort {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// marshalMeta / unmarshalMeta 是 metadata jsonb 的编解码。
func marshalMeta(m threadMeta) ([]byte, error) { return json.Marshal(m) }

func unmarshalMeta(raw []byte) (threadMeta, error) {
	var m threadMeta
	if len(raw) == 0 {
		return m, nil
	}
	err := json.Unmarshal(raw, &m)
	return m, err
}
