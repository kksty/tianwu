package main

import (
	"encoding/json"
	"testing"
)

// The console's per-project form sends three shapes: null (inherit), 0
// (uncapped) and a positive integer. Anything else must be a 400, not a
// silently coerced value.
func TestParseMaxRuns(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		set   bool
		value int
		ok    bool
	}{
		{name: "null 继承", raw: `null`, ok: true},
		{name: "0 不限", raw: `0`, set: true, value: 0, ok: true},
		{name: "正整数", raw: `2`, set: true, value: 2, ok: true},
		{name: "负数拒绝", raw: `-1`, ok: false},
		{name: "字符串拒绝", raw: `"3"`, ok: false},
		{name: "小数拒绝", raw: `1.5`, ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseMaxRuns(json.RawMessage(c.raw))
			if (err == nil) != c.ok {
				t.Fatalf("parseMaxRuns(%s) err = %v, want ok=%v", c.raw, err, c.ok)
			}
			if err != nil {
				return
			}
			if (got != nil) != c.set {
				t.Fatalf("parseMaxRuns(%s) = %v, set=%v", c.raw, got, c.set)
			}
			if c.set && *got != c.value {
				t.Fatalf("parseMaxRuns(%s) = %d, want %d", c.raw, *got, c.value)
			}
		})
	}
}
