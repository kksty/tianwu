// Package decider is the commander's wake-up loop: board + state in, one
// harness session per round out (D78). The decision itself is made inside the
// harness by an agent with the full tool set; this package only schedules the
// round, gates it (paused / no node / no model), and records what happened.
package decider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/store"
)

// Decision is one line of data/decisions.jsonl. The fields the retired
// in-process decider wrote (Raw, Tools, SessionRounds, SessionDropped, Actions)
// stay in the schema so old logs and the console keep parsing; new rounds leave
// them empty.
type Decision struct {
	At                 time.Time               `json:"at"`
	Round              int                     `json:"round"`
	Wake               string                  `json:"wake"`
	Digest             string                  `json:"board_digest"`
	Model              string                  `json:"model,omitempty"`
	MS                 int64                   `json:"ms"`
	Tokens             int                     `json:"tokens"`
	Fallback           string                  `json:"fallback,omitempty"`
	Error              string                  `json:"error,omitempty"`
	Raw                string                  `json:"raw,omitempty"`
	Actions            []Action                `json:"actions,omitempty"`
	Tools              []ToolUse               `json:"tools,omitempty"`
	Notes              []string                `json:"notes,omitempty"`
	Attention          []store.AttentionReason `json:"attention,omitempty"`
	AttentionThrough   uint64                  `json:"attention_through,omitempty"`
	AttentionDelivered bool                    `json:"attention_delivered,omitempty"`
	// SessionRounds / SessionDropped belonged to the local decision cache (D56,
	// retired by D78). Kept only because the console's checked-in bundle reads
	// them; new rounds always leave them at 0.
	SessionRounds  int `json:"session_rounds,omitempty"`
	SessionDropped int `json:"session_dropped,omitempty"`
}

// Action is one thing a round did, as the console shows it. Commander rounds
// act through the control-plane API, so this is filled only when the API side
// reports back; it stays part of the log schema.
type Action struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

// ToolUse is one read made by the retired in-process tool loop. Kept for old
// decision logs; new rounds do not produce any.
type ToolUse struct {
	Tool  string `json:"tool"`
	Args  string `json:"args,omitempty"`
	MS    int64  `json:"ms,omitempty"`
	Error string `json:"error,omitempty"`
}

// RoundResult is what one harness commander round produced: the tokens it
// spent and the ledger actions it performed through the control-plane API
// (recorded by the API, not guessed from the model's output).
type RoundResult struct {
	Tokens  int
	Actions []Action
	// ContextTokens is the session's context size after the round (0 when the
	// harness did not report one). The next round measures its handoff line
	// against it.
	ContextTokens int
	// ContextWindow is the window the harness resolved for this round's model
	// (0 when it did not report one). It replaces the control plane's profile
	// guess from the next round on (D80).
	ContextWindow int
	// Compacted says the harness compressed the session during this round. The
	// next round then continues from state.md instead of re-deriving.
	Compacted bool
}

// HandoffPercent is the line where a round stops dispatching and rewrites the
// note. The harness compacts near the window on its own; 85% leaves one round
// to write the note before that happens. It mirrors sapper's 85/90 protocol
// (D80): the note, not the context, is the memory that survives compaction.
const (
	HandoffPercent     = 85
	HandoffWarnPercent = 80
)

// ContextStatus is the commander's own context budget as the control plane
// sees it. The harness reports context_tokens and may compact the session
// silently; the model can see neither, so every round tells it where it stands
// and whether the last round lost memory (D80).
type ContextStatus struct {
	Used      int  // context tokens after the previous round; 0 = unknown
	Window    int  // configured context window; 0 = unknown
	Compacted bool // the harness compressed the session during the previous round
	// HandoffDone says the handoff round already ran in this pressure cycle.
	// Sapper rewrites STATE once at 85% and then continues the loop; repeating
	// the handoff every round above the line would spend the rest of the budget
	// writing the same note while nothing gets dispatched.
	HandoffDone bool
}

// Percent is 0 when either side is unknown: an unknown budget must read as
// "no pressure signal", not as 0% or 100%.
func (c ContextStatus) Percent() int {
	if c.Used <= 0 || c.Window <= 0 {
		return 0
	}
	return c.Used * 100 / c.Window
}

// Handoff reports whether this round is the 85% round. It is true once per
// pressure cycle: the round after the handoff keeps working until the harness
// compacts and the cycle resets.
func (c ContextStatus) Handoff() bool {
	return !c.HandoffDone && c.Percent() >= HandoffPercent
}

