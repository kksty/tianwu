package project

import (
	"strings"
	"testing"
	"time"
)

// The console edits the per-project commander round wall clock. Empty means
// "inherit the instance --round-timeout", "0" means unlimited, and malformed or
// negative values are errors — a typo must not silently become "inherit" (D91).
func TestParseRoundTimeout(t *testing.T) {
	cases := []struct {
		raw    string
		want   time.Duration
		has    bool
		errHas string
	}{
		{raw: "", has: false},
		{raw: "   ", has: false},
		{raw: "15m", want: 15 * time.Minute, has: true},
		{raw: "90s", want: 90 * time.Second, has: true},
		{raw: "0", want: 0, has: true},
		{raw: "-1m", errHas: "负数"},
		{raw: "abc", errHas: "合法时长"},
	}
	for _, c := range cases {
		d, has, err := ParseRoundTimeout(c.raw)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Fatalf("%q: err = %v, want containing %q", c.raw, err, c.errHas)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: unexpected error %v", c.raw, err)
		}
		if has != c.has || (has && d != c.want) {
			t.Fatalf("%q = (%s, %v), want (%s, %v)", c.raw, d, has, c.want, c.has)
		}
	}
}

// The override lives in the manifest: it survives a restart, and clearing it
// restores "inherit the instance default" instead of writing 0 (unlimited).
func TestRoundTimeoutOverridePersists(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if raw := p.RoundTimeoutRaw(); raw != "" {
		t.Fatalf("a new project has an override %q, want inherit", raw)
	}
	if err := m.SetRoundTimeout(p.ID, "10m"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRoundTimeout(p.ID, "nope"); err == nil {
		t.Fatal("an invalid override was accepted")
	}

	m2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	again := m2.Get(p.ID)
	if raw := again.RoundTimeoutRaw(); raw != "10m" {
		t.Fatalf("override after reopen = %q, want 10m", raw)
	}
	if d, ok, err := ParseRoundTimeout(again.RoundTimeoutRaw()); err != nil || !ok || d != 10*time.Minute {
		t.Fatalf("override decodes to (%s, %v, %v)", d, ok, err)
	}

	if err := m2.SetRoundTimeout(p.ID, ""); err != nil {
		t.Fatal(err)
	}
	m3, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if raw := m3.Get(p.ID).RoundTimeoutRaw(); raw != "" {
		t.Fatalf("cleared override survived the restart: %q", raw)
	}
}
