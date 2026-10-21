package main

import "testing"

func TestWebView2VersionUsable(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"120.0.2210.91", true},
		{" 131.0.2903.86 ", true},
		{"", false},
		{"   ", false},
		{"0.0.0.0", false}, // EdgeUpdate 卸载后残留的值
		{"0.1.2.3", false}, // 主版本 0 视为不可用
		{"not-a-version", false},
		{"abc.1.2.3", false},
	}
	for _, c := range cases {
		if got := webView2VersionUsable(c.raw); got != c.want {
			t.Fatalf("%q: 期望 %v，实际 %v", c.raw, c.want, got)
		}
	}
}
