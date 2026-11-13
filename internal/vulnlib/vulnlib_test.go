package vulnlib

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpsertKeepsOneRowPerFact(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := l.Upsert(Entry{
		ProjectID: "acme", Project: "Acme", SourceID: "f_1",
		Title: "越权读订单", Summary: "普通用户可读全部订单", Status: "unverified",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Upsert(Entry{
		ProjectID: "acme", Project: "Acme", SourceID: "f_1",
		Title: "越权读订单", Summary: "普通用户可读全部订单", Status: "verified",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("status update created a second row: %s vs %s", first.ID, second.ID)
	}
	if !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatal("created_at moved on update")
	}
	list := l.List("", "", 0)
	if len(list) != 1 || list[0].Status != "verified" {
		t.Fatalf("want one verified row, got %+v", list)
	}
	if l.Len() != 1 {
		t.Fatalf("len = %d, want 1", l.Len())
	}
}

// The library must be self-contained: proof mode and the latest verdict are
// copied in at capture time, so they survive a restart (and a project delete)
// with no ledger left to ask (D105).
func TestUpsertRoundTripsProofAndReview(t *testing.T) {
	root := t.TempDir()
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if _, err := l.Upsert(Entry{
		ProjectID: "acme", SourceID: "f_1", Title: "固件里的硬编码密钥",
		Status: "verified", Proof: "static",
		Review: &Review{
			Verdict:     "pass",
			Notes:       "在 v2.1 固件里重放 strings 命令，密钥一致",
			VerifierRun: "r_99",
			CreatedAt:   created,
		},
	}); err != nil {
		t.Fatal(err)
	}

	l2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got := l2.List("", "", 0)
	if len(got) != 1 {
		t.Fatalf("reloaded %d rows, want 1", len(got))
	}
	if got[0].Proof != "static" {
		t.Fatalf("proof = %q, want static", got[0].Proof)
	}
	r := got[0].Review
	if r == nil {
		t.Fatal("review lost on reload")
	}
	if r.Verdict != "pass" || r.Notes == "" || r.VerifierRun != "r_99" || !r.CreatedAt.Equal(created) {
		t.Fatalf("review = %+v", r)
	}
}

func TestUpsertWithoutReviewStaysNil(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.Upsert(Entry{
		ProjectID: "acme", SourceID: "f_2", Title: "待复核", Status: "unverified",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Review != nil {
		t.Fatalf("a fact without reviews must not invent one: %+v", got.Review)
	}
}

func TestListFiltersByProjectAndText(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{ProjectID: "a", SourceID: "f1", Title: "SQL 注入", Status: "unverified"},
		{ProjectID: "b", SourceID: "f2", Title: "弱口令", Summary: "sa/123456", Status: "verified"},
	} {
		if _, err := l.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	if got := l.List("a", "", 0); len(got) != 1 || got[0].ProjectID != "a" {
		t.Fatalf("project filter: %+v", got)
	}
	if got := l.List("", "弱口令", 0); len(got) != 1 || got[0].SourceID != "f2" {
		t.Fatalf("text filter: %+v", got)
	}
	if got := l.List("", "", 0); len(got) != 2 {
		t.Fatalf("unfiltered: %+v", got)
	}
}

func TestCountMatchesListFilters(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{ProjectID: "a", SourceID: "f1", Title: "SQL 注入", Summary: "登录接口布尔盲注", Status: "unverified"},
		{ProjectID: "a", SourceID: "f2", Title: "越权读订单", Doc: "普通用户可读全部订单", Status: "verified"},
		{ProjectID: "b", SourceID: "f3", Title: "弱口令", Summary: "sa/123456", Status: "verified"},
	} {
		if _, err := l.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		project, q string
		want       int
		why        string
	}{
		{"", "", 3, "no filter counts the whole library"},
		{"a", "", 2, "project a"},
		{"b", "", 1, "project b"},
		{"c", "", 0, "project without entries"},
		{"", "弱口令", 1, "text in title"},
		{"", "全部订单", 1, "text in doc"},
		{"", "sql", 1, "text match ignores case"},
		{"a", "弱口令", 0, "project and text are ANDed"},
		{"b", "弱口令", 1, "project and text hit the same entry"},
	}
	for _, c := range cases {
		if got := l.Count(c.project, c.q); got != c.want {
			t.Errorf("Count(%q, %q) = %d, want %d (%s)", c.project, c.q, got, c.want, c.why)
		}
		// The count is only useful if it agrees with the rows List returns.
		if got := len(l.List(c.project, c.q, 0)); got != c.want {
			t.Errorf("List(%q, %q, 0) returned %d rows, want %d", c.project, c.q, got, c.want)
		}
	}
}

func TestCountIgnoresLimit(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{ProjectID: "a", SourceID: "f1", Title: "SQL 注入", Status: "unverified"},
		{ProjectID: "b", SourceID: "f2", Title: "弱口令", Status: "verified"},
		{ProjectID: "b", SourceID: "f3", Title: "弱口令 复用", Status: "verified"},
	} {
		if _, err := l.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	if got := l.List("", "", 1); len(got) != 1 {
		t.Fatalf("List(limit 1) = %d rows, want 1", len(got))
	}
	if got := l.Count("", ""); got != 3 {
		t.Fatalf("Count = %d, want all 3 even though the page holds 1", got)
	}
	if got := l.List("", "弱口令", 1); len(got) != 1 {
		t.Fatalf("filtered List(limit 1) = %d rows, want 1", len(got))
	}
	if got := l.Count("", "弱口令"); got != 2 {
		t.Fatalf("filtered Count = %d, want all 2 matches beyond the page limit", got)
	}
}

func TestCountEmptyLibrary(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Count("", ""); got != 0 {
		t.Fatalf("Count on empty library = %d, want 0", got)
	}
	if got := l.Count("a", "弱口令"); got != 0 {
		t.Fatalf("filtered Count on empty library = %d, want 0", got)
	}
}

func TestKeepEvidenceCopiesIntoLibrary(t *testing.T) {
	root := t.TempDir()
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseRoot, "in_x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "in_x", "resp.txt"), []byte("200 OK"), 0o644); err != nil {
		t.Fatal(err)
	}

	kept, dropped := l.KeepEvidence("acme", caseRoot, []string{"in_x/resp.txt", "in_x/missing.txt"})
	if len(kept) != 1 || kept[0] != "evidence/acme/in_x/resp.txt" {
		t.Fatalf("kept = %v", kept)
	}
	if len(dropped) != 1 || dropped[0] != "in_x/missing.txt" {
		t.Fatalf("dropped = %v", dropped)
	}
	if _, err := os.Stat(filepath.Join(root, "evidence", "acme", "in_x", "resp.txt")); err != nil {
		t.Fatalf("evidence not copied: %v", err)
	}
	if p, ok := l.EvidencePath(kept[0]); !ok {
		t.Fatal("evidence path not resolvable")
	} else if b, _ := os.ReadFile(p); string(b) != "200 OK" {
		t.Fatalf("copied content = %q", b)
	}
}

func TestEvidencePathRefusesEscape(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.EvidencePath("evidence/../../etc/passwd"); ok {
		t.Fatal("path traversal accepted")
	}
	if _, ok := l.EvidencePath("vulns.json"); ok {
		t.Fatal("index file is not evidence")
	}
}

func TestMarkOrphanedKeepsRows(t *testing.T) {
	root := t.TempDir()
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Upsert(Entry{ProjectID: "acme", SourceID: "f1", Title: "x", Status: "unverified"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Upsert(Entry{ProjectID: "beta", SourceID: "f2", Title: "y", Status: "unverified"}); err != nil {
		t.Fatal(err)
	}
	n, err := l.MarkOrphaned("acme")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("marked %d rows, want 1", n)
	}
	list := l.List("", "", 0)
	if len(list) != 2 {
		t.Fatalf("delete removed rows: %+v", list)
	}
	for _, e := range list {
		if (e.ProjectID == "acme") != e.Orphaned {
			t.Fatalf("orphan flag wrong for %+v", e)
		}
	}

	// The flag must survive a restart: the library is the record.
	l2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l2.List("acme", "", 0) {
		if !e.Orphaned {
			t.Fatal("orphan flag lost on reload")
		}
	}
}
