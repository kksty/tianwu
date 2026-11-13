package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaimOnlyFromPendingOrFailed(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "内存马"})
	if err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.ClaimIntent(in.ID, "local")
	if err != nil || !ok {
		t.Fatalf("pending must be claimable: ok=%v err=%v", ok, err)
	}
	if got.Attempts != 1 || got.Status != model.StatusClaimed {
		t.Fatalf("claim: attempts=%d status=%s", got.Attempts, got.Status)
	}

	if _, ok, _ := s.ClaimIntent(in.ID, "local"); ok {
		t.Fatal("claimed intent must not be claimable twice")
	}

	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusParked }); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ClaimIntent(in.ID, "local"); ok {
		t.Fatal("parked intent must not be claimable without reopen")
	}

	reopened, err := s.ReopenIntent(in.ID)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Status != model.StatusPending || reopened.ParkReason != "" {
		t.Fatalf("reopen result: %+v", reopened)
	}
	if _, ok, _ := s.ClaimIntent(in.ID, "local"); !ok {
		t.Fatal("reopened intent must be claimable")
	}
}

func TestReopenRequiresParked(t *testing.T) {
	s := open(t)
	in, _ := s.CreateIntent(model.Intent{Title: "x"})
	if _, err := s.ReopenIntent(in.ID); err == nil {
		t.Fatal("reopening a pending intent must fail")
	}
}

func TestUpdateFactKeepsIdentityAndVersionsTheClaim(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "/admin 越权", Summary: "普通用户可读"})
	if err != nil {
		t.Fatal(err)
	}
	if f.ClaimVersion != 1 || f.ClaimHash == "" {
		t.Fatalf("new fact claim version = %d hash=%q", f.ClaimVersion, f.ClaimHash)
	}
	// Bookkeeping and display fields are not the claim: the version must hold.
	if _, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Title = "/admin 越权（改标题）" }); err != nil {
		t.Fatal(err)
	}
	kept, _ := s.FactByID(f.ID)
	if kept.ClaimVersion != 1 || kept.Title != "/admin 越权（改标题）" {
		t.Fatalf("display edit changed the claim version: %+v", kept)
	}
	updated, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Summary = "补了证据" })
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != f.ID || !updated.CreatedAt.Equal(f.CreatedAt) || updated.Summary != "补了证据" {
		t.Fatalf("update = %+v (original %+v)", updated, f)
	}
	if updated.ClaimVersion != 2 || updated.Status != "unverified" {
		t.Fatalf("changed claim = v%d status=%q, want v2 unverified", updated.ClaimVersion, updated.Status)
	}
}

// Declaration identity comes from the writer: a source ref outranks the
// declaration id, a line that later gains one adopts its own row, and two
// different declarations never merge because their titles match.
func TestFindDeclarationUsesWriterIdentity(t *testing.T) {
	s := open(t)
	first, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "同名结论", SourceRef: "report:r1#AUD-001",
		DeclarationID: "obs-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "同名结论", SourceRef: "report:r1#AUD-002",
		DeclarationID: "obs-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two source refs must be two facts")
	}
	if got, ok := s.FindDeclaration("in_1", "report:r1#AUD-002", ""); !ok || got.ID != second.ID {
		t.Fatalf("source-ref match = %+v, %v; want %s", got, ok, second.ID)
	}
	if _, ok := s.FindDeclaration("in_1", "", ""); ok {
		t.Fatal("a declaration without any identity must not resolve")
	}

	// Same title, different declaration ids: two observations, not one.
	plainA, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "先声明后导入", DeclarationID: "obs-1"})
	if err != nil {
		t.Fatal(err)
	}
	plainB, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "先声明后导入", DeclarationID: "obs-2"})
	if err != nil {
		t.Fatal(err)
	}
	if plainA.ID == plainB.ID {
		t.Fatal("different declarations with one title merged")
	}
	// The report import later supplies a source ref for obs-1: it adopts the
	// row recorded for that declaration instead of adding a second fact.
	if got, ok := s.FindDeclaration("in_1", "report:r1#AUD-003", "obs-1"); !ok || got.ID != plainA.ID {
		t.Fatalf("accepted row was not adopted: %+v %v", got, ok)
	}
	if got, ok := s.FindDeclaration("in_1", "", "obs-2"); !ok || got.ID != plainB.ID {
		t.Fatalf("declaration id match = %+v, %v", got, ok)
	}
}

func TestAddFactDefaultsToLeastTrustedOrigin(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "note", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Origin != model.OriginHeuristic {
		t.Fatalf("default origin = %q, want %q", f.Origin, model.OriginHeuristic)
	}
	if f.Status != "unverified" || f.Confidence != "observed" {
		t.Fatalf("defaults: status=%s confidence=%s", f.Status, f.Confidence)
	}
}

