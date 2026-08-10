package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }

func TestDatasourceCreateValidate(t *testing.T) {
	t.Parallel()

	valid := func() datasourceCreateReq {
		return datasourceCreateReq{
			Name: "ds", EngineFamily: "postgres", ConnectMode: "connector",
			ConnectorID: "55555555-5555-5555-5555-555555555555", Host: "h", Port: 5432,
		}
	}
	tests := []struct {
		name      string
		mutate    func(*datasourceCreateReq)
		wantField string // "" = 期望通过
	}{
		{"合法 connector 模式", func(r *datasourceCreateReq) {}, ""},
		{"合法 direct 模式", func(r *datasourceCreateReq) {
			r.ConnectMode = "direct"
			r.ConnectorID = ""
			r.Credential = &credentialReq{Username: "u", Password: "p"}
		}, ""},
		{"缺 name", func(r *datasourceCreateReq) { r.Name = "" }, "name"},
		{"name 超长", func(r *datasourceCreateReq) { r.Name = strings.Repeat("x", 129) }, "name"},
		{"engine_family 非法", func(r *datasourceCreateReq) { r.EngineFamily = "oracle" }, "engine_family"},
		{"缺 host", func(r *datasourceCreateReq) { r.Host = "" }, "host"},
		{"port 越界", func(r *datasourceCreateReq) { r.Port = 70000 }, "port"},
		{"group 半配对", func(r *datasourceCreateReq) { r.GroupID = "g" }, "group_id"},
		{"group_role 非法", func(r *datasourceCreateReq) {
			r.GroupID = "g"
			r.GroupRole = "master"
		}, "group_role"},
		{"connector 模式缺 connector_id", func(r *datasourceCreateReq) { r.ConnectorID = "" }, "connect_mode"},
		{"connector 模式带凭据", func(r *datasourceCreateReq) {
			r.Credential = &credentialReq{Username: "u", Password: "p"}
		}, "connect_mode"},
		{"direct 模式缺凭据", func(r *datasourceCreateReq) {
			r.ConnectMode = "direct"
			r.ConnectorID = ""
		}, "connect_mode"},
		{"direct 模式带 connector_id", func(r *datasourceCreateReq) {
			r.ConnectMode = "direct"
			r.Credential = &credentialReq{Username: "u", Password: "p"}
		}, "connect_mode"},
		{"connect_mode 非法", func(r *datasourceCreateReq) { r.ConnectMode = "tunnel" }, "connect_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := valid()
			tt.mutate(&req)
			details := req.validate()
			if tt.wantField == "" {
				if len(details) != 0 {
					t.Fatalf("want valid, got %+v", details)
				}
				return
			}
			if len(details) == 0 || details[0].Field != tt.wantField {
				t.Fatalf("details = %+v, want field %s", details, tt.wantField)
			}
		})
	}
}

func TestPatchValidators(t *testing.T) {
	t.Parallel()

	if d := (&datasourcePatchReq{Name: strPtr("")}).validate(); len(d) == 0 {
		t.Fatal("empty name accepted")
	}
	if d := (&datasourcePatchReq{Port: intPtr(0)}).validate(); len(d) == 0 {
		t.Fatal("port 0 accepted")
	}
	if d := (&datasourcePatchReq{GroupRole: strPtr("master")}).validate(); len(d) == 0 {
		t.Fatal("bad group_role accepted")
	}
	if d := (&datasourcePatchReq{GroupRole: strPtr("")}).validate(); len(d) != 0 {
		t.Fatalf("unbind group_role rejected: %+v", d)
	}
	if d := (&agentPatchReq{Status: strPtr("stopped")}).validate(); len(d) == 0 {
		t.Fatal("bad agent status accepted")
	}
	if d := (&agentCreateReq{Name: "a", Kind: "domain"}).validate(); len(d) != 0 {
		t.Fatalf("valid agent rejected: %+v", d)
	}
	if d := (&agentCreateReq{Name: "a", Kind: "robot"}).validate(); len(d) == 0 {
		t.Fatal("bad agent kind accepted")
	}
	if d := (&groupCreateReq{Name: "g", Kind: "cluster"}).validate(); len(d) != 0 {
		t.Fatalf("valid group rejected: %+v", d)
	}
	if d := (&groupCreateReq{Name: "g", Kind: "pair"}).validate(); len(d) == 0 {
		t.Fatal("bad group kind accepted")
	}
	if d := (&aliasCreateReq{Alias: "生产库"}).validate(); len(d) != 0 {
		t.Fatalf("valid alias rejected: %+v", d)
	}
	if d := (&aliasCreateReq{Alias: "a", Source: "guess"}).validate(); len(d) == 0 {
		t.Fatal("bad alias source accepted")
	}
}

