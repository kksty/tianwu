package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/contract"
	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The operator's authorization is frozen into the commander's own run, so a
// planner cannot make it disappear by simply not mentioning it: the id and
// version stay in run.InstructionVersions, the text is listed under 授权来源, and
// a conflict carries the one instruction that is allowed — stop and ask the
// operator. What the code guarantees is visibility and the obligation to ask;
// obedience is the model's, not something a flag can enforce.
func TestOperatorAuthorizationSurvivesTheCommanderPlan(t *testing.T) {
	_, p := newBindingServer(t)
	authz, _, err := p.Store.CreateInstruction(model.Instruction{
		CreatedBy: collab.OperatorID, Text: "真实订阅 topic 可测，只保留元信息",
		Authorization: true, IdempotencyKey: "op-auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.Store.PrepareCommanderInstructions(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsInstruction(rows, authz.ID, authz.Version) {
		t.Fatalf("frozen commander set dropped the operator authorization: %+v", rows)
	}
	saved, ok := p.Store.GetRun(run.ID)
	if !ok || saved.InstructionVersions[authz.ID] != authz.Version {
		t.Fatalf("run freeze = %+v, want %s v%d", saved.InstructionVersions, authz.ID, authz.Version)
	}

	// A commander-authored instruction on the same subject must not replace it:
	// both stay applicable, and only the operator's is a permission.
	if _, _, err := p.Store.CreateInstruction(model.Instruction{
		CreatedBy: collab.CommanderID, Text: "先按旧口径只读", IdempotencyKey: "cmd-1",
	}); err != nil {
		t.Fatal(err)
	}
	authorized := store.AuthorizedInstructions(p.Store.Snapshot(), "", true)
	if !containsInstruction(authorized, authz.ID, authz.Version) {
		t.Fatalf("authorization vanished after a commander instruction: %+v", authorized)
	}
	for _, in := range authorized {
		if in.CreatedBy == collab.CommanderID {
			t.Fatalf("a commander instruction was listed as an authorization: %+v", in)
		}
	}

	// The next commander round re-reads the ledger and still sees it.
	next, err := p.Store.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = p.Store.PrepareCommanderInstructions(next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsInstruction(rows, authz.ID, authz.Version) {
		t.Fatalf("second round lost the authorization: %+v", rows)
	}

	prompt := decider.CommanderRoundPrompt(board.View{}, "笔记", "真实订阅可测", decider.ContextStatus{}, decider.RoundContext{}, authorized)
	if !strings.Contains(prompt, contract.AuthorizationSources) {
		t.Fatal("commander prompt lost the authorization-source rule")
	}
	if !strings.Contains(prompt, "真实订阅 topic 可测") || !strings.Contains(prompt, "`"+authz.ID+"` v") {
		t.Fatalf("commander prompt does not list the operator authorization by id and text:\n%s", prompt)
	}
	// The only sanctioned reaction to a conflict is to stop and ask.
	if !strings.Contains(prompt, "停手并向操作员提问") {
		t.Fatal("commander prompt offers no stop-and-ask path for a conflict")
	}
}

func containsInstruction(rows []model.Instruction, id string, version uint64) bool {
	for _, in := range rows {
		if in.ID == id && in.Version == version {
			return true
		}
	}
	return false
}
