package ledger

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestReliabilityFailedSettlementMustNotConsumeUncommittedDeclaration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ledger.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	work := filepath.Join(root, "cases", "in_x")
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), `{"declaration_id":"observation-1","kind":"note","title":"uncommitted observation"}`+"\n")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec("CREATE TRIGGER audit_fail_fact BEFORE INSERT ON objects WHEN NEW.kind='fact' BEGIN SELECT RAISE(ABORT,'temporary fixture failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	ing := &Ingestor{Store: st}
	ing.OnFinish("run", "in_x", filepath.Join(root, "cases"), work, time.Now().Add(time.Hour))
	if off := Consumed(work); off != 0 {
		t.Fatalf("settlement advanced cursor to %d past a fact whose transaction failed; retry cannot recover it", off)
	}
	if _, err := db.Exec("DROP TRIGGER audit_fail_fact"); err != nil {
		t.Fatal(err)
	}
	ing.OnFinish("run", "in_x", filepath.Join(root, "cases"), work, time.Now().Add(time.Hour))
	if len(st.ListFacts("in_x", 0)) != 1 || Consumed(work) == 0 {
		t.Fatal("failed declaration was not recovered after repair")
	}
}

func TestSettlementRetriesPartialBatchAfterEvidenceOrKnowledgeWriteFailure(t *testing.T) {
	for _, kind := range []string{"evidence", "playbook"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "ledger.sqlite")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("CREATE TRIGGER fail_write BEFORE INSERT ON objects WHEN NEW.kind='" + kind + "' BEGIN SELECT RAISE(ABORT,'temporary failure'); END"); err != nil {
				t.Fatal(err)
			}
			caseRoot := filepath.Join(root, "cases")
			work := filepath.Join(caseRoot, "in_x")
			writeFile(t, filepath.Join(work, "proof.txt"), "synthetic evidence")
			writeFile(t, filepath.Join(work, HandoffDir, HandoffFile),
				`{"declaration_id":"kept","kind":"note","title":"kept observation"}`+"\n"+
					`{"declaration_id":"proved","kind":"vuln","title":"proved observation","evidence":["proof.txt"]}`+"\n"+
					`{"kind":"playbook","title":"reusable fixture","doc":"local fixture only"}`+"\n")
			ing := &Ingestor{Store: st}
			ing.OnFinish("first", "in_x", caseRoot, work, time.Now().Add(time.Hour))
			kept, ok := st.FindDeclaration("in_x", "", "kept")
			if !ok || Consumed(work) != 0 {
				t.Fatalf("partial batch was lost or consumed: %+v offset=%d", kept, Consumed(work))
			}
			if _, err = db.Exec("DROP TRIGGER fail_write"); err != nil {
				t.Fatal(err)
			}
			ing.OnFinish("retry", "in_x", caseRoot, work, time.Now().Add(time.Hour))
			replayed, _ := st.FindDeclaration("in_x", "", "kept")
			proved, _ := st.FindDeclaration("in_x", "", "proved")
			if Consumed(work) == 0 || replayed.ID != kept.ID || replayed.ClaimVersion != kept.ClaimVersion || len(proved.EvidenceIDs) != 1 || len(st.Evidence()) != 1 || len(st.Playbooks()) != 1 {
				t.Fatalf("batch did not recover idempotently: kept=%+v proved=%+v", replayed, proved)
			}
		})
	}
}

func TestSettlementReplayPreservesEvidenceIdentityAndVerifiedClaim(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	caseRoot := filepath.Join(root, "cases")
	work := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(work, "proof.txt"), "unchanged proof")
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), `{"declaration_id":"stable","kind":"vuln","title":"claim","evidence":["proof.txt"]}`+"\n")
	ing := &Ingestor{Store: st}
	ing.OnFinish("run", "in_x", caseRoot, work, time.Now().Add(time.Hour))
	f, _ := st.FindDeclaration("in_x", "", "stable")
	if _, err = st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "fixture-review"}); err != nil {
		t.Fatal(err)
	}
	if err = CommitDeclared(work, 0); err != nil {
		t.Fatal(err)
	}
	ing.OnFinish("retry", "in_x", caseRoot, work, time.Now().Add(time.Hour))
	got, _ := st.FactByID(f.ID)
	if got.Status != "verified" || got.ClaimVersion != f.ClaimVersion || len(st.Evidence()) != 1 || got.EvidenceIDs[0] != f.EvidenceIDs[0] {
		t.Fatalf("replay changed the proof: %+v", got)
	}
}
