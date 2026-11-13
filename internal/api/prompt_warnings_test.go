package api

// The next round's brief carries the previous attempt's handoff problems, so a
// path mistake is corrected instead of silently repeated (task-17). It is a
// short list of observed problems, not a workflow and not a tool tutorial.

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

const previousWarning = "事实「越权」的证据「../x」无法登记：路径越界，必须相对 case 根且真实存在"

func TestBuildPromptCarriesPreviousHandoffWarnings(t *testing.T) {
	db := store.DB{Runs: []model.Run{
		{
			ID: "r_prev", IntentID: "in_1", Status: model.RunFailed,
			StopReason: "completed", Warnings: []string{previousWarning},
		},
	}}
	in := model.Intent{ID: "in_1", Title: "继续这个方向", RunID: "r_prev"}
	p := BuildPrompt(db, in, "", "", "", 0, false)
	if n := strings.Count(p, "## 上一轮交接问题"); n != 1 {
		t.Fatalf("warning section appears %d times:\n%s", n, p)
	}
	if !strings.Contains(p, previousWarning) {
		t.Fatalf("warning text missing from the brief:\n%s", p)
	}
	if n := strings.Count(p, previousWarning); n != 1 {
		t.Fatalf("warning repeated %d times:\n%s", n, p)
	}
	// It is a separate section from the failure history, not a repeat of it.
	if !strings.Contains(p, "## 本意图历史") {
		t.Fatalf("failure history disappeared:\n%s", p)
	}
}

// A first dispatch and a previous run with nothing to report both stay quiet.
func TestBuildPromptOmitsWarningSectionWithoutPreviousWarnings(t *testing.T) {
	clean := store.DB{Runs: []model.Run{{ID: "r_prev", IntentID: "in_1", Status: model.RunDone}}}
	in := model.Intent{ID: "in_1", Title: "t", RunID: "r_prev"}
	if p := BuildPrompt(clean, in, "", "", "", 0, false); strings.Contains(p, "## 上一轮交接问题") {
		t.Fatalf("a clean previous run produced a warning section:\n%s", p)
	}
	// First dispatch: in.RunID is empty even though other runs in the ledger have
	// warnings — they are not "this direction's previous attempt".
	first := model.Intent{ID: "in_new", Title: "t"}
	db := store.DB{Runs: []model.Run{{ID: "r_other", Warnings: []string{previousWarning}}}}
	if p := BuildPrompt(db, first, "", "", "", 0, false); strings.Contains(p, "## 上一轮交接问题") {
		t.Fatalf("a first dispatch inherited another direction's warnings:\n%s", p)
	}
}

// The section is bounded: a run with many warnings must not turn the brief into
// a warning dump.
func TestBuildPromptCapsWarningList(t *testing.T) {
	many := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		many = append(many, "事实「F"+string(rune('A'+i))+"」的证据「x」无法登记：文件不存在（已作为引用保留）")
	}
	db := store.DB{Runs: []model.Run{{ID: "r_prev", Warnings: many}}}
	p := BuildPrompt(db, model.Intent{ID: "in_1", Title: "t", RunID: "r_prev"}, "", "", "", 0, false)
	if !strings.Contains(p, "其余见上一轮 run 的 warnings") {
		t.Fatalf("warning list is not bounded:\n%s", p)
	}
	if strings.Contains(p, many[len(many)-1]) {
		t.Fatalf("the cap did not drop the tail:\n%s", p)
	}
}
