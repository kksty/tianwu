package api

// Prompt/role contract tests (audit F12, items 1–6). They pin the contract
// text, not an implementation detail: authorization has one rule across roles,
// verified_only never suggests a bypass, the worker brief has no operator-owned
// project goals, the declaration schema has one source, notes are history rather
// than orders, and a continuation never promises a written index that may have
// failed.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// readSkillAsset reads one shipped skill body. The skills are file content under
// internal/skills/assets; the test runs in the api package directory.
func readSkillAsset(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "skills", "assets", name, "SKILL.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill %s: %v", name, err)
	}
	return string(b)
}

// F12.1: the worker brief, the shared instruction section and both skills state
// one authorization rule. The latest ordinary operator message may add or narrow
// work; it does not silently revoke persistent instructions, and the project
// authorization scope is the hard boundary.
func TestAuthorizationRuleIsUnified(t *testing.T) {
	prompt := BuildPrompt(testDB(), testDB().Intents[0], "10.0.0.0/24", "", "", 0, false)
	instruction := instructionSection(nil, "r_1")
	commanderSkill := readSkillAsset(t, "commander")
	collaborateSkill := readSkillAsset(t, "collaborate")

	authorization := []struct {
		name string
		text string
	}{
		{"worker brief", prompt},
		{"instruction section", instruction},
		{"commander skill", commanderSkill},
		{"collaborate skill", collaborateSkill},
	}
	for _, tt := range authorization {
		for _, want := range []string{
			// A newer ordinary message is supplemental, not a revocation.
			"不会自动撤销",
			// The persistent instruction set is a hard constraint.
			"持久指令",
			// The project scope is the boundary every role shares.
			"授权边界",
		} {
			if !strings.Contains(tt.text, want) {
				t.Errorf("%s lost the unified authorization rule %q:\n%s", tt.name, want, tt.text)
			}
		}
	}

	// The contradictory rule "the latest operator message wins" must be gone:
	// it turned a chat message into an implicit revocation of a persistent
	// instruction and made the scope look like a per-message whitelist.
	for _, banned := range []string{"以操作员最新一条为准", "以最新消息为准", "以操作员最新指令为准"} {
		for _, tt := range authorization {
			if strings.Contains(tt.text, banned) {
				t.Errorf("%s still says %q", tt.name, banned)
			}
		}
	}
	// The empty-scope fallback is the same sentence the UI shows.
	noScope := BuildPrompt(testDB(), testDB().Intents[0], "", "", "", 0, false)
	if !strings.Contains(noScope, "按操作员当前有效指令与项目授权口径执行") {
		t.Errorf("empty-scope fallback is not the unified sentence:\n%s", noScope)
	}
}

// F12.2: verified_only must not teach "move the required fact into
// context_fact_ids to get past admission". A real premise waits for review; an
// unverified lead can spawn an independent verification direction instead.
func TestVerifiedOnlyGuidanceSeparatesPremisesFromLeads(t *testing.T) {
	skill := readSkillAsset(t, "commander")

	for _, banned := range []string{
		"把它放进 `context_fact_ids` 当参考",
		"或等复核通过再派",
	} {
		if strings.Contains(skill, banned) {
			t.Errorf("verified_only guidance still suggests a bypass: %q", banned)
		}
	}
	for _, want := range []string{
		"真正必要的条件不能靠改标签/换字段放行",
		"把它变成阅读参考",
		"未验证线索与必要前提不同",
		"单独立一条「验证该线索」的方向",
		"不要为了派下游动作把它塞进 `context_fact_ids`",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("verified_only guidance missing the premise/lead distinction %q:\n%s", want, skill)
		}
	}
}

