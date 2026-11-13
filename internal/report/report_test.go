package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// fixture builds a complete, valid report directory and returns its parts so a
// test can break exactly one invariant at a time.
func fixture(t *testing.T) (dir string, db fixtureDB) {
	t.Helper()
	dir = t.TempDir()
	for _, sub := range []string{FindingsDir, EvidenceDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fact := model.Fact{
		ID: "f_1", IntentID: "in_1", Kind: "vuln", Title: "/admin 越权",
		Summary:     "普通用户可读全部订单",
		EvidenceIDs: []string{"e_1"}, Status: "verified", CreatedAt: time.Now(),
	}
	ev := model.Evidence{ID: "e_1", URI: "in_1/admin.html", Name: "admin.html"}

	write(EvidenceDir+"/e_1-admin.html", "HTTP/1.1 200 OK")
	write(Findings, `[{"fact":"f_1","verdict":"confirmed","title":"/admin 越权",
		"location":"http://t:18099/admin","preconditions":"普通用户会话","impact":"读取全部订单",
		"trace":"GET /admin → 200","evidence":["`+EvidenceDir+`/e_1-admin.html"]}]`)
	write(IndexFile, "# 报告\n\n- 结论：/admin 越权\n")
	write(FindingsDir+"/f_1.md", "# /admin 越权\n\n证据："+EvidenceDir+"/e_1-admin.html\n")

	return dir, fixtureDB{db: dbWith(fact, ev)}
}

type fixtureDB struct {
	db store.DB
}

func dbWith(f model.Fact, e model.Evidence) store.DB {
	return store.DB{Facts: []model.Fact{f}, Evidence: []model.Evidence{e}}
}

func validate(t *testing.T, dir string, fx fixtureDB) map[string]Check {
	t.Helper()
	out := map[string]Check{}
	for _, c := range Validate(dir, fx.db, DefaultStaleAfter) {
		out[c.Rule] = c
	}
	return out
}

func TestValidatePassesOnCompleteReport(t *testing.T) {
	dir, fx := fixture(t)
	checks := validate(t, dir, fx)
	if len(checks) != 10 {
		t.Fatalf("expected all 10 rules to run, got %d", len(checks))
	}
	for rule, c := range checks {
		if !c.OK {
			t.Errorf("rule %s failed: %s", rule, c.Detail)
		}
	}
}

func TestValidateCatchesMissingIndex(t *testing.T) {
	dir, fx := fixture(t)
	_ = os.Remove(filepath.Join(dir, IndexFile))
	if c := validate(t, dir, fx)["index_md"]; c.OK {
		t.Fatal("missing index.md must fail")
	}
}

// A confirmed row must point at a fact the ledger actually verified: five
// well-written fields are prose, not proof (D55).
func TestValidateRejectsConfirmedUnverifiedFact(t *testing.T) {
	dir, fx := fixture(t)
	fx.db.Facts[0].Status = "unverified"
	c := validate(t, dir, fx)["confirmed_verified"]
	if c.OK {
		t.Fatal("confirmed row over an unverified fact must fail")
	}
	if !strings.Contains(c.Detail, "f_1") {
		t.Fatalf("detail = %q", c.Detail)
	}
}

func TestValidateCatchesThinConfirmedFinding(t *testing.T) {
	dir, fx := fixture(t)
	body := `[{"fact":"f_1","verdict":"confirmed","title":"x","location":"http://t/a",
		"preconditions":"","impact":"","trace":"","evidence":["evidence/e_1-admin.html"]}]`
	_ = os.WriteFile(filepath.Join(dir, Findings), []byte(body), 0o644)
	c := validate(t, dir, fx)["confirmed_fields"]
	if c.OK || !strings.Contains(c.Detail, "impact") {
		t.Fatalf("thin confirmed must fail with the missing fields, got %+v", c)
	}
}

func TestValidateCatchesEvidenceEscape(t *testing.T) {
	dir, fx := fixture(t)
	body := `[{"fact":"f_1","verdict":"confirmed","title":"x","location":"a","preconditions":"b",
		"impact":"c","trace":"d","evidence":["../../etc/passwd"]}]`
	_ = os.WriteFile(filepath.Join(dir, Findings), []byte(body), 0o644)
	if c := validate(t, dir, fx)["evidence_inside"]; c.OK {
		t.Fatal("evidence outside the report dir must fail")
	}
}

// findings.json is model-written and `fact` becomes a file name when the page is
// read. `../..` used to make rule 8 read outside the report directory and turn
// the check into a filesystem probe.
func TestValidateCatchesFindingPageEscape(t *testing.T) {
	dir, fx := fixture(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.md")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `[{"fact":"../../outside","verdict":"confirmed","title":"x","location":"a",
		"preconditions":"b","impact":"c","trace":"d","evidence":[]}]`
	if err := os.WriteFile(filepath.Join(dir, Findings), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := validate(t, dir, fx)["finding_pages"]; c.OK {
		t.Fatal("a finding page outside the report dir must fail the check")
	}
}

func TestValidateCatchesEvidenceOwnedByAnotherFact(t *testing.T) {
	dir, fx := fixture(t)
	// A path that exists but was copied for a different evidence id.
	_ = os.WriteFile(filepath.Join(dir, EvidenceDir, "e_9-admin.html"), []byte("x"), 0o644)
	body := `[{"fact":"f_1","verdict":"confirmed","title":"x","location":"a","preconditions":"b",
		"impact":"c","trace":"d","evidence":["evidence/e_9-admin.html"]}]`
	_ = os.WriteFile(filepath.Join(dir, Findings), []byte(body), 0o644)
	c := validate(t, dir, fx)["evidence_inside"]
	if c.OK {
		t.Fatal("evidence belonging to another fact must fail")
	}
}

func TestValidateCatchesUnlinkedFindingPage(t *testing.T) {
	dir, fx := fixture(t)
	_ = os.WriteFile(filepath.Join(dir, FindingsDir, "f_1.md"), []byte("# /admin 越权\n"), 0o644)
	c := validate(t, dir, fx)["finding_pages"]
	if c.OK {
		t.Fatal("a finding page without its evidence link must fail")
	}
}

func TestValidateCatchesStaleEvidence(t *testing.T) {
	dir, fx := fixture(t)
	fx.db.Facts[0].CreatedAt = time.Now().Add(-60 * 24 * time.Hour)
	c := validate(t, dir, fx)["freshness"]
	if c.OK {
		t.Fatal("evidence older than stale_after must fail")
	}
}

// A re-test is the exit from staleness. UpdateFact keeps CreatedAt, so the only
// place a successful re-run is recorded is the review row; without reading it,
// an old fact stayed stale forever even after today's pass.
func TestRecentPassMakesAnOldFactFresh(t *testing.T) {
	dir, fx := fixture(t)
	fx.db.Facts[0].CreatedAt = time.Now().Add(-60 * 24 * time.Hour)
	fx.db.Reviews = []model.Review{{
		ID: "rv_1", FactID: "f_1", Verdict: "pass", CreatedAt: time.Now(),
	}}
	if c := validate(t, dir, fx)["freshness"]; !c.OK {
		t.Fatalf("a fact re-tested today must not be stale: %+v", c)
	}
}

// needs_evidence is the verifier saying it could not reproduce the finding; it
// is not a re-test, so it must not move the freshness clock.
func TestNeedsEvidenceDoesNotRefreshAFact(t *testing.T) {
	dir, fx := fixture(t)
	fx.db.Facts[0].CreatedAt = time.Now().Add(-60 * 24 * time.Hour)
	fx.db.Reviews = []model.Review{{
		ID: "rv_1", FactID: "f_1", Verdict: "needs_evidence", CreatedAt: time.Now(),
	}}
	if c := validate(t, dir, fx)["freshness"]; c.OK {
		t.Fatal("needs_evidence is not a successful re-test")
	}
}

func TestValidateCatchesMissingFindingRow(t *testing.T) {
	dir, fx := fixture(t)
	_ = os.WriteFile(filepath.Join(dir, Findings), []byte(`[]`), 0o644)
	c := validate(t, dir, fx)["findings_cover_reportable"]
	if c.OK || !strings.Contains(c.Detail, "f_1") {
		t.Fatalf("a reportable fact with no row must fail, got %+v", c)
	}
}

func TestValidateCatchesRowForUnknownFact(t *testing.T) {
	dir, fx := fixture(t)
	// A row pointing at an id nobody recorded: ownership is skipped for facts
	// that do not resolve, so this used to pass every other rule.
	body := `[{"fact":"f_1","verdict":"confirmed","title":"x","location":"a","preconditions":"b",
		"impact":"c","trace":"d","evidence":[]},
		{"fact":"f_404","verdict":"candidate","title":"凭空"}]`
	_ = os.WriteFile(filepath.Join(dir, Findings), []byte(body), 0o644)
	c := validate(t, dir, fx)["fact_exists"]
	if c.OK || !strings.Contains(c.Detail, "f_404") {
		t.Fatalf("a row for an unrecorded fact must fail, got %+v", c)
	}
}

func TestValidateCatchesRowForRejectedFact(t *testing.T) {
	dir, fx := fixture(t)
	fx.db.Facts[0].Status = "rejected"
	// The fact is no longer reportable, so its row must go too.
	c := validate(t, dir, fx)["fact_exists"]
	if c.OK {
		t.Fatal("a rejected fact must not keep a finding row")
	}
}

func TestValidateOnlyAcceptsTwoVerdicts(t *testing.T) {
	dir, fx := fixture(t)
	for _, verdict := range []string{"dismissed", "inconclusive"} {
		body := `[{"fact":"f_1","verdict":"` + verdict + `","title":"x"}]`
		_ = os.WriteFile(filepath.Join(dir, Findings), []byte(body), 0o644)
		c := validate(t, dir, fx)["verdict_enum"]
		if c.OK {
			t.Fatalf("verdict %q must be refused (D41)", verdict)
		}
	}
}

// A fact whose support never became bytes still has to reach the writer: the
// snapshot carries its refs so "we saw it there, we cannot replay it" can appear
// under scope and limitations (D44).
func TestSnapshotCarriesRefsWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	db := store.DB{Facts: []model.Fact{{
		ID: "f_2", IntentID: "in_1", Kind: "vuln", Title: "只在线上", Status: "unverified",
		Refs: []model.Ref{{Scheme: model.RefURL, Locator: "https://target/admin"}},
	}}}
	if err := writeSnapshot(dir, db, map[string][]string{}, "10.0.0.0/24", nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("snapshot is not json: %v", err)
	}
	if len(s.Facts) != 1 || len(s.Facts[0].Refs) != 1 {
		t.Fatalf("snapshot = %+v", s.Facts)
	}
	if got := s.Facts[0].Refs[0]; got.Scheme != model.RefURL || got.Locator != "https://target/admin" {
		t.Fatalf("ref = %+v", got)
	}
	if len(s.Facts[0].Evidence) != 0 {
		t.Fatalf("a ref must not arrive as evidence: %+v", s.Facts[0].Evidence)
	}
	if s.Facts[0].Status != "unverified" {
		t.Fatalf("status = %q", s.Facts[0].Status)
	}
}

// The snapshot must carry each fact's latest review and the rejected
// conclusions: §4 needs the verifier's reason a candidate has not passed, and
// §5.2 needs the negative reason of each rejected conclusion. Before D105 the
// prompt asked for both while the snapshot carried neither, so the writer had
// to invent or omit them.
func TestSnapshotCarriesReviewAndRejected(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	db := store.DB{
		Facts: []model.Fact{
			{ID: "f_1", Kind: "vuln", Title: "待复核的越权", Status: "unverified"},
			{ID: "f_2", Kind: "cred", Title: "默认口令不成立", Summary: "口令已改", Status: "rejected"},
			{ID: "f_3", Kind: "vuln", Title: "已复核的注入", Status: "verified"},
		},
		Reviews: []model.Review{
			// Out of write order on purpose: the newest verdict must win.
			{ID: "rv_new", FactID: "f_1", Verdict: "needs_evidence",
				Notes: "缺少可读回显，无法判定", VerifierRun: "r_2", CreatedAt: now},
			{ID: "rv_old", FactID: "f_1", Verdict: "pass",
				Notes: "旧结论", VerifierRun: "r_1", CreatedAt: now.Add(-time.Hour)},
			{ID: "rv_rej", FactID: "f_2", Verdict: "reject",
				Notes: "口令在 10 月已轮换，重放失败", VerifierRun: "r_3", CreatedAt: now},
			{ID: "rv_pass", FactID: "f_3", Verdict: "pass",
				Notes: "重跑注入成功", VerifierRun: "r_4", CreatedAt: now},
		},
	}
	if err := writeSnapshot(dir, db, map[string][]string{}, "", nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("snapshot is not json: %v", err)
	}

	// Rejected conclusion: no fact row, present in rejected with its reason.
	if len(s.Facts) != 2 {
		t.Fatalf("facts = %+v, want only the reportable ones", s.Facts)
	}
	var f1, f3 *factView
	for i := range s.Facts {
		switch s.Facts[i].ID {
		case "f_1":
			f1 = &s.Facts[i]
		case "f_3":
			f3 = &s.Facts[i]
		}
	}
	if f1 == nil || f1.Review == nil || f1.Review.Verdict != "needs_evidence" ||
		f1.Review.Notes != "缺少可读回显，无法判定" || f1.Review.VerifierRun != "r_2" {
		t.Fatalf("f_1 review = %+v, want the newest needs_evidence verdict", f1)
	}
	if f3 == nil || f3.Review == nil || f3.Review.Verdict != "pass" {
		t.Fatalf("f_3 review = %+v", f3)
	}
	if len(s.Rejected) != 1 {
		t.Fatalf("rejected = %+v, want one item", s.Rejected)
	}
	rej := s.Rejected[0]
	if rej.ID != "f_2" || rej.Kind != "cred" || rej.Title != "默认口令不成立" || rej.Summary != "口令已改" {
		t.Fatalf("rejected item = %+v", rej)
	}
	if rej.Review == nil || rej.Review.Verdict != "reject" || rej.Review.Notes != "口令在 10 月已轮换，重放失败" {
		t.Fatalf("rejected review = %+v", rej.Review)
	}
}

// The prompt and the snapshot are one contract: the prompt may only ask the
// writer to use review/rejected if the snapshot really carries them.
func TestBuildPromptCarriesReviewAndRejected(t *testing.T) {
	p := BuildPrompt(2, "")
	for _, want := range []string{"review", "rejected", "5.2"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

// The report must not be built around a missing original: the deliverable's
// promise is that every conclusion opens its proof, and a silently thinner
// report looks complete while quietly being less (docs/08).
// Preflight refuses before a model call, so the operator gets a 409 instead of
// a report that silently lost a proof (D45).
func TestPreflightRefusesMissingEvidence(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.AddEvidence(model.Evidence{RunID: "r1", IntentID: "in_1", Name: "gone.html", URI: "in_1/gone.html"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "t", Status: "unverified", EvidenceIDs: []string{e.ID}})
	if err != nil {
		t.Fatal(err)
	}

	g := &Generator{Store: st, CaseRoot: root}
	err = g.Preflight()
	if err == nil {
		t.Fatal("preflight must refuse when an original is gone")
	}
	if !strings.Contains(err.Error(), "in_1/gone.html") || !strings.Contains(err.Error(), fact.ID) {
		t.Fatalf("error must name the file and the fact: %v", err)
	}

	// With the file back, the same preflight passes: the check is about bytes,
	// not about policy.
	if err := os.MkdirAll(filepath.Join(root, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "in_1", "gone.html"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Preflight(); err != nil {
		t.Fatalf("preflight with the file restored: %v", err)
	}
}

func TestCopyEvidenceFailsWhenAnOriginalIsGone(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	fact := model.Fact{ID: "f_1", Kind: "vuln", Title: "t", EvidenceIDs: []string{"e_1"}}
	db := store.DB{
		Facts:    []model.Fact{fact},
		Evidence: []model.Evidence{{ID: "e_1", URI: "in_1/gone.html"}},
	}
	g := &Generator{CaseRoot: root}
	_, err := g.copyEvidence(dir, db, []model.Fact{fact})
	if err == nil {
		t.Fatal("a missing original must fail the report")
	}
	if !strings.Contains(err.Error(), "in_1/gone.html") || !strings.Contains(err.Error(), "f_1") {
		t.Fatalf("error must name the file and the fact: %v", err)
	}
	// Nothing half-copied is left behind for the model to cite.
	entries, _ := os.ReadDir(filepath.Join(dir, EvidenceDir))
	if len(entries) != 0 {
		t.Fatalf("report evidence dir is not empty: %v", entries)
	}
}

func TestCopyEvidenceCopiesTheOriginal(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "in_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "in_1", "a.html"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	fact := model.Fact{ID: "f_1", Kind: "vuln", Title: "t", EvidenceIDs: []string{"e_1"}}
	db := store.DB{Evidence: []model.Evidence{{ID: "e_1", URI: "in_1/a.html"}}}
	g := &Generator{CaseRoot: root}
	got, err := g.copyEvidence(dir, db, []model.Fact{fact})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["f_1"]) != 1 || got["f_1"][0] != EvidenceDir+"/e_1-a.html" {
		t.Fatalf("copied = %v", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, EvidenceDir, "e_1-a.html")); err != nil || string(b) != "proof" {
		t.Fatalf("copy = %q err=%v", b, err)
	}
}

func TestSafePathRefusesEscape(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"../x", "../../etc/passwd", "/abs/path", ""} {
		if _, ok := SafePath(dir, rel); ok {
			t.Fatalf("SafePath(%q) must be refused", rel)
		}
	}
	if _, ok := SafePath(dir, "index.md"); !ok {
		t.Fatal("a plain relative path must be accepted")
	}
}

func TestReadMetaRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, MetaFile), []byte("{not json"), 0o644)
	if _, err := ReadMeta(dir); err == nil {
		t.Fatal("broken report.json must not parse")
	}
	_ = os.WriteFile(filepath.Join(dir, MetaFile), mustJSON(t, Meta{ID: "rp_1", Status: StatusReady}), 0o644)
	m, err := ReadMeta(dir)
	if err != nil || m.ID != "rp_1" || m.Status != StatusReady {
		t.Fatalf("meta = %+v err = %v", m, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The outline is the skill's job (D92): the prompt carries the material, the
// three artifacts and the hard invariants, then points at tianwu-report instead
// of restating the sections. In particular the old "覆盖表" line was a leftover
// of the design D30 removed; the prompt must not reintroduce it.
func TestBuildPromptDefersOutlineToSkill(t *testing.T) {
	p := BuildPrompt(2, "https://example.test")
	for _, want := range []string{"tianwu-report", Snapshot, Findings, IndexFile, FindingsDir, "不增、不减、不换序", "status=verified", "refs"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "覆盖表") {
		t.Errorf("prompt must not ask for a coverage table (D30):\n%s", p)
	}
}
