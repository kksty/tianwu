package ledger

// Evidence paths that cannot be registered used to be a bare count in the log.
// They are now structured warnings on the run row, so the operator — and the
// next round's prompt — can see which path to fix (task-17).

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func newWarningLedger(t *testing.T) (*store.Store, model.Run) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: "in_1", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

func TestDeclaredEvidenceWarningsAcrossThreeStates(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "ok.txt"), "proof")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"越界证据","summary":"s","evidence":["../outside.txt"]}
{"kind":"vuln","title":"彻底越界","summary":"s","evidence":["../../outside.txt"]}
{"kind":"vuln","title":"缺失证据","summary":"s","evidence":["missing.txt"]}
{"kind":"note","title":"合法证据","summary":"s","evidence":["ok.txt"]}
`)
	st, run := newWarningLedger(t)
	g := &Ingestor{Store: st}
	if got := g.OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour)); got != 2 {
		t.Fatalf("declared = %d, want 2 kept facts (the invalid lines are dropped)", got)
	}

	run, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	// Two invalid paths: one resolves directly into the cases root (a file
	// there belongs to no case), the other leaves the project cases tree
	// entirely. Both are refused, and both are named on the run row; only the
	// missing file is kept (as a ref).
	if len(run.Warnings) != 3 {
		t.Fatalf("warnings = %v, want one per invalid path and one for the missing file", run.Warnings)
	}
	joined := strings.Join(run.Warnings, "\n")
	for _, want := range []string{
		"越界证据", "../outside.txt", evidenceOutOfBoundsWarning,
		"彻底越界", "../../outside.txt", evidenceOutOfBoundsWarning,
		"缺失证据", "missing.txt", "文件不存在",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning text missing %q:\n%s", want, joined)
		}
	}
	for _, banned := range []string{"合法证据", "ok.txt"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("a valid path produced a warning (%q):\n%s", banned, joined)
		}
	}

	// The valid declaration is still evidence; the missing one is kept as a ref
	// (D44) and the invalid line was dropped. The evidence address is the
	// preserved copy, with the working path kept as provenance (D150).
	db := st.Snapshot()
	if len(db.Evidence) != 1 || db.Evidence[0].SourceURI != "in_1/ok.txt" || db.Evidence[0].Digest == "" {
		t.Fatalf("evidence = %+v", db.Evidence)
	}
}

func TestValidEvidenceProducesNoWarnings(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "ok.txt"), "proof")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"note","title":"都合法","summary":"s","evidence":["ok.txt"]}`)
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	if len(run.Warnings) != 0 {
		t.Fatalf("valid evidence produced warnings: %v", run.Warnings)
	}
}

func TestRunWarningsAreCapped(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	var lines strings.Builder
	for i := 0; i < maxRunWarnings+5; i++ {
		lines.WriteString(`{"kind":"note","title":"事实`)
		lines.WriteString(strings.Repeat("x", i%3+1))
		lines.WriteString(itoa(i))
		lines.WriteString(`","summary":"s","evidence":["missing-`)
		lines.WriteString(itoa(i))
		lines.WriteString(`.txt"]}` + "\n")
	}
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), lines.String())
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	if len(run.Warnings) != maxRunWarnings {
		t.Fatalf("warnings = %d, want the cap %d", len(run.Warnings), maxRunWarnings)
	}
}

func TestMergeWarningsDedupesAndBounds(t *testing.T) {
	got := mergeWarnings([]string{"a", "b"}, []string{"b", " a ", "", "c", "c"})
	want := []string{"a", "b", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("merge = %v, want %v", got, want)
	}
	many := make([]string, 0, maxRunWarnings+10)
	for i := 0; i < maxRunWarnings+10; i++ {
		many = append(many, "w"+itoa(i))
	}
	if got := mergeWarnings(nil, many); len(got) != maxRunWarnings {
		t.Fatalf("merged %d warnings, want %d", len(got), maxRunWarnings)
	}
}

// A run row serialised without warnings keeps its old shape; a warning list
// written today must survive a round trip.
func TestRunWarningsJSONRoundTrip(t *testing.T) {
	old := []byte(`{"id":"r_old","intent_id":"in_1","node":"local","status":"done"}`)
	var r model.Run
	if err := json.Unmarshal(old, &r); err != nil {
		t.Fatal(err)
	}
	if r.Warnings != nil {
		t.Fatalf("an old run grew warnings: %v", r.Warnings)
	}
	raw, err := json.Marshal(model.Run{ID: "r_new", Warnings: []string{"w"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"warnings":["w"]`) {
		t.Fatalf("warnings missing from JSON: %s", raw)
	}
	raw, err = json.Marshal(model.Run{ID: "r_plain"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "warnings") {
		t.Fatalf("omitempty did not drop an empty list: %s", raw)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