// contextSection tells the commander where its own budget stands. It is empty
// when neither a size nor a compaction is known, so an unconfigured window does
// not add noise to every round.
func contextSection(c ContextStatus) string {
	var sb strings.Builder
	switch {
	case c.Compacted:
		sb.WriteString("## 上下文\n上一轮 harness 压缩过上下文（有损，细节可能已经没了）。" +
			"下面的笔记是刚从磁盘读的；它记录当时条件与判断；按当前授权和新观察自行决定下一步。")
		if pct := c.Percent(); pct > 0 {
			fmt.Fprintf(&sb, "\n压缩后：%d/%d（%d%%）。", c.Used, c.Window, pct)
		}
	case c.Percent() > 0:
		fmt.Fprintf(&sb, "## 上下文\n上一轮结束时约 %d/%d（%d%%）。", c.Used, c.Window, c.Percent())
		switch {
		case c.HandoffDone:
			sb.WriteString("\n这个压力周期已经跑过交接轮（不再重复重写同一份笔记）。继续推进；" +
				"harness 到压缩线会自己压缩，压缩后的第一轮会从笔记接着做。")
		case c.Percent() >= HandoffWarnPercent && !c.Handoff():
			fmt.Fprintf(&sb, "\n已接近交接线（%d%%）：新事实与结论随手写进笔记，过时的内容随手删掉；一旦觉得前面的细节开始模糊，"+
				"立即整份重写笔记并收口。", HandoffPercent)
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	sb.WriteString("\n\n")
	return sb.String()
}

// noteOwnership is what both note branches say about the file itself. The note
// is the commander's own memory of facts and judgement for the next decision —
// not a chronological activity log. A real instance wrote it as one ("## 08:58
// 收结果", "09:03 心跳", …): the valuable findings drown in round records, and
// the model keeps imitating the shape it last wrote. Saying what the note is
// (and that it may delete/rewrite it) is part of the memory contract, not
// politeness (D80, D106).
const noteOwnership = "这份笔记归你：它是结论与线索，不是轮次日志。只写对下一轮有价值的事实与判断（带 fact id 或证据位置）；过时、无关、低价值的内容直接删掉，随时可以自己整份重写或删改（`PUT /api/state` 是整份覆盖，不是追加）。\n"

// handoffSection is the whole contract of a handoff round. The note is written
// through the API because that is the file the next round reads back; a file
// in the session's working directory would not survive (D78).
const handoffSection = "## 交接（本轮必做）\n" +
	"压缩是有损的，`state.md` 保存压缩后的判断与线索；事实、关系和未处理变化从账本恢复。笔记是给下一轮梳理的历史判断与线索，不是对下一轮的指令：下一轮的当前有效指令、授权边界与事实记录版本优先。现在停下派发，用 `PUT /api/state` **整份重写**它（不是追加；留什么、删什么由你定），至少写清：\n" +
	"- 目标与证据进度：什么算完成、还缺哪一块证据；\n" +
	"- 已确认的事实与结论：带 fact id 或证据位置（漏洞、凭据、版本、接口）；\n" +
	"- 待验证线索 / 已排除死路：各自缺什么、为什么排除；\n" +
	"- 活跃意图与停放：每个 id / 状态 / 卡在哪 / 下一步动作；\n" +
	"- 你回来后的第一个动作。\n" +
	"这份笔记是结论与线索，不是轮次日志：时间线、心跳、「本轮派了哪个 run」都别写（账本和 run 日志里已有）。只写对下一轮有价值的事实与判断；过时、无关、低价值的内容直接删掉，别抄事实正文。写完本轮就结束。\n\n"

// RoundContext is this round's memory situation, resolved by the caller before
// the prompt is built (D89). The zero value is the safe side: an unknown
// session is treated as fresh, so the note is injected in full rather than
// assumed to be in a context that may not exist.
type RoundContext struct {
	// Resume is whether this round really continues the same AOP session; it
	// must be the same answer the executor's CanResume gives.
	Resume bool
	// Compacted is whether the harness compressed the session during the
	// previous round: recent turns are gone and only the note survived.
	Compacted bool
	// Handoff is whether the previous round was the 85% handoff round that
	// rewrote the note.
	Handoff bool
	// Restarted is the first commander round after a process restart: other
	// runs may have been settled as canceled, so the board must be re-checked.
	Restarted bool
	// StatePath is where state.md lives. The runner fills it (the hub does not
	// know the file); it is only used in the pointer branch, and an empty path
	// drops that line instead of inventing one.
	StatePath string
}

// StateFullHeader starts the full-note section of a round prompt. The control
// plane uses it to record the injection audit event (runs/<id>.jsonl
// kind=injected) instead of duplicating the prompt layout in the API package.
const StateFullHeader = "## 你的笔记（state.md，全文）"

// StateFullMaxLines is the injection budget for the whole note when memory was
// lost. The note on disk is untouched: a longer one is clipped at a line
// boundary for this prompt only, so one runaway note cannot crowd out the board,
// messages and tier guidance; the round is told to rewrite it. 150 lines is the
// same discipline the commander skill states, now enforced at the injection
// boundary instead of being advice that a compacted session may have forgotten.
const StateFullMaxLines = 150

// clipStateLines bounds the injected note at a line boundary and reports whether
// it cut. It never edits the stored note: the next PUT /api/state is the model's
// chance to rewrite it.
func clipStateLines(note string) (string, bool) {
	lines := strings.Split(note, "\n")
	if len(lines) <= StateFullMaxLines {
		return note, false
	}
	return strings.Join(lines[:StateFullMaxLines], "\n"), true
}

// StateInjectionBytes reports how many bytes of the whole note a prompt carries
// (the section runs up to the next "## board" heading), or 0 when the prompt has
// the pointer only. It is the audit half of "the note was injected": the run
// log can answer what the round saw without storing the prompt twice.
func StateInjectionBytes(prompt string) int {
	start := strings.Index(prompt, StateFullHeader)
	if start < 0 {
		return 0
	}
	section := prompt[start:]
	if end := strings.Index(section, "\n## board\n"); end >= 0 {
		section = section[:end]
	}
	return len([]byte(section))
}

// stateSection decides whether the round gets the whole note or just a pointer
// to it. A long commander session already carries the note in its context;
// re-pasting it every round is exactly the token waste the harness's own
// compaction exists to avoid. The full text is for the states where memory was
// actually lost or rewritten.
func stateSection(stateText string, rc RoundContext) string {
	note := strings.TrimSpace(stateText)
	if note == "" {
		return ""
	}
	preamble := ""
	switch {
	case !rc.Resume:
		preamble = "你没有上一轮的会话记忆（新会话/换模型/会话丢失）；这是已有项目记忆，不是指令：先核对再动手，历史结论只对当时条件成立；当前有效指令、授权边界与事实记录版本优先，缺失/未加载/已否定的更新按账本重读，不要照笔记直接执行。"
	case rc.Compacted:
		preamble = "harness 刚压缩过上下文（有损）；下面是从磁盘重读的全文。笔记里的「下一步」是上一轮的历史建议，不是指令：当前有效指令、授权边界与事实记录版本优先，先核对再动手，冲突以当前约束与实测为准。"
	case rc.Handoff:
		preamble = "上一轮是交接轮，笔记刚整份重写；它是待核对的历史判断与线索，不是指令。按当前有效指令、授权边界与事实记录版本工作，与事实或现场冲突时以当前约束与实测为准。"
	case rc.Restarted:
		preamble = "进程重启过；别的 run 可能被标记过取消，先核对 board 再动作。"
	}
	if preamble != "" {
		full, clipped := clipStateLines(note)
		out := StateFullHeader + "\n" + preamble + "\n" + noteOwnership + full + "\n\n"
		if clipped {
			out += "（笔记超过 150 行，本轮只注入了前 150 行；请整份重写，只保留对下一轮有价值的内容。）\n\n"
		}
		return out
	}
	var sb strings.Builder
	sb.WriteString("## 你的笔记（state.md）\n本会话里已有它，不要重复粘贴。\n")
	sb.WriteString(noteOwnership)
	sb.WriteString("需要核对具体 id/数字/下一步时：\n")
	if path := strings.TrimSpace(rc.StatePath); path != "" {
		sb.WriteString("- 文件：" + path + "（要改用 API，别用文件工具写它）\n")
	}
	sb.WriteString("- API：GET /api/state\n\n")
	return sb.String()
}

// tieringSection describes available resources, not attack or escalation policy.
const tieringSection = "## 模型档位\n" +
	"项目 worker_profile 是未显式选择 profile 时的执行者默认；未设置则使用实例 active profile。\n" +
	"项目 strong_profile 选择指挥官模型。GET /api/model-profiles 返回可用配置，选择 runnable=true 的项。\n" +
	"tier=strong / tier=worker 只是模型配置的资源标注，不决定职责或委派能力；同一种模型可以承担全部职责。\n" +
	"轻量预设职责，动态形成组织，按需要委派，不追求层级齐全；能由一个执行者完成就直接完成。\n" +
	"分工、委派、换模型与渗透路径由你根据授权和目标自主判断，没有按次数或费用规定的升级流程。\n\n"

// commanderIdentity re-anchors the role on every round. The first turn also
// loads the commander skill, but a long session can be compacted, handed off or
// resumed from a state where that turn is gone; the round prompt is the one
// input that always arrives. It states the role, not a workflow.
const commanderIdentity = "你是天吴项目的指挥官：读 board 与账本，决定派方向、续话、停放或只更新笔记；不执行具体方向。\n" +
	"新方向必须写清依据哪些已记录的既有事实（from_fact_ids）；确实没有任何事实可用（首轮、纯授权驱动）时，在 description 明说「无前提，基于授权首轮探测」，不要编造 id。\n" +
	"项目还没有目标时，先按项目简报划分起点与终点，用 POST /api/goals 建 1–3 条可判真假的目标（statement/quantified_target），再派方向；已有目标不要重复建。\n" +
	"执行者可以直接互发消息、共享事实、派子任务并自行选择模型档位；你只规定方向与验收，不制造层级或固定角色、不强制每个方向都先拆分。\n\n"

// CommanderRoundPrompt is one wake's input to the commander session. The
// session keeps its own long context, so this carries only the new situation:
// the scope, its own notes, the fresh board, and the one thing the model
// cannot see for itself — how full its context is (D78, D80). The first turn
// gets the API header and /skill:commander from the caller; later turns are
// just this.
func CommanderRoundPrompt(b board.View, stateText, scope string, ctx ContextStatus, rc RoundContext) string {
	var sb strings.Builder
	sb.WriteString(commanderIdentity)
	sb.WriteString("## 本轮\n")
	if ctx.Handoff() {
		fmt.Fprintf(&sb, "上下文已经到 %d%%：**这一轮先交接，不派新方向**，写完笔记就收口。\n\n", ctx.Percent())
	} else {
		sb.WriteString("board 更新了。board、授权边界和你的笔记都在下面，**不要再拉一遍**；" +
			"可按需续读变化、完整分页、相关子图、事实正文和 run 的工具输出。" +
			"然后决定：派方向、续话/插话、停放，或者只更新笔记。动作通过控制面 API 做。\n\n")
		// The round is the unit the decider schedules and the control plane
		// wakes: a decision. Stating that contract here is what stops a long
		// session from parking itself inside a round to wait for executor
		// results — those results are ledger rows, and the wake they cause can
		// only run after this round returns (docs/06 D91).
		sb.WriteString("这一轮的工作就是这一次决定：该查的查完、决定里的动作发完，本轮就结束了。" +
			"执行者/复核的结果不在本轮的输入里——它们先写进账本，控制面在它们结束时唤醒下一轮，" +
			"到那一轮你会在新的 board 和未读消息里看到。把本轮留着等结果，只会让那些唤醒排队。\n\n")
	}
	sb.WriteString(contextSection(ctx))
	if ctx.Handoff() {
		sb.WriteString(handoffSection)
	} else {
		sb.WriteString(tieringSection)
	}
	if s := strings.TrimSpace(scope); s != "" {
		sb.WriteString("## 授权边界（当前口径）\n" + s +
			"\n\n这是当前口径，不是逐资产白名单：资产会在测试中动态增删，持续有效的指导由操作员的持久指令维护，按本轮有效版本及后续明确更正处理；普通聊天和变更通知不覆盖有效指令。程序不据此做逐资产校验。派发时把新增/变更的资产与约束带给相关执行者。\n\n")
	}
	sb.WriteString(stateSection(stateText, rc))
	// Pointer-only capability inventory: the commander session is long, so the
	// new read/selection entries are named but never inlined. Facts, bodies and
	// pages stay behind their APIs.
	sb.WriteString("## 可续读入口\n")
	sb.WriteString("- 某方向的焦点/冷层：`GET /api/focus?intent=in_x`（`&layer=cold` 读旧事实）；焦点行带入选原因、状态与按 id 查询入口。\n")
	sb.WriteString("- 执行现场资源：`GET /api/resources?intent=in_x`（浏览器租约、材料、人工等待、外部动作、会话的持有者/状态/版本；读取不获取也不重放）。\n")
	sb.WriteString("- 调整某方向的显式参考/固定/兴趣实体：`PUT /api/intents/in_x/context`，正文带 `version`（从意图行读 `context_version`）。\n")
	sb.WriteString("- run 的 `premise_claim_versions`/`context_fact_versions` 说明那次运行实际读到哪一版断言和参考；断言版本与事实状态不符时以事实当前状态为准。\n\n")
	sb.WriteString("## board\n")
	// The prompt gets the summary layer, not the ledger: long model-written
	// summaries are clipped, ids and counts stay complete, full text is one API
	// call away (docs/17 §3.1). The API/console board is unchanged.
	if body, err := json.Marshal(board.ForPrompt(b)); err == nil {
		sb.Write(body)
	}
	sb.WriteString("\n")
	return sb.String()
}