func TestCursorRoundTripAndTamper(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 10, 12, 0, 0, 12345, time.UTC)
	id := "55555555-5555-5555-5555-555555555555"
	cur, err := decodeCursor(encodeCursor(at, id))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cur.CreatedAt.Equal(at) || cur.ID != id {
		t.Fatalf("roundtrip = %+v", cur)
	}

	for _, bad := range []string{"!!!", "bm90LWEtY3Vyc29y", "MTIzLm5vdC11dWlk"} {
		if _, err := decodeCursor(bad); err == nil {
			t.Fatalf("cursor %q accepted", bad)
		}
	}
}

func TestParsePageParams(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest("GET", "/x", nil)
	cur, limit, err := parsePageParams(r)
	if err != nil || cur != nil || limit != defaultLimit {
		t.Fatalf("defaults = (%v, %d, %v)", cur, limit, err)
	}

	r = httptest.NewRequest("GET", "/x?limit=999", nil)
	if _, _, err := parsePageParams(r); err == nil {
		t.Fatal("limit 999 accepted")
	}
	r = httptest.NewRequest("GET", "/x?limit=abc", nil)
	if _, _, err := parsePageParams(r); err == nil {
		t.Fatal("limit abc accepted")
	}
	r = httptest.NewRequest("GET", "/x?cursor=broken", nil)
	if _, _, err := parsePageParams(r); err == nil {
		t.Fatal("broken cursor accepted")
	}
}

func TestNewPageAndHelpers(t *testing.T) {
	t.Parallel()

	p := newPage([]int{1, 2, 3}, 3, func(int) string { return "cur" })
	if p.NextCursor == nil || *p.NextCursor != "cur" {
		t.Fatalf("full page next_cursor = %v", p.NextCursor)
	}
	p = newPage([]int{1}, 3, func(int) string { return "cur" })
	if p.NextCursor != nil {
		t.Fatal("partial page has next_cursor")
	}
	p = newPage(nil, 3, func(int) string { return "" })
	if p.Items == nil || len(p.Items) != 0 {
		t.Fatal("nil items not normalized to empty slice")
	}

	if nilIfEmpty("") != nil {
		t.Fatal("nilIfEmpty empty")
	}
	if v := nilIfEmpty("x"); v == nil || *v != "x" {
		t.Fatal("nilIfEmpty non-empty")
	}
	if !isUUID("55555555-5555-5555-5555-555555555555") || isUUID("nope") {
		t.Fatal("isUUID")
	}
}

func TestDecodeStrictAndReadBody(t *testing.T) {
	t.Parallel()

	var dst struct {
		A string `json:"a"`
	}
	if err := decodeStrict([]byte(`{"a":"x"}`), &dst); err != nil || dst.A != "x" {
		t.Fatalf("valid decode: %v", err)
	}
	if err := decodeStrict([]byte(`{"unknown":1}`), &dst); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := decodeStrict([]byte(`not-json`), &dst); err == nil {
		t.Fatal("bad json accepted")
	}

	r := httptest.NewRequest("POST", "/x", strings.NewReader(strings.Repeat("x", maxBodyBytes+1)))
	if _, err := readBody(r); err == nil {
		t.Fatal("oversized body accepted")
	}
}