// F12.4: the mandatory handoff contract is the one place that defines the
// declaration schema; the collaborate skill defers to it instead of repeating a
// competing example.
func TestHandoffContractIsTheSingleDeclarationSchema(t *testing.T) {
	var b strings.Builder
	writeHandoff(&b)
	handoff := b.String()

	for _, want := range []string{
		`"declaration_id":"…"`,
		`"entity_ref":"host:10.0.0.5"`,
		`"source_ref":"…"`,
		`"from_fact_ids":["f_x"]`,
		`"tags":["technique:rce"]`,
		`"evidence":["api-users.json"]`,
		`"observed_at"`,
		`"execution_context_ref"`,
		"声明字段（唯一来源",
		"`entity_ref`：被观察的**实体身份**",
	} {
		if !strings.Contains(handoff, want) {
			t.Errorf("mandatory handoff schema missing %q:\n%s", want, handoff)
		}
	}

	collaborate := readSkillAsset(t, "collaborate")
	for _, want := range []string{"本轮任务书的「交接（必做）」", "不重复定义"} {
		if !strings.Contains(collaborate, want) {
			t.Errorf("collaborate skill does not defer to the handoff contract (%q):\n%s", want, collaborate)
		}
	}
	// A second, competing declaration example in the skill would recreate the
	// multiple-sources problem this item exists to remove.
	if strings.Contains(collaborate, `"declaration_id":"…"`) {
		t.Error("collaborate skill carries a second declaration JSON example")
	}
}

// F12.5: a recovered worker note is memory and historical advice, not an
// instruction; current persistent instructions and fact record versions win.
func TestWorkerNoteNextStepIsHistoricalAdvice(t *testing.T) {
	note := "下一步：导出订单并留原始响应"
	p := BuildPromptWithWorkerNote(testDB(), testDB().Intents[0], "", "", "", 0, false, note)

	for _, want := range []string{
		"不是事实，也不是指令",
		"历史建议",
		"当前有效持久指令",
		"事实记录版本",
		"未加载/未确认的保持未知",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("worker-note contract missing %q:\n%s", want, p)
		}
	}
	for _, banned := range []string{"以它的下一步为准", "照它执行", "以 facts 与现场为准，冲突以实测为准"} {
		if strings.Contains(p, banned) {
			t.Errorf("worker-note contract still turns history into an order: %q", banned)
		}
	}
	// The note body itself is still memory the worker needs.
	if !strings.Contains(p, note) {
		t.Errorf("worker note body was dropped:\n%s", p)
	}
}

// F12.6: continuation/wake prompts are built before the optional indexes are
// written, so they must not claim a refreshed graph. The dispatcher appends the
// truthful line only when the write succeeded; here the file is an input that
// may exist from an earlier dispatch.
func TestContinuationPromptsDoNotPromiseUnwrittenIndexes(t *testing.T) {
	s, _ := newBindingServer(t)
	in := model.Intent{ID: "in_x", Title: "继续", RunID: "r_prev"}
	prev := model.Run{ID: "r_prev", Status: model.RunFailed, StopReason: "completed"}

	prompts := map[string]string{
		"retry resume": s.retryResumePrompt(in, prev, ""),
		"continue":     s.continuePrompt(in, prev, "", ""),
		"fresh wake":   s.wakePrompt(in, false),
	}
	for name, prompt := range prompts {
		for _, banned := range []string{"本轮已刷新", "已刷新", "本轮已写"} {
			if strings.Contains(prompt, banned) {
				t.Errorf("%s prompt claims an index write that may not have happened (%q):\n%s", name, banned, prompt)
			}
		}
		if !strings.Contains(prompt, "若存在") {
			t.Errorf("%s prompt does not describe the graph as a possibly-older input:\n%s", name, prompt)
		}
	}
	if !strings.Contains(prompts["fresh wake"], "不要把整份文件读进上下文") {
		t.Error("fresh wake prompt lost the bounded-index warning")
	}
}

// promptCaptureExec records each prompt without writing a ledger declaration.
// Run executes on the dispatch goroutine, so the slice is mutex-guarded: tests
// poll it while the run is still finishing, and an unguarded read is a data race
// under -race.
type promptCaptureExec struct {
	mu      sync.Mutex
	prompts []string
}

func (e *promptCaptureExec) Name() string { return "capture" }

func (e *promptCaptureExec) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.prompts = append(e.prompts, req.Prompt)
	e.mu.Unlock()
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *promptCaptureExec) promptAt(i int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.prompts) {
		return ""
	}
	return e.prompts[i]
}

func (e *promptCaptureExec) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.prompts)
}

