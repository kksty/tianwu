package board

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func emergenceStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// An empty ledger must still serialise the aggregate as zeros with every field
// present: the console renders one stable shape whether or not the model has
// done anything yet.
func TestEmergenceEmptyLedgerReportsZeros(t *testing.T) {
	st := emergenceStore(t)
	v := Build(st, 20)
	if v.Emergence == nil {
		t.Fatal("empty ledger lost the emergence object")
	}
	if *v.Emergence != (EmergenceStats{}) {
		t.Fatalf("empty ledger emergence = %+v, want zeros", *v.Emergence)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var em map[string]json.RawMessage
	if err := json.Unmarshal(raw["emergence"], &em); err != nil {
		t.Fatalf("view has no emergence object: %s", b)
	}
	for _, key := range emergenceJSONKeys {
		if _, ok := em[key]; !ok {
			t.Fatalf("emergence is missing %q: %s", key, b)
		}
	}
	if len(em) != len(emergenceJSONKeys) {
		t.Fatalf("emergence has unexpected keys: %s", raw["emergence"])
	}
}

// The aggregate is a count of what the model did on its own. Each signal is
// independent and derived from a different table, so a code path that silently
// drops one (messages, runs, intent provenance) must fail here.
func TestEmergenceCountsSelfOrganisedWork(t *testing.T) {
	st := emergenceStore(t)

	// Directions: five opened by the commander (two plain, one documented
	// park, one unreasoned park, one handoff) and two by an executor (one
	// child under first, one with no parent).
	first, err := st.CreateIntent(model.Intent{Title: "指挥官自建方向", CreatedBy: collab.CommanderID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateIntent(model.Intent{Title: "另一个指挥官方向", CreatedBy: collab.CommanderID})
	if err != nil {
		t.Fatal(err)
	}
	workerParticipant := collab.ParticipantID(first.ID)
	if _, err := st.CreateIntent(model.Intent{
		Title: "worker 造的子方向", ParentID: first.ID, CreatedBy: workerParticipant,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIntent(model.Intent{Title: "worker 造的无父方向", CreatedBy: workerParticipant}); err != nil {
		t.Fatal(err)
	}
	parked, err := st.CreateIntent(model.Intent{Title: "带证据的自主停放", CreatedBy: collab.CommanderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(parked.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
		i.ParkReason = "前提被证伪"
	}); err != nil {
		t.Fatal(err)
	}
	unreasoned, err := st.CreateIntent(model.Intent{Title: "只停放不写理由", CreatedBy: collab.CommanderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(unreasoned.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
	}); err != nil {
		t.Fatal(err)
	}
	handoff, err := st.CreateIntent(model.Intent{Title: "换档方向", CreatedBy: collab.CommanderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(handoff.ID, func(i *model.Intent) {
		i.HandoffVersion = 2
		i.Handoff = &model.Handoff{Version: 2}
	}); err != nil {
		t.Fatal(err)
	}

	// The horizon is derived from the oldest intent, so age it explicitly.
	if _, err := st.UpdateIntent(second.ID, func(i *model.Intent) {
		i.CreatedAt = time.Now().Add(-2*time.Hour - 30*time.Minute)
	}); err != nil {
		t.Fatal(err)
	}

	// Runs: two worker-tier profiles (one repeated), one verifier and one
	// commander purpose run that must not count as the model choosing a tier,
	// one legacy row with no resolved profile, and one self-inflicted kill.
	for _, r := range []model.Run{
		{IntentID: first.ID, Purpose: "", ResolvedProfile: "p1"},
		{IntentID: second.ID, Purpose: "", ResolvedProfile: "p2"},
		{IntentID: first.ID, Purpose: "", ResolvedProfile: "p1"},
		{IntentID: first.ID, Purpose: "verifier", ResolvedProfile: "p3"},
		{Purpose: "commander", ResolvedProfile: "p4"},
		{IntentID: first.ID, Purpose: ""},
	} {
		if _, err := st.AddRun(r); err != nil {
			t.Fatal(err)
		}
	}
	killed, err := st.AddRun(model.Run{IntentID: first.ID, Purpose: ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(killed.ID, func(r *model.Run) {
		r.Status = model.RunCanceled
		r.CancelCause = model.CancelCauseWorkerKill
	}); err != nil {
		t.Fatal(err)
	}

	// Messages: two worker-to-worker notes, one commander message, one operator
	// channel message and one legacy row without a sender.
	peer := collab.ParticipantID(second.ID)
	for _, m := range []model.Message{
		{FromParticipantID: workerParticipant, ToParticipantID: peer, Kind: model.MessageNote, Text: "给你几条事实"},
		{FromParticipantID: peer, ToParticipantID: workerParticipant, Kind: model.MessageNote, Text: "收到"},
		{FromParticipantID: collab.CommanderID, ToParticipantID: workerParticipant, Text: "尽快收口"},
		{FromParticipantID: collab.OperatorID, ToParticipantID: workerParticipant, Text: "人工插话"},
		{ToParticipantID: workerParticipant, Text: "旧行没有发件人"},
	} {
		if _, _, err := st.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	v := Build(st, 20)
	if v.Emergence == nil {
		t.Fatal("board lost emergence")
	}
	e := *v.Emergence
	want := EmergenceStats{
		CommanderCreatedIntents: 5,
		WorkerCreatedIntents:    2,
		ChildIntents:            1,
		SpawnedWorkers:          2,
		Handoffs:                1,
		WorkerKills:             1,
		PeerMessages:            2,
		ModelMessages:           3,
		ProfilesUsed:            2,
		HorizonHours:            3,
		ParkedWithReason:        1,
	}
	if e != want {
		t.Fatalf("emergence = %+v, want %+v", e, want)
	}

	// The prompt is the one place the aggregate must not reach: a model that
	// can read its own autonomy score optimises the score. The API board keeps
	// it for the operator.
	if got := ForPrompt(v).Emergence; got != nil {
		t.Fatalf("ForPrompt kept emergence: %+v", got)
	}
}

// Rows written before CreatedBy/ResolvedProfile/CancelCause existed load as
// zero values. They must count as nothing, not panic and not turn into a fake
// "commander" or a fake worker.
func TestEmergenceToleratesLegacyRowsWithoutProvenance(t *testing.T) {
	st := emergenceStore(t)
	if _, err := st.CreateIntent(model.Intent{Title: "旧方向"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRun(model.Run{Node: "local"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddMessage(model.Message{ToParticipantID: collab.CommanderID, Text: "旧消息"}); err != nil {
		t.Fatal(err)
	}

	v := Build(st, 20)
	if v.Emergence == nil {
		t.Fatal("board lost emergence")
	}
	e := *v.Emergence
	if e.CommanderCreatedIntents != 0 || e.WorkerCreatedIntents != 0 || e.ChildIntents != 0 {
		t.Fatalf("legacy intents were attributed to someone: %+v", e)
	}
	if e.SpawnedWorkers != 0 || e.Handoffs != 0 || e.WorkerKills != 0 || e.ParkedWithReason != 0 {
		t.Fatalf("legacy rows produced emergence: %+v", e)
	}
	if e.ModelMessages != 0 || e.PeerMessages != 0 {
		t.Fatalf("legacy message counted as model authorship: %+v", e)
	}
	if e.ProfilesUsed != 0 {
		t.Fatalf("empty profile counted as a tier: %+v", e)
	}
	// The intent was written now, so the ceil horizon is 1 — never a huge span
	// from a zero timestamp.
	if e.HorizonHours != 1 {
		t.Fatalf("horizon_hours = %d, want 1 for a fresh ledger", e.HorizonHours)
	}
}

// emergenceJSONKeys is the console/API contract for the aggregate. Renaming a
// Go field is cheap; renaming the JSON key silently breaks every reader, so
// the names are pinned here.
var emergenceJSONKeys = []string{
	"commander_created_intents",
	"worker_created_intents",
	"child_intents",
	"spawned_workers",
	"handoffs",
	"worker_kills",
	"peer_messages",
	"model_messages",
	"profiles_used",
	"horizon_hours",
	"parked_with_reason",
}

func TestEmergenceJSONFieldNamesAreStable(t *testing.T) {
	st := emergenceStore(t)
	if _, err := st.CreateIntent(model.Intent{Title: "一个方向", CreatedBy: collab.CommanderID}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(Build(st, 20))
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]json.RawMessage
	if err := json.Unmarshal(b, &view); err != nil {
		t.Fatal(err)
	}
	var em map[string]json.RawMessage
	if err := json.Unmarshal(view["emergence"], &em); err != nil {
		t.Fatalf("view has no emergence object: %s", b)
	}
	got := make([]string, 0, len(em))
	for key := range em {
		got = append(got, key)
	}
	sort.Strings(got)
	want := append([]string(nil), emergenceJSONKeys...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("emergence keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("emergence keys = %v, want %v", got, want)
		}
	}
}

// The API board and the prompt projection must differ on exactly this field:
// the operator can see what the model organised on its own, the model cannot.
// This pins the JSON consequence of ForPrompt, not just the nil pointer.
func TestForPromptOmitsEmergenceFromJSON(t *testing.T) {
	st := emergenceStore(t)
	if _, err := st.CreateIntent(model.Intent{Title: "一个方向", CreatedBy: collab.CommanderID}); err != nil {
		t.Fatal(err)
	}
	v := Build(st, 20)
	if v.Emergence == nil {
		t.Fatal("API board lost emergence")
	}
	boardJSON, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(boardJSON), `"emergence"`) {
		t.Fatalf("API board JSON lost emergence: %s", boardJSON)
	}
	promptJSON, err := json.Marshal(ForPrompt(v))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(promptJSON), `"emergence"`) {
		t.Fatalf("ForPrompt JSON kept emergence: %s", promptJSON)
	}
}
