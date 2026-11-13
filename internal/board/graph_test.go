package board

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/ledger"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The executor's snapshot is raw facts, not the board's port aggregate: same
// port is not a family, and the program must not hand the agent a grouping it
// would otherwise have to judge (D54). Nor does it derive a target.
func TestGraphKeepsRawFactsWithoutPortsOrTargets(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	db := store.DB{
		Intents: []model.Intent{
			{
				ID: "in_run", Title: "打 10.0.0.5:443", Description: "试 CVE-X",
				Status: model.StatusRunning, RunID: "r_1", Node: "local",
				FromFactIDs: []string{"f_svc1"}, CreatedAt: now,
			},
			{ID: "in_pending", Title: "打 10.0.0.6:443", Status: model.StatusPending, CreatedAt: now},
			{ID: "in_done", Title: "扫端口", Status: model.StatusDone, CreatedAt: now},
		},
		Runs: []model.Run{{
			ID: "r_1", IntentID: "in_run", Node: "local",
			Status: model.RunRunning, StartedAt: now,
		}},
		Facts: []model.Fact{
			{
				ID: "f_svc1", Kind: "service", Title: "10.0.0.5:443", Summary: "nginx",
				Status: "unverified", Confidence: "observed", Origin: model.OriginDeclared,
				IntentID: "in_run", CreatedAt: now,
			},
			{
				ID: "f_svc2", Kind: "service", Title: "10.0.0.6:443",
				Status: "unverified", Confidence: "observed", IntentID: "in_done",
				CreatedAt: now.Add(time.Minute),
			},
			{
				ID: "f_note", Kind: "note", Title: "提问：要不要横向",
				Summary: "同段还有 10.0.0.7", IntentID: "in_run", CreatedAt: now,
			},
			{
				ID: "f_cred", Kind: "cred", Title: "admin", Summary: "password",
				Doc:    "curl -u admin:password http://10.0.0.5:443/admin -> 200",
				Status: "unverified", EvidenceIDs: []string{"e_1"}, CreatedAt: now,
			},
		},
		Evidence: []model.Evidence{{ID: "e_1", URI: "in_done/creds.txt"}},
		Goals: []model.Goal{{
			ID: "g_1", Statement: "拿到管理员权限", QuantifiedTarget: "至少一条 verified",
		}},
	}

	g := BuildGraph(db, "10.0.0.0/24")
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, forbidden := range []string{`"ports"`, `"port"`, `"targets"`, `"family"`, `"families"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("snapshot carries %s: %s", forbidden, raw)
		}
	}

	// Notes are lifted out of facts; every other kind stays a row.
	if len(g.Facts) != 3 {
		t.Fatalf("facts = %d, want 3 (note excluded): %s", len(g.Facts), raw)
	}
	if len(g.Notes) != 1 || g.Notes[0].ID != "f_note" {
		t.Fatalf("notes = %+v", g.Notes)
	}

	var cred *GraphFact
	for i := range g.Facts {
		if g.Facts[i].Kind == "cred" {
			cred = &g.Facts[i]
		}
	}
	if cred == nil || len(cred.Evidence) != 1 || cred.Evidence[0] != "../in_done/creds.txt" {
		t.Fatalf("evidence URI must be case-dir relative: %+v", cred)
	}
	// The snapshot carries the summary layer, not the doc: the full text stays in
	// the ledger and is read by id (docs/17 §3.1).
	if strings.Contains(raw, "curl -u admin") || strings.Contains(raw, `"doc"`) {
		t.Fatalf("snapshot carries the fact doc body: %s", raw)
	}
	if cred == nil || cred.Summary != "password" {
		t.Fatalf("fact summary must reach the executor: %+v", cred)
	}
	if g.EvidenceBase != "case_dir" {
		t.Fatalf("evidence base = %q", g.EvidenceBase)
	}

	if len(g.Intents.Running) != 1 {
		t.Fatalf("running = %+v", g.Intents.Running)
	}
	run := g.Intents.Running[0]
	if run.Run != "r_1" || run.Description != "试 CVE-X" || len(run.FromFacts) != 1 {
		t.Fatalf("running intent lost the prose that says what it is doing: %+v", run)
	}
	if len(g.Intents.Done) != 1 || g.Intents.Done[0].Description != "" {
		t.Fatalf("done intents are titles only: %+v", g.Intents.Done)
	}
	if g.Totals.Running != 1 || g.Totals.Open != 1 || g.Totals.Done != 1 {
		t.Fatalf("totals = %+v", g.Totals)
	}
}

// A cut must be visible: the totals say how many existed, the flag says the
// list is short.
func TestGraphReportsTruncation(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	db := store.DB{}
	for i := 0; i < 40; i++ {
		db.Facts = append(db.Facts, model.Fact{
			ID:   "f_" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Kind: "vuln", Title: "一条很长的结论 " + strings.Repeat("x", 20),
			Summary: strings.Repeat("细节", 100), CreatedAt: now,
		})
	}
	for i := 0; i < 20; i++ {
		db.Facts = append(db.Facts, model.Fact{
			ID: "fn_" + string(rune('a'+i)), Kind: "note",
			Title: "备注 " + string(rune('a'+i)), Summary: strings.Repeat("注", 300),
			CreatedAt: now,
		})
	}

	g := buildGraph(db, "", now, 600, 300)
	if !g.Truncated.Facts {
		t.Fatalf("facts budget did not cut: %d rows", len(g.Facts))
	}
	if len(g.Facts) >= g.Totals.Facts {
		t.Fatalf("kept %d of %d facts but claimed truncation", len(g.Facts), g.Totals.Facts)
	}
	if !g.Truncated.Notes {
		t.Fatalf("notes budget did not cut: %d rows", len(g.Notes))
	}
	if len(g.Notes) >= g.Totals.Notes {
		t.Fatalf("kept %d of %d notes but claimed truncation", len(g.Notes), g.Totals.Notes)
	}
}

func TestGraphIsDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	db := store.DB{
		Facts: []model.Fact{
			{ID: "f_a", Kind: "service", Title: "10.0.0.5:80", CreatedAt: now},
			{ID: "f_b", Kind: "host", Title: "10.0.0.5", CreatedAt: now},
			{ID: "f_c", Kind: "note", Title: "备注", CreatedAt: now},
		},
		Intents: []model.Intent{
			{ID: "in_b", Title: "b", Status: model.StatusPending},
			{ID: "in_a", Title: "a", Status: model.StatusRunning, RunID: "r_1"},
		},
	}
	b1, _ := json.Marshal(buildGraph(db, "", now, graphFactsBudget, graphNotesBudget))
	b2, _ := json.Marshal(buildGraph(db, "", now, graphFactsBudget, graphNotesBudget))
	if !bytes.Equal(b1, b2) {
		t.Fatalf("two snapshots of the same ledger differ:\n%s\n%s", b1, b2)
	}
	// Hosts come before services in retention order, and intents stay in
	// creation order (newest first), not map order.
	if !strings.Contains(string(b1), `"f_b","kind":"host"`) {
		t.Fatalf("facts not sorted by retention rank: %s", b1)
	}
}

// The snapshot's evidence paths and the handoff's evidence paths must be one
// convention: the dispatch prompt says a graph path can be copied verbatim into
// facts.jsonl. If BuildGraph's spelling were refused by ledger.ReadDeclared the
// whole fact line would be dropped — that is exactly the "路径越界" warning this
// test guards against (D113).
func TestGraphEvidencePathsAreDeclarable(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	casesRoot := t.TempDir()
	proof := filepath.Join(casesRoot, "in_a", "raw", "proof.hdr")
	if err := os.MkdirAll(filepath.Dir(proof), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proof, []byte("HTTP/1.1 200 OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(casesRoot, "in_b")
	if err := os.MkdirAll(filepath.Join(workDir, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := store.DB{
		Intents: []model.Intent{
			{ID: "in_a", Title: "旧方向", Status: model.StatusDone},
			{ID: "in_b", Title: "新方向", Status: model.StatusRunning},
		},
		Facts: []model.Fact{{
			ID: "f_old", Kind: "vuln", Title: "上一轮的结论", IntentID: "in_a",
			EvidenceIDs: []string{"e_1"}, CreatedAt: now,
		}},
		Evidence: []model.Evidence{{ID: "e_1", URI: "in_a/raw/proof.hdr"}},
	}
	g := BuildGraph(db, "")

	var emitted string
	for _, f := range g.Facts {
		if f.ID == "f_old" && len(f.Evidence) > 0 {
			emitted = f.Evidence[0]
		}
	}
	if emitted == "" {
		t.Fatalf("snapshot carries no evidence path for f_old: %+v", g.Facts)
	}

	// Copy the snapshot's spelling into a declaration, as the prompt allows.
	quoted, err := json.Marshal(emitted)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"kind":"note","title":"复用旧证据","evidence":[` + string(quoted) + `]}` + "\n"
	if err := os.WriteFile(filepath.Join(workDir, ".tw", "facts.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	decl, err := ledger.ReadDeclaredSince(casesRoot, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(decl.Facts) != 1 || len(decl.Facts[0].Evidence) != 1 || decl.Facts[0].Evidence[0] != emitted {
		t.Fatalf("snapshot path %q was not accepted as a declaration: %+v", emitted, decl)
	}
	if len(decl.Warnings) != 0 {
		t.Fatalf("a graph evidence path produced handoff warnings: %v", decl.Warnings)
	}
	// The accepted spelling must name the file the snapshot meant.
	accepted := filepath.Join(workDir, filepath.FromSlash(decl.Facts[0].Evidence[0]))
	if _, err := os.Stat(accepted); err != nil {
		t.Fatalf("accepted path %q does not open the snapshot's file: %v", decl.Facts[0].Evidence[0], err)
	}
}