// F12.6 (second half): a handoff brief must say the snapshot is a capped index,
// never "all known context".
func TestHandoffBriefDoesNotClaimCompleteContext(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &promptCaptureExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.SkillsDir = filepath.Join(dir, "skills")

	in, err := st.CreateIntent(model.Intent{Title: "接手", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	in, err = st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Handoff = &model.Handoff{FromRun: "r_old", Note: "旧会话交接"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})
	if ex.count() == 0 {
		t.Fatal("handoff brief was never captured")
	}
	prompt := ex.promptAt(0)
	if !strings.Contains(prompt, handoffIndexNote) {
		t.Fatalf("handoff brief is missing the index caveat:\n%s", prompt)
	}
	for _, want := range []string{"不是全部已知信息", "不是完整账本", "按 ID 或分页从账本续读"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("handoff brief does not bound the snapshot %q:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"全部已知上下文", "以下是全部", "包含所有已知"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("handoff brief claims complete context: %q", banned)
		}
	}
}

// D152 revised: the harness-context-limit slim brief keeps every hard
// constraint and replaces the optional summaries with id/API pointers instead of
// clipping them silently.
func TestSlimBriefKeepsHardConstraints(t *testing.T) {
	db := testDB()
	db.Facts[0].Summary = strings.Repeat("很长的摘要", 500)
	db.Playbooks = []model.Playbook{{
		ID: "pb_1", Title: "历史打法", When: "when", Doc: strings.Repeat("doc ", 400),
		Status: model.PlaybookConfirmed,
	}}
	db.Goals = []model.Goal{{ID: "g_1", Statement: "全局目标标记"}}
	in := db.Intents[0]
	in.Eval = "验收标准标记"
	in.FromFactIDs = []string{"f_1"}
	instructions := "\n## 当前持久指令\n指令标记\n"

	full := BuildPromptWithInstructions(db, in, "10.0.0.0/24", "", "", 0, true, true, "笔记标记", instructions)
	slim := buildPromptWithoutOptionalSummaries(db, in, "10.0.0.0/24", "", "", 0, true, true, instructions)

	// Hard material survives the degradation, whole.
	for _, want := range []string{
		"指令标记",
		"授权边界（当前口径）",
		"10.0.0.0/24",
		"## 意图",
		"验收标准标记",
		".tw/context.json",
		"## 纪律",
		// The endpoint goal is operator-owned; even the degraded brief must keep
		// the rule that a session cannot create, rewrite or append one.
		"项目终点目标由操作员定义",
		"你不创建、改写或追加目标",
		"## 证据（原始材料）",
		"## 交接（必做）",
		"声明字段（唯一来源",
	} {
		if !strings.Contains(slim, want) {
			t.Errorf("degraded brief dropped hard material %q:\n%s", want, slim)
		}
	}
	// Optional summaries are gone and named as omissions with read paths.
	for _, banned := range []string{"很长的摘要", "历史打法", "全局目标标记", "笔记标记"} {
		if strings.Contains(slim, banned) {
			t.Errorf("degraded brief still carries optional material %q", banned)
		}
	}
	for _, want := range []string{"本轮可选摘要有界省略", "按 `.tw/context.json` 的前提目录和 fact id 读取", "未列出或截断不表示不存在"} {
		if !strings.Contains(slim, want) {
			t.Errorf("degraded brief does not point at the omitted material (%q):\n%s", want, slim)
		}
	}
	if runeLen(slim) >= runeLen(full) {
		t.Fatalf("degraded brief is not smaller: %d vs %d runes", runeLen(slim), runeLen(full))
	}
}

// F10: bounded optional lists say where the full list is, so a cut is an
// omission with a stable pointer, not a silent disappearance.
func TestTruncatedBriefListsPointAtTheirSource(t *testing.T) {
	db := store.DB{Intents: []model.Intent{{ID: "in_x", Title: "当前"}}}
	for i := 0; i < maxBriefItems+5; i++ {
		db.Intents = append(db.Intents, model.Intent{
			ID: "in_p" + string(rune('a'+i)), Title: "停放方向", Status: model.StatusParked, ParkReason: "原因",
		})
	}
	p := BuildPrompt(db, db.Intents[0], "", "", "", 0, false)
	if !strings.Contains(p, "清单已截断") || !strings.Contains(p, "GET /api/intents") {
		t.Fatalf("truncated list has no stable pointer:\n%s", p)
	}
	if strings.Contains(p, "…（清单已截断）\n") {
		t.Fatal("truncated list still ends without a pointer")
	}
}
