package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/store"
)

func newFocusTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil, dir, dir), st
}

// focusIntentFixture creates one direction with a hot premise and a few cold
// rows, so the API tests exercise all three layers without a model.
func focusIntentFixture(t *testing.T, st *store.Store) (model.Intent, model.Fact) {
	t.Helper()
	// The premise is produced by another direction: a fact cannot be its own
	// intent's prerequisite, and the structured graph check now rejects that
	// self-cycle instead of silently accepting a fixture-only shape.
	producer, err := st.CreateIntent(model.Intent{Title: "premise producer"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "focus fixture"})
	if err != nil {
		t.Fatal(err)
	}
	premise, err := st.AddFact(model.Fact{IntentID: producer.ID, Kind: "cred", Title: "premise"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(cur *model.Intent) {
		cur.FromFactIDs = []string{premise.ID}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFact(premise.ID, func(f *model.Fact) { f.Summary = "hot premise" }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := st.AddFact(model.Fact{IntentID: in.ID, Kind: "note", Title: "cold row"}); err != nil {
			t.Fatal(err)
		}
	}
	return in, premise
}

func focusRequest(t *testing.T, mux *http.ServeMux, target string, identity *collab.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if identity != nil {
		req = req.WithContext(WithIdentity(req.Context(), *identity))
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestFocusAPIScopesParticipantToItsOwnIntent(t *testing.T) {
	s, st := newFocusTestServer(t)
	own, _ := focusIntentFixture(t, st)
	other, err := st.CreateIntent(model.Intent{Title: "another direction"})
	if err != nil {
		t.Fatal(err)
	}
	otherFact, err := st.AddFact(model.Fact{IntentID: other.ID, Kind: "note", Title: "another direction fact"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerFocusRoutes(mux)
	identity := collab.Identity{ProjectID: "p", ParticipantID: "pt_own", IntentID: own.ID, RunID: "r_own"}

	rr := focusRequest(t, mux, "/api/focus", &identity)
	if rr.Code != http.StatusOK {
		t.Fatalf("own focus = %d %s", rr.Code, rr.Body.String())
	}
	var page retrieval.FocusPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Intent != own.ID || page.Hot != 1 {
		t.Fatalf("participant page = %+v", page)
	}
	for _, row := range page.Facts {
		if row.Temperature == retrieval.FocusCold {
			t.Fatalf("default focus page leaked a cold row: %+v", row)
		}
	}

	// A query naming another direction is refused, not silently replaced.
	rr = focusRequest(t, mux, "/api/focus?intent="+other.ID, &identity)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-intent focus = %d %s, want 403", rr.Code, rr.Body.String())
	}

	// The operator/commander surface has no participant identity and may name
	// the direction explicitly. The cold layer is project-wide: it is the shared
	// ledger residue, not another task's hot projection.
	rr = focusRequest(t, mux, "/api/focus?intent="+other.ID+"&layer=cold", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("operator focus = %d %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Intent != other.ID {
		t.Fatalf("operator page intent = %q, want %q", page.Intent, other.ID)
	}
	foundOther := false
	for _, row := range page.Facts {
		if row.ID == otherFact.ID {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatal("other direction's fact missing from the project-wide cold layer")
	}
}

func TestFocusAPIRejectsMissingOrUnknownIntent(t *testing.T) {
	s, _ := newFocusTestServer(t)
	mux := http.NewServeMux()
	s.registerFocusRoutes(mux)
	if rr := focusRequest(t, mux, "/api/focus", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("no intent = %d %s, want 400", rr.Code, rr.Body.String())
	}
	if rr := focusRequest(t, mux, "/api/focus?intent=in_unknown", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown intent = %d %s, want 404", rr.Code, rr.Body.String())
	}
}

func TestFocusAPIPaginatesOneFrozenSnapshot(t *testing.T) {
	s, st := newFocusTestServer(t)
	in, _ := focusIntentFixture(t, st)
	mux := http.NewServeMux()
	s.registerFocusRoutes(mux)

	rr := focusRequest(t, mux, "/api/focus?intent="+in.ID+"&layer=cold&limit=4", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("first page = %d %s", rr.Code, rr.Body.String())
	}
	var first retrieval.FocusPage
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Facts) == 0 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	if _, err := st.AddFact(model.Fact{IntentID: in.ID, Kind: "note", Title: "written after the first page"}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range first.Facts {
		seen[row.ID] = true
		if row.RecordVersion == nil {
			t.Fatalf("row %s has no snapshot record version", row.ID)
		}
	}
	cursor := first.NextCursor
	wantVersion := first.Version
	pages := 1
	for cursor != "" {
		rr = focusRequest(t, mux, "/api/focus?intent="+in.ID+"&layer=cold&limit=4&cursor="+cursor, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("page %d = %d %s", pages+1, rr.Code, rr.Body.String())
		}
		var page retrieval.FocusPage
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Version != wantVersion {
			t.Fatalf("page %d snapshot moved: %+v, want %+v", pages+1, page.Version, wantVersion)
		}
		if page.Total != first.Total {
			t.Fatalf("page %d total = %d, want %d", pages+1, page.Total, first.Total)
		}
		for _, row := range page.Facts {
			if seen[row.ID] || strings.Contains(row.Title, "after the first page") {
				t.Fatalf("page %d saw a fact added after the snapshot: %+v", pages+1, row)
			}
			seen[row.ID] = true
		}
		pages++
		cursor = page.NextCursor
	}
	if len(seen) != first.Total {
		t.Fatalf("walked %d rows, want %d", len(seen), first.Total)
	}

	// An expired cursor is a conflict, not a fresh page at the live watermark.
	if rr := focusRequest(t, mux, "/api/focus?intent="+in.ID+"&layer=cold&cursor=not-a-cursor", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d %s, want 400", rr.Code, rr.Body.String())
	}
}

func TestWriteFocusWritesBoundedIndexAndSweepsStalePages(t *testing.T) {
	_, st := newFocusTestServer(t)
	in, premise := focusIntentFixture(t, st)
	if _, err := st.AddFact(model.Fact{IntentID: in.ID, Kind: "vuln", Title: "cold with body", Doc: "SECRET-DOC-BODY"}); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".tw", "focus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A previous dispatch left pages the new run must not advertise.
	if err := os.WriteFile(filepath.Join(dir, "page-9.json"), []byte("old page"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page-1.json.tmp"), []byte("crashed write"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".tw", "focus.json"), []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db := st.Snapshot()
	if !writeFocus(workDir, db, in.ID) {
		t.Fatal("writeFocus reported failure")
	}
	raw, err := os.ReadFile(filepath.Join(workDir, ".tw", "focus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest focusManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest = %s: %v", raw, err)
	}
	if manifest.IntentID != in.ID || manifest.Hot != 1 || manifest.Cold != 13 {
		t.Fatalf("manifest counts = %+v", manifest)
	}
	if manifest.Snapshot == nil || manifest.Snapshot.Seq == 0 {
		t.Fatalf("manifest snapshot = %+v", manifest.Snapshot)
	}
	if manifest.ColdQuery != "/api/focus?intent="+in.ID+"&layer=cold" || manifest.Premises != ".tw/context.json" {
		t.Fatalf("manifest read paths = %+v", manifest)
	}
	if strings.Contains(string(raw), "resources") {
		t.Fatalf("manifest advertises the unimplemented resources entry: %s", raw)
	}
	if len(manifest.Pages) == 0 || manifest.Indexed != manifest.Hot+manifest.Warm {
		// Only hot/warm rows are written to disk; cold rows stay queryable
		// through layer=cold instead of being copied into the case.
		t.Fatalf("manifest pages = %v indexed = %d hot = %d warm = %d", manifest.Pages, manifest.Indexed, manifest.Hot, manifest.Warm)
	}
	foundPremise := false
	for _, rel := range manifest.Pages {
		pageRaw, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("page %s: %v", rel, err)
		}
		if len(pageRaw) > retrieval.FocusPageBytes+4096 {
			t.Fatalf("page %s is %d bytes", rel, len(pageRaw))
		}
		if strings.Contains(string(pageRaw), "SECRET-DOC-BODY") {
			t.Fatalf("page %s carries a doc body", rel)
		}
		if strings.Contains(string(pageRaw), `"id":"`+premise.ID+`"`) &&
			strings.Contains(string(pageRaw), `"temperature":"hot"`) {
			foundPremise = true
		}
	}
	if !foundPremise {
		t.Fatalf("premise row missing from focus pages: %v", manifest.Pages)
	}
	if _, err := os.Stat(filepath.Join(dir, "page-9.json")); !os.IsNotExist(err) {
		t.Fatal("stale page-9.json survived the write")
	}
	if _, err := os.Stat(filepath.Join(dir, "page-1.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("stale temporary page survived the write")
	}
	var stale map[string]any
	if err := json.Unmarshal(raw, &stale); err != nil || stale["old"] != nil {
		t.Fatalf("old manifest was not replaced: %s", raw)
	}
}

func TestFocusPromptNamesTheBoundedIndexAndDeclarationIdentity(t *testing.T) {
	_, st := newFocusTestServer(t)
	in, _ := focusIntentFixture(t, st)
	db := st.Snapshot()
	prompt := BuildPromptWithFocus(db, in, "", "", "", 0, false, true, "")
	for _, want := range []string{".tw/focus.json", "不等于", "layer=cold", "/api/facts/", ".tw/context.json"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("focus prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "resources") {
		t.Fatalf("focus prompt advertises the unimplemented resources entry")
	}
	if !strings.Contains(focusRefreshedNote(), ".tw/focus.json") {
		t.Fatal("continuation note does not name the refreshed index")
	}
	plain := BuildPromptWithWorkerNote(db, in, "", "", "", 0, false, "")
	if strings.Contains(plain, ".tw/focus.json") {
		t.Fatal("prompt points at a focus index that was not written")
	}
	if off := BuildPromptWithFocus(db, in, "", "", "", 0, false, false, ""); strings.Contains(off, ".tw/focus.json") {
		t.Fatal("focus prompt pointer appeared with focus=false")
	}
	// Phase 2's declaration identity is a contract sentence, not a workflow.
	for _, want := range []string{"declaration_id", "observed_at", "execution_context_ref", "同一观察"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("handoff contract missing %q", want)
		}
	}
}
