package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

// manyFactLedger seeds n facts across a few directions and returns the store,
// one producer intent and the fact ids in creation order.
func manyFactLedger(t *testing.T, n int) (*Server, *store.Store, model.Intent, []string) {
	t.Helper()
	s, st := newFocusTestServer(t)
	producer, err := st.CreateIntent(model.Intent{Title: "producer"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		f, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: fmt.Sprintf("fact %04d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	return s, st, producer, ids
}

// The model's on-disk focus index and the human/commander API must describe the
// same projection: identical contract, counts, snapshot, reason breakdown and
// continuation entries. Truncating the bounded case index does not truncate the
// API enumeration, and the manifest says which one it is.
func TestFocusIndexAndAPIExposeTheSameContract(t *testing.T) {
	_, st, producer, _ := manyFactLedger(t, 30)
	in, err := st.CreateIntent(model.Intent{Title: "target"})
	if err != nil {
		t.Fatal(err)
	}
	// 500 pinned rows with long summaries: enough to fill more than the 16
	// bounded case pages (16 * 32 KiB), so the manifest is genuinely truncated
	// while the API stays completely enumerable.
	pinned := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		long := strings.Repeat("证", 240)
		f, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: strings.Repeat("钉", 160), Summary: long})
		if err != nil {
			t.Fatal(err)
		}
		pinned = append(pinned, f.ID)
	}
	if _, err := st.UpdateIntent(in.ID, func(cur *model.Intent) {
		cur.PinnedFactIDs = append([]string{}, pinned...)
	}); err != nil {
		t.Fatal(err)
	}
	// One hard premise and one explicit reference, so the reasons breakdown is
	// not just "pinned".
	old, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "cred", Title: "early premise"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "host", Title: "explicit ref"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(cur *model.Intent) {
		cur.FromFactIDs = []string{old.ID}
		cur.ContextFactIDs = []string{ref.ID}
	}); err != nil {
		t.Fatal(err)
	}
	in, _ = st.GetIntent(in.ID)
	db := st.Snapshot()
	workDir := t.TempDir()
	if err := writeFocusFiles(workDir, db, in.ID); err != nil {
		t.Fatal(err)
	}
	rawManifest, err := os.ReadFile(filepath.Join(workDir, ".tw", "focus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest focusManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	page, err := retrieval.Focus(st, retrieval.FocusQuery{Intent: in.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	// Same contract on both surfaces: heat is not trust, missing/rejected are
	// states, a page is not the layer.
	if manifest.Contract != page.Contract {
		t.Fatalf("model/API focus contract drift:\nmodel=%+v\napi=%+v", manifest.Contract, page.Contract)
	}
	if manifest.Contract != retrieval.FocusContractValue() {
		t.Fatal("focus contract is not the exported single source")
	}
	// Same whole-projection counts (they describe the snapshot, not the page).
	if manifest.Hot != page.Hot || manifest.Warm != page.Warm || manifest.Cold != page.Cold {
		t.Fatalf("counts drifted: model hot/warm/cold=%d/%d/%d api=%d/%d/%d",
			manifest.Hot, manifest.Warm, manifest.Cold, page.Hot, page.Warm, page.Cold)
	}
	if page.Hot != 502 || page.Total != page.Hot+page.Warm {
		t.Fatalf("hot=%d total=%d, want 502 hot rows on the focus layer", page.Hot, page.Total)
	}
	// Snapshot version travels with both surfaces.
	if manifest.Snapshot == nil || manifest.Snapshot.Seq == 0 || *manifest.Snapshot != page.Version {
		t.Fatalf("snapshot: model=%+v api=%+v", manifest.Snapshot, page.Version)
	}
	// Reason breakdown and layer continuation entries are the same read paths.
	wantReasons := map[string]int{
		retrieval.FocusReasonPinned:     500,
		retrieval.FocusReasonRequired:   1,
		retrieval.FocusReasonReference:  1,
		retrieval.FocusReasonProvenance: 1, // the required premise's producer path
	}
	for reason, want := range wantReasons {
		if manifest.ReasonCounts[reason] != want || page.ReasonCounts[reason] != want {
			t.Fatalf("reason %q: model=%d api=%d want %d", reason, manifest.ReasonCounts[reason], page.ReasonCounts[reason], want)
		}
	}
	for _, q := range []struct{ model, api string }{
		{manifest.IndexQuery, page.IndexQuery},
		{manifest.HotQuery, page.HotQuery},
		{manifest.WarmQuery, page.WarmQuery},
		{manifest.ColdQuery, page.ColdQuery},
		{manifest.FactQuery, page.FactQuery},
		{manifest.Premises, page.Premises},
		{manifest.PremisesQuery, page.PremisesQuery},
	} {
		if q.model == "" || q.model != q.api {
			t.Fatalf("read path drift: model=%q api=%q", q.model, q.api)
		}
	}
	if !strings.Contains(page.PremisesQuery, "direction=ancestors") {
		t.Fatalf("premises query is not the complete ancestor catalogue: %q", page.PremisesQuery)
	}
	if !strings.Contains(page.Contract.Heat, "不代表") {
		t.Fatalf("API heat contract does not disclaim truth: %q", page.Contract.Heat)
	}

	// The bounded case index is genuinely truncated and says so without
	// pretending the missing rows do not exist.
	if !manifest.Truncated || manifest.Omitted <= 0 {
		t.Fatalf("manifest truncation = %+v, want a cut index with omitted rows", manifest)
	}
	if manifest.Indexed+manifest.Omitted != len(pinned)+2 {
		t.Fatalf("manifest indexed+omitted = %d+%d, want %d", manifest.Indexed, manifest.Omitted, len(pinned)+2)
	}
	if len(manifest.Pages) > focusMaxPages {
		t.Fatalf("manifest wrote %d pages over the %d cap", len(manifest.Pages), focusMaxPages)
	}
	// The API remains complete: walking every page yields every hot row, with
	// the old premise in it. Truncating the file cannot truncate the ledger.
	seen := map[string]bool{}
	q := retrieval.FocusQuery{Intent: in.ID, Limit: 37}
	for pages := 0; ; pages++ {
		p, err := retrieval.Focus(st, q)
		if err != nil {
			t.Fatal(err)
		}
		if p.Version != page.Version {
			t.Fatalf("page %d snapshot moved", pages+1)
		}
		for _, row := range p.Facts {
			if seen[row.ID] {
				t.Fatalf("row %s repeated", row.ID)
			}
			seen[row.ID] = true
			if row.Temperature != retrieval.FocusHot {
				t.Fatalf("focus layer leaked %s", row.Temperature)
			}
			if row.Query != "/api/facts/"+row.ID {
				t.Fatalf("row %s lost its original link: %q", row.ID, row.Query)
			}
			if row.RecordVersion == nil {
				t.Fatalf("row %s lost its snapshot record version", row.ID)
			}
		}
		if !p.HasMore {
			break
		}
		q.Cursor = p.NextCursor
	}
	if len(seen) != page.Total || !seen[old.ID] {
		t.Fatalf("API enumeration saw %d rows, want %d including old premise", len(seen), page.Total)
	}
}

// The case index is derived from the frozen ledger alone: writing it twice for
// the same snapshot produces identical bytes, and a fresh model session (no
// history) reads the same stable ids. Adding unrelated newer facts keeps the old
// required premise in the index — a compaction or model switch cannot lose it.
func TestFocusIndexIsDeterministicAndSurvivesHistoryLoss(t *testing.T) {
	_, st, producer, _ := manyFactLedger(t, 20)
	premise, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "cred", Title: "old required premise"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "target", FromFactIDs: []string{premise.ID}})
	if err != nil {
		t.Fatal(err)
	}
	db := st.Snapshot()
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	if err := writeFocusFiles(firstDir, db, in.ID); err != nil {
		t.Fatal(err)
	}
	if err := writeFocusFiles(secondDir, db, in.ID); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".tw/focus.json"} {
		a, err := os.ReadFile(filepath.Join(firstDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(secondDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("%s differs between two writes of one frozen snapshot", rel)
		}
	}
	firstManifest := readFocusManifest(t, firstDir)
	if len(firstManifest.Pages) == 0 {
		t.Fatal("focus index wrote no pages")
	}
	// The old premise is in the hot layer of the bounded index.
	if !focusPagesContain(t, firstDir, firstManifest, premise.ID) {
		t.Fatal("old required premise missing from the hot focus pages")
	}

	// Many newer distractors arrive after the index was written. The next
	// dispatch sees a new snapshot but the stable premise id and its reason
	// stay the same; nothing session-dependent is involved.
	for i := 0; i < 300; i++ {
		if _, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: fmt.Sprintf("distractor %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	thirdDir := t.TempDir()
	if err := writeFocusFiles(thirdDir, st.Snapshot(), in.ID); err != nil {
		t.Fatal(err)
	}
	thirdManifest := readFocusManifest(t, thirdDir)
	if !focusPagesContain(t, thirdDir, thirdManifest, premise.ID) {
		t.Fatal("old required premise evicted by newer facts")
	}
	page, err := retrieval.Focus(st, retrieval.FocusQuery{Intent: in.ID, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Facts {
		if row.ID == premise.ID {
			found = row.Temperature == retrieval.FocusHot && row.Reasons[0] == retrieval.FocusReasonRequired
		}
	}
	if !found {
		t.Fatal("old premise is not the first hot required row after the distractors")
	}
}

// The worker prompt names the same bounded index and the same complete premise
// catalogue in a fresh dispatch and in a continuation, so a model switch or a
// harness compaction does not change the contract it sees. The catalogue itself
// is the complete premise list: IDs, versions and missing/rejected states, paged
// without a silent cut.
func TestDispatchContextContractIsSessionIndependentForOldPremise(t *testing.T) {
	_, st, producer, _ := manyFactLedger(t, 10)
	old, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "cred", Title: "first-hour credential"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "vuln", Title: "rejected claim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: rejected.ID, Verdict: "reject", Notes: "fixture"}); err != nil {
		t.Fatal(err)
	}
	premises := []string{old.ID, rejected.ID}
	for i := 0; i < 140; i++ {
		f, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "note", Title: fmt.Sprintf("premise %03d", i)})
		if err != nil {
			t.Fatal(err)
		}
		premises = append(premises, f.ID)
	}
	in, err := st.CreateIntent(model.Intent{Title: "target", FromFactIDs: premises})
	if err != nil {
		t.Fatal(err)
	}
	db := st.Snapshot()
	workDir := t.TempDir()
	if err := writeFocusFiles(workDir, db, in.ID); err != nil {
		t.Fatal(err)
	}
	fresh := BuildPromptWithFocus(db, in, "", "", "", 0, false, true, "")
	continued := "## 本轮\n" + focusRefreshedNote() + "\n" + fresh
	for name, prompt := range map[string]string{"fresh": fresh, "continued": continued} {
		for _, want := range []string{".tw/focus.json", "/api/facts/", ".tw/context.json", "不等于", "layer=cold"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s prompt missing %q", name, want)
			}
		}
	}
	// A fresh model session receives the complete premise catalogue for the
	// same frozen ledger: two builds must be identical (nothing depends on a
	// previous session or on map iteration order).
	again := BuildPromptWithFocus(db, in, "", "", "", 0, false, true, "")
	if fresh != again {
		t.Fatal("fresh dispatch prompt changed between builds of one frozen ledger")
	}
	// The catalogue lists every required premise, including the old one, with
	// missing/rejected states preserved and no silent cut (page count grows).
	if err := writeTaskManifest(workDir, db, in.ID); err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		IntentID      string   `json:"intent_id"`
		Prerequisites int      `json:"prerequisites_total"`
		Pages         []string `json:"pages"`
	}
	raw, err := os.ReadFile(filepath.Join(workDir, ".tw", "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Prerequisites != len(premises) {
		t.Fatalf("catalogue total = %d, want %d (no silent trim)", catalog.Prerequisites, len(premises))
	}
	if len(catalog.Pages) < 2 {
		t.Fatalf("catalogue pages = %v, want the >128 premise list paged", catalog.Pages)
	}
	type ref struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
		State   string `json:"state"`
	}
	seen := map[string]ref{}
	for _, rel := range catalog.Pages {
		pageRaw, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		var refs []ref
		if err := json.Unmarshal(pageRaw, &refs); err != nil {
			t.Fatal(err)
		}
		for _, r := range refs {
			if _, dup := seen[r.ID]; dup {
				t.Fatalf("premise %s repeated across catalogue pages", r.ID)
			}
			seen[r.ID] = r
		}
	}
	if len(seen) != len(premises) {
		t.Fatalf("catalogue enumerated %d premises, want %d", len(seen), len(premises))
	}
	if got := seen[old.ID]; got.State != "recorded" || got.Version == 0 {
		t.Fatalf("old premise in catalogue = %+v", got)
	}
	if got := seen[rejected.ID]; got.State != "rejected" {
		t.Fatalf("rejected premise state = %+v, want rejected (heat/order never upgrades trust)", got)
	}
}

