package main

import "testing"

// TestBanner spec-0.4 D5 范本：表驱动 + t.Parallel 默认 + 行为命名
// （development-standards §2 / spec-0.4 §2.2 约定的活文档）。
func TestBanner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{name: "dev_version", version: "0.0.0-dev", want: "console 0.0.0-dev"},
		{name: "release_version", version: "1.2.3", want: "console 1.2.3"},
		{name: "rc_version", version: "0.1.0-rc.1", want: "console 0.1.0-rc.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := banner(tc.version); got != tc.want {
				t.Fatalf("banner(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}
