package pgstore

import (
	"testing"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"
	"github.com/sqlrush/codexgo/pkg/threadstore/contracttest"
)

func strp(s string) *string { return &s }

func TestMetaFromCreateDefaults(t *testing.T) {
	id := contracttest.ThreadID(1)
	params := contracttest.DefaultCreateParams(t, id)
	params.Metadata.ModelProvider = ""
	parent := contracttest.ThreadID(2)
	params.ForkedFromID = &parent
	params.Metadata.MemoryMode = protocol.ThreadMemoryMode("disabled")

	m := metaFromCreate(params, "chat-default")
	if m.ModelProvider != "chat-default" || m.HistoryMode != protocol.ThreadHistoryModePaginated {
		t.Fatalf("defaults = %+v", m)
	}
	if m.ForkedFromID == nil || *m.ForkedFromID != parent.String() || m.Cwd == "" || m.Source == nil || m.MemoryMode == nil {
		t.Fatalf("meta = %+v", m)
	}
	raw, err := marshalMeta(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := unmarshalMeta(raw)
	if err != nil || back.ModelProvider != "chat-default" || back.ForkedFromID == nil {
		t.Fatalf("round trip = %+v (%v)", back, err)
	}
	if empty, err := unmarshalMeta(nil); err != nil || empty.ModelProvider != "" {
		t.Fatalf("empty meta = %+v (%v)", empty, err)
	}
}

func TestApplyPatchIsImmutableAndFieldWise(t *testing.T) {
	base := threadMeta{Name: strp("old"), Preview: "p0", ModelProvider: "prov"}
	earlier := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	effort := protocol.ReasoningEffort("high")
	sha := "abc123"

	first := base.applyPatch(threadstore.ThreadMetadataPatch{
		Preview:          strp("p1"),
		Model:            strp("chat-fast"),
		AdvanceRecencyAt: &later,
		ReasoningEffort:  threadstore.SetClearable(effort),
		GitInfo:          &threadstore.GitInfoPatch{SHA: threadstore.SetClearable(sha), Branch: threadstore.SetClearable("main")},
		FirstUserMessage: strp("hi"),
	})
	if base.Preview != "p0" || base.Model != nil || base.RecencyAt != nil {
		t.Fatalf("applyPatch mutated its receiver: %+v", base)
	}
	if first.Preview != "p1" || first.Model == nil || *first.Model != "chat-fast" || first.Name == nil || *first.Name != "old" {
		t.Fatalf("first = %+v", first)
	}
	if first.RecencyAt == nil || !first.RecencyAt.Equal(later) || first.ReasoningEffort == nil || *first.ReasoningEffort != effort {
		t.Fatalf("first recency/effort = %+v", first)
	}
	if first.GitInfo == nil || first.GitInfo.CommitHash == nil || first.GitInfo.Branch == nil || *first.GitInfo.Branch != "main" {
		t.Fatalf("git info = %+v", first.GitInfo)
	}

	// 回退的 recency 不生效；Name 显式清空；GitInfo 清到全空 → nil
	second := first.applyPatch(threadstore.ThreadMetadataPatch{
		AdvanceRecencyAt: &earlier,
		Name:             threadstore.ClearField[string](),
		GitInfo:          &threadstore.GitInfoPatch{SHA: threadstore.ClearField[string](), Branch: threadstore.ClearField[string]()},
	})
	if !second.RecencyAt.Equal(later) {
		t.Fatalf("recency must only advance: %v", second.RecencyAt)
	}
	if second.Name != nil {
		t.Fatalf("name must be cleared: %v", *second.Name)
	}
	if second.GitInfo != nil {
		t.Fatalf("git info must collapse to nil: %+v", second.GitInfo)
	}
	// Title 走 Name
	third := second.applyPatch(threadstore.ThreadMetadataPatch{Title: strp("标题")})
	if third.Name == nil || *third.Name != "标题" {
		t.Fatalf("title → name: %+v", third)
	}
}

func TestStoredThreadProjectionDefaults(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	parent := contracttest.ThreadID(9).String()
	row := threadRow{
		ID:        contracttest.ThreadID(1).String(),
		Title:     "行上的标题",
		Model:     "chat-default",
		Status:    "idle",
		CreatedAt: now,
		UpdatedAt: now,
		ParentID:  &parent,
		Meta:      threadMeta{RecencyAt: &later},
	}
	st := row.storedThread()
	if st.Name == nil || *st.Name != "行上的标题" || st.Model == nil || *st.Model != "chat-default" {
		t.Fatalf("column fallbacks = %+v", st)
	}
	if !st.RecencyAt.Equal(later) || st.ParentThreadID == nil || st.ParentThreadID.String() != parent {
		t.Fatalf("recency/parent = %+v", st)
	}
	if st.HistoryMode != protocol.ThreadHistoryModePaginated || st.Source.Kind != rollout.DefaultSessionSource().Kind {
		t.Fatalf("defaults = %+v", st)
	}
	if st.ApprovalMode != threadstore.OnRequestApproval() {
		t.Fatalf("approval default = %v", st.ApprovalMode)
	}
	// 元数据里的值优先于列
	row.Meta.Name = strp("meta 名")
	row.Meta.Model = strp("chat-fast")
	st = row.storedThread()
	if *st.Name != "meta 名" || *st.Model != "chat-fast" {
		t.Fatalf("meta precedence = %+v", st)
	}
}

func TestApplyPatchAgentAndRuntimeFields(t *testing.T) {
	src := rollout.NewCliSource()
	ts := rollout.ThreadSource("subagent_review")
	am := threadstore.OnRequestApproval()
	pp := threadstore.ReadOnlyPermissionProfile()
	tu := protocol.TokenUsage{TotalTokens: 5}
	mm := protocol.ThreadMemoryMode("disabled")
	out := threadMeta{}.applyPatch(threadstore.ThreadMetadataPatch{
		Source:            &src,
		ThreadSource:      threadstore.SetClearable(ts),
		AgentNickname:     threadstore.SetClearable("nick"),
		AgentRole:         threadstore.SetClearable("role"),
		AgentPath:         threadstore.SetClearable("a/b"),
		Cwd:               strp("/w"),
		CliVersion:        strp("v1"),
		ApprovalMode:      &am,
		PermissionProfile: &pp,
		TokenUsage:        &tu,
		MemoryMode:        &mm,
		ModelProvider:     strp("prov2"),
	})
	if out.Source == nil || out.ThreadSource == nil || *out.ThreadSource != ts || out.AgentNickname == nil || *out.AgentNickname != "nick" ||
		out.AgentRole == nil || out.AgentPath == nil || out.Cwd != "/w" || out.CliVersion != "v1" || out.ApprovalMode == nil ||
		out.PermissionProfile == nil || out.TokenUsage == nil || out.TokenUsage.TotalTokens != 5 || out.MemoryMode == nil || out.ModelProvider != "prov2" {
		t.Fatalf("applied = %+v", out)
	}
	cleared := out.applyPatch(threadstore.ThreadMetadataPatch{ThreadSource: threadstore.ClearField[rollout.ThreadSource](), AgentNickname: threadstore.ClearField[string]()})
	if cleared.ThreadSource != nil || cleared.AgentNickname != nil || cleared.AgentRole == nil {
		t.Fatalf("cleared = %+v", cleared)
	}
	st := threadRow{ID: contracttest.ThreadID(3).String(), Meta: cleared}.storedThread()
	if st.ApprovalMode != am || st.Source.Kind != src.Kind || st.TokenUsage == nil {
		t.Fatalf("projection = %+v", st)
	}
}
