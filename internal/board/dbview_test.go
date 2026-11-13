package board

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// BuildFromDB exists so a caller that already froze a ledger clone (a report
// input moment) can derive the board view and source digest from that clone
// instead of taking a second Snapshot. It must be exactly Build's view for the
// same ledger state, including the content digest the report list compares.
func TestBuildFromDBSameViewAsBuild(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIntent(model.Intent{Title: "查 /admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "越权", Status: "unverified"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/a.txt", Name: "a.txt"}); err != nil {
		t.Fatal(err)
	}

	live := Build(st, 20)
	frozen := BuildFromDB(st.Snapshot(), 20)
	// GeneratedAt is the wall clock at each call, not ledger content.
	live.GeneratedAt, frozen.GeneratedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(live, frozen) {
		t.Fatalf("BuildFromDB differs from Build for the same ledger:\nlive=%+v\nfrozen=%+v", live, frozen)
	}
	if got, want := ContentDigest(frozen), ContentDigest(live); got != want {
		t.Fatalf("source digest = %q, want %q", got, want)
	}
}

// A clone frozen by PreparePurposeInput does not run Snapshot's DataHash lift,
// so BuildFromDB has to read the watermark from Memory. The digest must still
// equal what the report list computes from the live store for the same moment:
// otherwise every new report would immediately read as stale.
func TestBuildFromDBFrozenPurposeInputKeepsContentDigest(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "越权", Status: "unverified"}); err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{Purpose: "report", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := st.PreparePurposeInput(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ContentDigest(BuildFromDB(db, 20)), ContentDigest(Build(st, 20)); got != want {
		t.Fatalf("frozen digest = %q, live digest = %q (the run row must not change content)", got, want)
	}
}
