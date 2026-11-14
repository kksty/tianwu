package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func instructionFixture(t *testing.T, s *Store, target, key string) model.Instruction {
	t.Helper()
	in, _, err := s.CreateInstruction(model.Instruction{CreatedBy: collab.OperatorID, IntentID: target, Text: "只在授权范围内推进", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestInstructionsSurviveRestartAndApplyToFutureDescendants(t *testing.T) {
	s := open(t)
	parent, _ := s.CreateIntent(model.Intent{Title: "parent"})
	global := instructionFixture(t, s, "", "global")
	local := instructionFixture(t, s, parent.ID, "local")
	child, _, err := s.CreateSpawnIntent(model.Intent{Title: "child", ParentID: parent.ID, CreatedBy: "commander", IdempotencyKey: "child"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateIntent(model.Intent{Title: "other"})
	if got := ApplicableInstructions(s.Snapshot(), child.ID, false); len(got) != 2 {
		t.Fatalf("child=%+v", got)
	}
	if got := ApplicableInstructions(s.Snapshot(), other.ID, false); len(got) != 1 || got[0].ID != global.ID {
		t.Fatalf("other=%+v", got)
	}
	if _, err := s.UpdateInstruction(local.ID, 1, "更新的约束", model.InstructionActive); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateInstruction(local.ID, 1, "过时写入", model.InstructionRevoked); !errors.Is(err, ErrInstructionConflict) {
		t.Fatal(err)
	}
	if _, err := s.UpdateInstruction(local.ID, 2, "更新的约束", model.InstructionRevoked); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.Instructions()) != 2 || len(reopened.History("instruction", local.ID)) != 3 {
		t.Fatal("instruction revisions lost")
	}
	if got := ApplicableInstructions(reopened.Snapshot(), child.ID, false); len(got) != 1 {
		t.Fatalf("revoked inherited: %+v", got)
	}
	if len(reopened.Snapshot().Facts) != 0 {
		t.Fatal("guidance became a fact")
	}
}

func TestInstructionAndRelevantNotificationsCommitOrRollbackTogether(t *testing.T) {
	s := open(t)
	parent, _ := s.CreateIntent(model.Intent{Title: "parent"})
	child, _, _ := s.CreateSpawnIntent(model.Intent{Title: "child", ParentID: parent.ID, CreatedBy: "commander", IdempotencyKey: "c"})
	_, run, err := s.StartIntentRun(child.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateIntent(model.Intent{Title: "other"})
	_, _, _ = s.StartIntentRun(other.ID, "local", "", model.Run{})
	local := instructionFixture(t, s, parent.ID, "local")
	notices := s.InstructionNotifications(local.ID, 1)
	if len(notices) != 2 {
		t.Fatalf("wanted commander + child: %+v", notices)
	}
	for _, m := range notices {
		if !m.Durable || m.ToParticipantID == collab.ParticipantID(other.ID) {
			t.Fatal("unrelated notification")
		}
	}
	r, _ := s.GetRun(run.ID)
	if r.InstructionNotices[local.ID] != 1 {
		t.Fatal("notice version not committed")
	}
	before, _ := json.Marshal(s.Snapshot())
	if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_instruction BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateInstruction(local.ID, 1, "should roll back", model.InstructionActive); err == nil {
		t.Fatal("expected failed transaction")
	}
	after, _ := json.Marshal(s.Snapshot())
	if string(before) != string(after) {
		t.Fatal("failed instruction leaked notification or version")
	}
}

func TestInstructionInputAndAcknowledgementUseSpecificRunAndVersion(t *testing.T) {
	s := open(t)
	in := instructionFixture(t, s, "", "g")
	task, _ := s.CreateIntent(model.Intent{Title: "work"})
	brief, run, _ := s.StartIntentRun(task.ID, "local", "", model.Run{})
	if _, err := s.PrepareRunInput(run.ID, brief, model.PremiseVerifiedOnly); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetRun(run.ID)
	if r.InstructionVersions[in.ID] != 1 || len(r.InstructionAcks) != 0 {
		t.Fatal("preparation is not acknowledgement")
	}
	r.InstructionVersions[in.ID] = 999
	if err := s.AcknowledgeInstruction(run.ID, in.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateInstruction(in.ID, 1, "new text", model.InstructionActive); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeInstruction(run.ID, in.ID, 1); !errors.Is(err, ErrInstructionConflict) {
		t.Fatal("stale acknowledgement accepted")
	}
	if err := s.AcknowledgeInstruction(run.ID, in.ID, 2); err != nil {
		t.Fatal(err)
	}
	_, _ = s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone })
	if err := s.AcknowledgeInstruction(run.ID, in.ID, 2); err == nil {
		t.Fatal("finished holder acknowledged")
	}
}

func TestInstructionCreateRetryKeepsOriginalIdentityAfterRevision(t *testing.T) {
	s := open(t)
	in := instructionFixture(t, s, "", "key")
	_, _ = s.UpdateInstruction(in.ID, 1, "revised", model.InstructionActive)
	again, created, err := s.CreateInstruction(model.Instruction{CreatedBy: collab.OperatorID, Text: in.Text, IdempotencyKey: "key"})
	if err != nil || created || again.ID != in.ID || again.Version != 2 {
		t.Fatalf("retry=%+v new=%v err=%v", again, created, err)
	}
	_, _, err = s.CreateInstruction(model.Instruction{CreatedBy: collab.OperatorID, Text: "different", IdempotencyKey: "key"})
	if !errors.Is(err, ErrInstructionConflict) {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		_, _, err = s.CreateInstruction(model.Instruction{CreatedBy: collab.OperatorID, Text: strings.Repeat("界", 4000), IdempotencyKey: NewID("large")})
		if i < 5 && err != nil {
			t.Fatal(err)
		}
	}
	if err == nil {
		t.Fatal("active instruction capacity must reject explicitly")
	}
}

func TestTaskResultsAndExplicitDurableMessagesSurviveChatRetention(t *testing.T) {
	s := open(t)
	for _, m := range []model.Message{{Kind: model.MessageResult}, {Kind: "question", Durable: true}, {Kind: "note"}} {
		m.FromParticipantID, m.ToParticipantID, m.Text = "a", "b", "content"
		row, _, err := s.AddMessage(m)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.UpdateMessage(row.ID, func(m *model.Message) { m.CreatedAt = time.Now().Add(-48 * time.Hour) })
	}
	if n, err := s.SweepQueued(time.Now(), time.Hour, 1); err != nil || n != 1 {
		t.Fatalf("sweep=%d %v", n, err)
	}
	if len(s.PendingFor("b")) != 2 {
		t.Fatal("durable result lost")
	}
}

func TestPreviousProjectSchemaIsRejectedWithoutUpgrade(t *testing.T) {
	s := open(t)
	var raw []byte
	if err := s.sqlite.db.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta sqliteMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Version = schemaVersion - 1
	raw, _ = json.Marshal(meta)
	if _, err := s.sqlite.db.Exec("UPDATE meta SET payload=? WHERE id=1", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.Path()); err == nil || !strings.Contains(err.Error(), "migration is not supported") {
		t.Fatalf("old project accepted: %v", err)
	}
	var after []byte
	if err := s.sqlite.db.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("opening an old project rewrote its metadata")
	}
}
