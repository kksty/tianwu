package main

import "testing"

// The simplified console sends one paragraph and no name; the server derives a
// readable project name instead of rejecting the request.
func TestProjectNameFromBrief(t *testing.T) {
	cases := []struct{ brief, want string }{
		{"", "未命名项目"},
		{"   \n\t\n", "未命名项目"},
		{"测试 http://10.0.0.5 的 /admin 越权\n补充说明", "测试 http://10.0.0.5 的 /admin 越权"},
		{"第一行\n第二行", "第一行"},
	}
	for _, tc := range cases {
		if got := projectNameFromBrief(tc.brief); got != tc.want {
			t.Fatalf("projectNameFromBrief(%q) = %q, want %q", tc.brief, got, tc.want)
		}
	}
	long := ""
	for i := 0; i < 60; i++ {
		long += "字"
	}
	got := projectNameFromBrief(long)
	if len([]rune(got)) != 41 { // 40 runes + ellipsis
		t.Fatalf("long brief name = %q (%d runes), want 40 + ellipsis", got, len([]rune(got)))
	}
}