func readFocusManifest(t *testing.T, workDir string) focusManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(focusFile)))
	if err != nil {
		t.Fatal(err)
	}
	var manifest focusManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func focusPagesContain(t *testing.T, workDir string, manifest focusManifest, factID string) bool {
	t.Helper()
	for _, rel := range manifest.Pages {
		raw, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		var page focusPageFile
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Facts {
			if row.ID == factID && row.Temperature == retrieval.FocusHot {
				return true
			}
		}
	}
	return false
}

// The real commander round input has to label the board top-N itself: the
// commander may never call the read APIs, and a plain list of 20 open intents
// can be read as "the project has 20 directions". The digest and the complete
// enumeration entries ride the same JSON the model receives.
func TestCommanderRoundPromptLabelsBoardTopNAsDigest(t *testing.T) {
	_, st, _, _ := manyFactLedger(t, 40)
	for i := 0; i < 45; i++ {
		if _, err := st.CreateIntent(model.Intent{Title: fmt.Sprintf("direction %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	prompt := decider.CommanderRoundPrompt(board.Build(st, 20), "笔记", "10.0.0.0/24",
		decider.ContextStatus{Used: 1000, Window: 128000}, decider.RoundContext{})
	for _, want := range []string{
		"prompt_digest",          // the label is in the input, not only in a doc
		"bounded_lists",          // per-list shown/total
		"/api/intents?limit=200", // complete enumeration for the open/done lists
		"/api/facts?limit=200",
		"/api/focus?intent=", // task focus entry: commander has no case dir, so the API is the entry (docs/32 remaining)
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("commander round prompt missing %q", want)
		}
	}
	// The digest totals must describe the ledger, not the 20 shown rows.
	var payload struct {
		Open   []any               `json:"open_intents"`
		Digest *board.PromptDigest `json:"prompt_digest"`
	}
	start := strings.Index(prompt, "## board\n")
	if start < 0 {
		t.Fatal("commander prompt has no board section")
	}
	body := prompt[start+len("## board\n"):]
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &payload); err != nil {
		t.Fatalf("board section is not the expected JSON: %v", err)
	}
	if len(payload.Open) != 20 || payload.Digest == nil {
		t.Fatalf("open list = %d, digest = %+v", len(payload.Open), payload.Digest)
	}
	var openTotal, openShown int
	found := false
	for _, b := range payload.Digest.Bounded {
		if b.List == "open_intents" {
			openTotal, openShown, found = b.Total, b.Shown, true
		}
	}
	if !found || openShown != 20 || openTotal != 46 {
		t.Fatalf("open_intents digest = shown %d total %d (found=%v), want 20/46", openShown, openTotal, found)
	}
}