func TestPinGoalRequiresRealEvidenceAndReason(t *testing.T) {
	s := open(t)
	g, err := s.AddGoal(model.Goal{Statement: "拿到管理员权限", QuantifiedTarget: "至少一条 verified vault"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Met {
		t.Fatal("new goal must start unmet")
	}
	if _, err := s.PinGoal(g.ID, "   ", "范围已覆盖", nil); err == nil {
		t.Fatal("pinning without evidence must fail")
	}
	if _, err := s.PinGoal(g.ID, "e_does_not_exist", "范围已覆盖", nil); err == nil {
		t.Fatal("pinning to unknown evidence must fail")
	}
	ev, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinGoal(g.ID, ev.ID, "   ", nil); err == nil {
		t.Fatal("pinning without a written reason must fail: the page would have nothing to show")
	}
	f1, err := s.AddFact(model.Fact{Title: "first", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := s.AddFact(model.Fact{Title: "second", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.PinGoal(g.ID, ev.ID, "  复核通过，证据覆盖目标  ",
		[]string{f1.ID, " ", f1.ID, f2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Met || pinned.EvidenceID != ev.ID {
		t.Fatalf("pinned goal: %+v", pinned)
	}
	if pinned.Note != "复核通过，证据覆盖目标" {
		t.Fatalf("note = %q, want the trimmed reason", pinned.Note)
	}
	if pinned.CompletedAt == nil {
		t.Fatal("completed_at must be stamped with the pin")
	}
	if got := pinned.From; len(got) != 2 || got[0] != f1.ID || got[1] != f2.ID {
		t.Fatalf("from = %v, want the de-duplicated citation list in order", got)
	}
}

// A wrong stamp has to be reversible: the goal returns to the open list and the
// receipt leaves with the stamp, because a reason next to "未达成" would read as
// if the goal were still met (D98).
func TestReopenGoalWithdrawsTheStamp(t *testing.T) {
	s := open(t)
	ev, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.AddGoal(model.Goal{Statement: "拿到 shell"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenGoal(g.ID); err == nil {
		t.Fatal("an open goal has no stamp to withdraw")
	}
	if _, err := s.ReopenGoal("g_missing"); err == nil {
		t.Fatal("reopening an unknown goal must fail")
	}
	f, err := s.AddFact(model.Fact{Title: "support", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinGoal(g.ID, ev.ID, "已验证", []string{f.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReopenGoal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Met || got.EvidenceID != "" || got.Note != "" || len(got.From) != 0 || got.CompletedAt != nil {
		t.Fatalf("reopened goal kept part of the receipt: %+v", got)
	}
	if !got.CreatedAt.Equal(g.CreatedAt) {
		t.Fatalf("reopen rewrote created_at: %+v", got)
	}
}

func TestSearchFactsByKeywordAndKind(t *testing.T) {
	s := open(t)
	mustFact(t, s, model.Fact{IntentID: "in_1", Kind: "vuln", Title: "/admin 越权", Summary: "普通用户可读全部订单"})
	mustFact(t, s, model.Fact{IntentID: "in_1", Kind: "host", Title: "10.0.0.5", Doc: "tomcat 9.0.31"})
	mustFact(t, s, model.Fact{IntentID: "in_1", Kind: "service", Title: "8080 http", Summary: "Apache-Coyote"})

	if got := s.SearchFacts("ADMIN", "", 10); len(got) != 1 {
		t.Fatalf("keyword search: want 1, got %d", len(got))
	}
	if got := s.SearchFacts("tomcat", "", 10); len(got) != 1 {
		t.Fatalf("doc search: want 1, got %d", len(got))
	}
	if got := s.SearchFacts("", "host", 10); len(got) != 1 {
		t.Fatalf("kind filter: want 1, got %d", len(got))
	}
	if got := s.SearchFacts("admin", "host", 10); len(got) != 0 {
		t.Fatalf("combined filter: want 0, got %d", len(got))
	}
	if got := s.SearchFacts("", "", 2); len(got) != 2 {
		t.Fatalf("limit: want 2, got %d", len(got))
	}
}

func mustFact(t *testing.T, s *Store, f model.Fact) {
	t.Helper()
	if _, err := s.AddFact(f); err != nil {
		t.Fatal(err)
	}
}

// The two writers are not equal: a commander nomination only creates a
// candidate or refines one, while an executor declaration (a run that used the
// tactic) confirms, overwrites and revives the same row (D70).
func TestPlaybookWritersHaveDifferentRanks(t *testing.T) {
	s := open(t)
	cand, changed, err := s.UpsertPlaybook(model.Playbook{
		Title: "ThinkPHP RCE", When: "5.0.x 可写日志时可用", Doc: "curl /index.php?s=",
		Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate,
		FromFactIDs: []string{"f_1"},
	})
	if err != nil || !changed {
		t.Fatalf("nominate: changed=%v err=%v", changed, err)
	}
	if cand.Status != model.PlaybookCandidate || cand.Origin != model.PlaybookFromCommander {
		t.Fatalf("candidate = %+v", cand)
	}

	conf, changed, err := s.UpsertPlaybook(model.Playbook{
		Title: "thinkphp rce", When: "5.0.x 且日志可写", Doc: "curl -s 'http://T/index.php?s=...'",
		Tags:   []string{"product:thinkphp"},
		Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed, IntentID: "in_1",
	})
	if err != nil || !changed {
		t.Fatalf("confirm: changed=%v err=%v", changed, err)
	}
	if conf.ID != cand.ID {
		t.Fatalf("case-insensitive title must dedupe: %s vs %s", conf.ID, cand.ID)
	}
	if conf.Status != model.PlaybookConfirmed || conf.Origin != model.PlaybookFromExecutor || conf.IntentID != "in_1" {
		t.Fatalf("promoted row = %+v", conf)
	}
	if conf.When != "5.0.x 且日志可写" || len(conf.Tags) != 1 {
		t.Fatalf("executor content did not win: %+v", conf)
	}

	again, changed, err := s.UpsertPlaybook(model.Playbook{
		Title: "ThinkPHP RCE", When: "别写这个", Origin: model.PlaybookFromCommander,
	})
	if err != nil || changed {
		t.Fatalf("nomination over a confirmed row must be a no-op: changed=%v err=%v", changed, err)
	}
	if again.When != "5.0.x 且日志可写" {
		t.Fatalf("commander rewrote confirmed content: %+v", again)
	}

	retired, err := s.RetirePlaybook(conf.ID)
	if err != nil || retired.Status != model.PlaybookRetired {
		t.Fatalf("retire: %+v err=%v", retired, err)
	}
	revived, changed, err := s.UpsertPlaybook(model.Playbook{
		Title: "ThinkPHP RCE", When: "5.0.x 且日志可写", Origin: model.PlaybookFromExecutor,
	})
	if err != nil || !changed || revived.Status != model.PlaybookConfirmed {
		t.Fatalf("a live run must revive a retired row: %+v changed=%v err=%v", revived, changed, err)
	}
}

// An empty ledger file is corruption, not a new project. Treating it as empty
// let the next write overwrite the only copy and silently zero the engagement.
func TestOpenRejectsAnEmptyLedgerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	if err := os.WriteFile(path, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("an existing empty ledger must be an error, not a fresh start")
	}
	// Nothing was written over it.
	if b, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatalf("Open modified the corrupt file: %q %v", b, err)
	}
}

func TestOpenRejectsNullLedgerWithoutModifyingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	contents := []byte(" \nnull\n")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("a null ledger must not be accepted as a new empty database")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(contents) {
		t.Fatalf("corrupt ledger changed: %q, %v", got, err)
	}
}

// A snapshot is taken while runs keep writing (a dispatch builds its prompt
// from it). It must not alias the live rows: in-place updates would otherwise
// change a prompt or a report after it was frozen, and read/write races would
// follow. This is the unit-level proof that the copy is detached.
// The harness reports the window with the first usage event; the live run must
// carry it before it settles so the console pressure bar has a denominator
// (D80).
func TestProgressWindowIsVisibleWhileTheRunIsLive(t *testing.T) {
	s := open(t)
	run, err := s.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	s.ProgressWindow(run.ID, 1000000)
	if got := s.Runs()[0].ContextWindow; got != 1000000 {
		t.Fatalf("context window = %d, want 1000000", got)
	}
}

func TestSnapshotDoesNotAliasLiveRows(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "note", Title: "旧标题"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "旧意图"})
	if err != nil {
		t.Fatal(err)
	}

	db := s.Snapshot()
	if _, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Title = "新标题" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(x *model.Intent) { x.Title = "新意图" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "note", Title: "追加"}); err != nil {
		t.Fatal(err)
	}

	if len(db.Facts) != 1 || db.Facts[0].Title != "旧标题" {
		t.Fatalf("snapshot changed after the ledger moved on: %+v", db.Facts)
	}
	if len(db.Intents) != 1 || db.Intents[0].Title != "旧意图" {
		t.Fatalf("snapshot intent changed after update: %+v", db.Intents)
	}
}

func TestPlaybookRejectsEmptyTitle(t *testing.T) {
	s := open(t)
	if _, _, err := s.UpsertPlaybook(model.Playbook{Title: "  ", When: "x"}); err == nil {
		t.Fatal("a playbook without a title must be refused")
	}
}

func TestOpenRefusesUnsupportedSQLiteSchema(t *testing.T) {
	for _, version := range []int{10, 99} {
		s := open(t)
		if _, err := s.sqlite.db.Exec("UPDATE meta SET payload=json_set(payload,'$.version',?)", version); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(s.Path()); err == nil || !strings.Contains(err.Error(), "unsupported ledger schema") {
			t.Fatalf("unsupported version accepted: %v", err)
		}
		var got int
		if err := s.sqlite.db.QueryRow("SELECT json_extract(payload,'$.version') FROM meta").Scan(&got); err != nil || got != version {
			t.Fatal("unsupported ledger rewritten")
		}
	}
}
