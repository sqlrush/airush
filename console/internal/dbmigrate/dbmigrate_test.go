package dbmigrate

import (
	"strings"
	"testing"
)

// TestValidateArgs 参数校验（无 DB 依赖路径，spec-0.8 期补齐单测口径）。
func TestValidateArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "up", args: []string{"up"}, want: "up"},
		{name: "down", args: []string{"down"}, want: "down"},
		{name: "version", args: []string{"version"}, want: "version"},
		{name: "empty", args: nil, wantErr: "用法"},
		{name: "too_many", args: []string{"up", "x"}, wantErr: "用法"},
		{name: "unknown", args: []string{"sideways"}, wantErr: "未知子命令"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v, want %q", got, err, tc.want)
			}
		})
	}
}

// TestToPgx5URL scheme 改写。
func TestToPgx5URL(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"postgres://u:p@h/db", "pgx5://u:p@h/db"},
		{"postgresql://u:p@h/db", "pgx5://u:p@h/db"},
		{"pgx5://u:p@h/db", "pgx5://u:p@h/db"},
	}
	for _, tc := range cases {
		if got := toPgx5URL(tc.in); got != tc.want {
			t.Fatalf("toPgx5URL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
