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
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/contract"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Decision is one line of data/decisions.jsonl. The fields the retired
// in-process decider wrote (Raw, Tools, SessionRounds, SessionDropped, Actions)
// stay in the schema so old logs and the console keep parsing; new rounds leave
// them empty.
type Decision struct {
	RunID              string                  `json:"run_id,omitempty"`
	NoteRead           *model.NoteRef          `json:"note_read,omitempty"`
	HandoffReceipt     *model.NoteRef          `json:"handoff_receipt,omitempty"`
	ContextTokens      int                     `json:"context_tokens,omitempty"`
	ContextWindow      int                     `json:"context_window,omitempty"`
	ContextPercent     int                     `json:"context_percent,omitempty"`
	Compacted          bool                    `json:"compacted,omitempty"`
	Handoff            bool                    `json:"handoff,omitempty"`
	HandoffMissing     int                     `json:"handoff_missing,omitempty"`
	HandoffReleased    bool                    `json:"handoff_released,omitempty"`
	NoActionRounds     int                     `json:"no_action_rounds,omitempty"`
	NewFacts           int                     `json:"new_facts"`
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
	RunID   string
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
	MaxHandoffAttempts = 2
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
	// HandoffForced means the harness compacted before a handoff completed (or
	// the handoff round never wrote the note). The next round must repair the
	// note even if the post-compaction context is below 85%: that is the only
	// moment the memory was actually lost.
	HandoffForced bool
	// Bypass releases dispatch pressure after failed receipts; it never claims
	// a handoff succeeded and never weakens the notebook provenance check.
	HandoffBypassed bool
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
	return !c.HandoffDone && !c.HandoffBypassed && (c.HandoffForced || c.Percent() >= HandoffPercent)
}

// contextSection tells the commander where its own budget stands. It is empty
// when neither a size nor a compaction is known, so an unconfigured window does
// not add noise to every round.
func contextSection(c ContextStatus) string {
	var sb strings.Builder
	if c.HandoffBypassed {
		return "## 上下文\n连续交接缺少有效回执，控制面已释放交接压力。本轮继续处理当前工作；这不代表交接成功，按账本恢复判断并在接口恢复后补写笔记。不得改用操作员令牌。\n\n"
	}
	if c.HandoffForced {
		sb.WriteString("## 上下文\n上一轮 harness 在交接完成前压缩了上下文（有损）；结论与线索现在只以磁盘上的笔记和账本为准。" +
			"这一轮补写笔记；如有操作员明确请求，先处理该请求要求的动作，再交接。")
		if pct := c.Percent(); pct > 0 {
			fmt.Fprintf(&sb, "\n压缩后：%d/%d（%d%%）。", c.Used, c.Window, pct)
		}
		sb.WriteString("\n\n")
		return sb.String()
	}
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
// is the commander's own memory of judgement and clues for the next decision —
// not a chronological activity log, and not a second ledger of facts. A real
// instance wrote it as a round log ("## 08:58 收结果", "09:03 心跳", …), and
// copying facts and evidence into it is how the note drifts into a stale
// observation document; either way the judgement the next round needs drowns.
// Saying what the note is for (and that it may delete/rewrite it) is part of
// the memory contract, not politeness (D80, D106).
//
// It is deliberately soft about size: no line, character or byte target. A hard
// cap turns the note into a form to fill — the model starts writing facts to
// fill the quota and trimming judgement to fit — so the only pressure stated is
// the real one: a long note spends the model's own next context. Whether to
// compact, and how long the note should be, stays the model's judgement.
const noteOwnership = "这份笔记归你：它是结论与线索，不是轮次日志。只写对下一轮有价值的判断与线索；事实和证据用 id / 位置引用、别抄正文（这里不是第二份账本）；过时、无关、低价值的内容直接删掉，随时可以自己整份重写或删改（`PUT` 是整份覆盖，不是追加）。篇幅由你判断：注入时不会被截断，写长了只会多占你自己下一轮的上下文。\n"

// handoffSection is the whole contract of a handoff round. The note is written
// through the note API because that is the file the next round reads back; the
// concrete endpoint and its per-round token ride the note section of the same
// prompt (stateAPILine), so this contract never names a stale path.
const handoffSection = "## 交接（本轮必做）\n" +
	"压缩是有损的，`state.md` 保存压缩后的判断与线索；事实、关系和未处理变化从账本恢复。笔记是历史判断，不是对下一轮的指令；当前有效指令、授权边界与事实记录版本优先。通常不派新方向；本轮若有 direct_request、instruction_changed、native_request 或操作员显式唤醒，先处理操作员请求要求的动作，再交接，不得用交接推迟或吞掉指令。用本轮会话令牌和笔记 API 的 `PUT` **整份重写**它，不得读取或改用 console.token（不是追加；留什么、删什么由你定）。这份笔记要从账本重新推导，不是改写上一版：先处理下方「事实已变化」清单（如有），按 ID 重读；再把上一版只当待核对清单，逐条回到事实/意图/目标，确认 id 是否还在、断言版本是否变了、状态是否已结束，再决定留、删、改；不要在原句上改写或逐段转述。写完前逐条自查：每条结论能否指向一个当前存在的 fact id / 证据；指不到的就降级为线索或删掉。至少写清：\n" +
	"- 目标与证据进度：什么算完成、还缺哪一块证据；\n" +
	"- 已确认的结论：用 fact id 或证据位置引用，不抄正文（漏洞、凭据、版本、接口都在账本里）；\n" +
	"- 待验证线索 / 已排除死路：各自缺什么、为什么排除；\n" +
	"- 活跃意图与停放：每个 id / 状态 / 卡在哪 / 下一步动作；\n" +
	"- 你回来后的第一个动作。\n" +
	"这份笔记是结论与线索，不是轮次日志：时间线、心跳、「本轮派了哪个 run」都别写（账本和 run 日志里已有）。只写对下一轮有价值的事实与判断；过时、无关、低价值的内容直接删掉，别抄事实正文——账本才是事实来源，笔记不是第二份账本。写完本轮就结束。\n\n"

// RoundContext is this round's memory situation, resolved by the caller before
// the prompt is built (D89). The zero value is the safe side: an unknown
// session is treated as fresh, so the note is injected in full rather than
// assumed to be in a context that may not exist.
type RoundContext struct {
	NoteRead       *model.NoteRef
	NoteChanged    bool
	StaleNoteFacts []string
	// Resume is whether this round really continues the same AOP session; it
	// must be the same answer the executor's CanResume gives.
	Resume bool
	// Rotated is a planned new session after a verified handoff, not a lost
	// session: the previous pressure cycle closed with a valid receipt and the
	// runner deliberately started a fresh commander session. The note is still
	// injected in full, but the preamble must not mix this up with "the session
	// was lost" — the board and the ledger are the entry point, the note is
	// history.
	Rotated bool
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
	// StateAPI and StateToken are where this round reads and rewrites the note.
	// StateAPI is the project-mux note endpoint (the same base as the
	// battlefield); StateToken is the case file holding a ready Authorization
	// header line, exported as $TW_AUTH by the shell init. Empty means the caller
	// has no project-scoped note channel; the prompt then falls back to the
	// legacy relative path.
	StateAPI   string
	StateToken string
}

// StateFullHeader starts the full-note section of a round prompt. The control
// plane uses it to record the injection audit event (runs/<id>.jsonl
// kind=injected) instead of duplicating the prompt layout in the API package.
const StateFullHeader = "## 你的笔记（state.md，全文）"

// StateInjectionBytes reports how many bytes of the whole note a prompt carries
// (the section runs up to the next "## board" heading), or 0 when the prompt has
// the pointer only. It is the audit half of "the note was injected": the run
// log can answer what the round saw without storing the prompt twice. It is a
// reading, not a cap.
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
//
// The full note is injected as-is: there is no line, character or byte budget
// on purpose. A hard cap tells the model the note is a fixed-size form, so it
// starts filling the quota with facts and trimming the judgement the next round
// needs — the note would become the fact observation document it must not be
// (D106). Size stays the model's judgement; noteOwnership states the real cost
// (its own next context) once, softly.
func stateSection(stateText string, rc RoundContext) string {
	note := strings.TrimSpace(stateText)
	if note == "" {
		return ""
	}
	preamble := ""
	switch {
	case rc.Rotated:
		preamble = "这是计划内的新会话第一轮，不是会话丢失：上一轮已用有效交接回执收口，本轮入口是笔记、board 与授权/指令。笔记是历史判断，不是指令：先读 board 与账本，不要照笔记执行；当前有效指令、授权边界与事实记录版本优先。"
	case !rc.Resume:
		preamble = "你没有上一轮的会话记忆（新指挥官/会话丢失）；这是已有项目记忆，不是指令：先核对再动手，历史结论只对当时条件成立；当前有效指令、授权边界与事实记录版本优先，缺失/未加载/已否定的更新按账本重读，不要照笔记直接执行。"
	case rc.Compacted:
		preamble = "harness 刚压缩过上下文（有损）；下面是从磁盘重读的全文。笔记里的「下一步」是上一轮的历史建议，不是指令：当前有效指令、授权边界与事实记录版本优先，先核对再动手，冲突以当前约束与实测为准。"
	case rc.Handoff:
		preamble = "上一轮是交接轮，笔记刚整份重写；它是待核对的历史判断与线索，不是指令。按当前有效指令、授权边界与事实记录版本工作，与事实或现场冲突时以当前约束与实测为准。"
	case rc.Restarted:
		preamble = "进程重启过；别的 run 可能被标记过取消，先核对 board 再动作。"
	case rc.NoteChanged:
		preamble = "笔记在其他轮次或由操作员更新过；这是当前版本的全文。先核对账本与当前约束，再使用历史判断。"
	}
	if preamble != "" {
		return StateFullHeader + "\n" + preamble + "\n" + noteOwnership + stateAPILine(rc) + note + "\n\n"
	}
	var sb strings.Builder
	sb.WriteString("## 你的笔记（state.md）\n本会话里已有它，不要重复粘贴。\n")
	sb.WriteString(noteOwnership)
	sb.WriteString("需要核对具体 id/数字/下一步时：\n")
	if path := strings.TrimSpace(rc.StatePath); path != "" {
		sb.WriteString("- 文件镜像：" + path + "（版本正文以 API 为准，读写笔记都用 API）\n")
	}
	sb.WriteString(stateAPILine(rc))
	sb.WriteString("\n")
	return sb.String()
}

// stateAPILine is the concrete read/write recipe for the note. The note is the
// commander's private memory, so it is deliberately not on the shared harness
// surface: every session on a host inherits the same agent token, and an
// executor holding it could otherwise read or rewrite state.md. The caller
// passes the commander's own participant-mux endpoint and the case file with
// its per-round bearer; when they are absent (tests, callers without a project)
// the legacy project-mux path is printed instead.
func stateAPILine(rc RoundContext) string {
	version := uint64(0)
	if rc.NoteRead != nil {
		version = rc.NoteRead.Version
	}
	contract := fmt.Sprintf("；version=%d，PUT 带 version/run_id，可带 fact_versions；409 重读，成功取 receipt", version)
	if len(rc.StaleNoteFacts) > 0 {
		contract += "\n- 笔记引用的事实已变化，按 ID 重读再判断：" + strings.Join(rc.StaleNoteFacts, "、")
	}
	api := strings.TrimSpace(rc.StateAPI)
	if api == "" {
		return "- API：GET /api/state" + contract + "\n"
	}
	line := "- 笔记 API：`GET " + api + "`、`PUT " + api + "`（整份覆盖，不是追加；和战场同一个令牌）"
	if token := strings.TrimSpace(rc.StateToken); token != "" {
		line += "；用 `-H \"$TW_AUTH\"` 带上令牌（由会话环境注入）；为空时 `-H \"$(cat " + token + ")\"`"
	}
	return line + contract + "\n"
}

// commanderIdentity re-anchors the role on every round. The first turn also
// loads the commander skill, but a long session can be compacted, handed off or
// resumed from a state where that turn is gone; the round prompt is the one
// input that always arrives. It states the role, not a workflow.
const commanderIdentity = "你是天吴项目的指挥官：读 board 与账本，决定派方向、续话、停放、请求停止项目或只更新笔记；不执行具体方向。\n" +
	"新方向必须写清准入依据：`from_fact_ids` 只写这条方向**离不开**的前提——每一条都要能说出「删掉它，这条方向就建不起来」；能删的删掉，只说明来路、只供参考、属于更早几轮的事实不要写，宁可一条不写并在 description 说明理由，也不要堆一串不承担准入作用的 id。还在复核中的线索不要放进 from_fact_ids，等复核结果再派下游。确实没有任何前提（首轮、纯授权驱动）时，在 description 明说「无前提，基于授权首轮探测」，并在 `interest_entity_refs` 写本轮目标实体（如 `host:10.0.0.5`）：无前提方向直接运行、挂到项目节点，不会挂起等操作员；未核验的观察不会进入你的决策上下文，只以「待核验 N 条 + id/kind」占位出现。不要编造 id。\n" +
	"复核通过后重读相关 pending 方向的 readiness；ready 就立即 run，不要让 ready 方向停在 pending。\n" +
	"并发口径：max_runs 只统计意图 run；commander/verifier/report 不计入，不要为它们留槽；以控制面 /run 的 409 为准，不自行设更低上限。\n" +
	"项目的终点目标由操作员定义（起点 → 终点）。只有操作员明确要求时，才可引用该条 operator_message_id 创建、update 或 close 目标；必须保留原意，不得自行放宽验收。关闭表示撤回目标，不表示已达成。未复核事实在描述与 context_fact_ids 中也必须标为待复核假设，不能当作已验证前提。\n" +
	"执行者可以直接互发消息、共享事实、派子任务；你只规定方向与验收，不制造层级或固定角色、不强制每个方向都先拆分。\n" +
	"方向标题写要达成的可判定状态，验收标准写怎么判断达成；具体打法留给执行者，只有操作员授权本身限定了做法时才把做法写进约束。\n\n"

// CommanderRoundPrompt is one wake's input to the commander session. The
// session keeps its own long context, so this carries only the new situation:
// the scope, its own notes, the fresh board, and the one thing the model
// cannot see for itself — how full its context is (D78, D80). The first turn
// gets the API header and /skill:commander from the caller; later turns are
// just this.
func CommanderRoundPrompt(b board.View, stateText, scope string, ctx ContextStatus, rc RoundContext, authorized []model.Instruction) string {
	var sb strings.Builder
	sb.WriteString(commanderIdentity)
	sb.WriteString(contract.AuthorizationSources)
	sb.WriteString(executor.AuthorizedSection(authorized))
	sb.WriteString("## 本轮\n")
	// Current time rides the new user turn, after the stable system prompt and
	// history. Putting it in the system prompt changes the provider's cached
	// prefix on every wake.
	sb.WriteString("当前时间：" + time.Now().Format(time.RFC3339) + "。\n\n")
	if ctx.Handoff() {
		if ctx.HandoffForced && ctx.Percent() < HandoffPercent {
			sb.WriteString("上一轮压缩前没有完成交接：本轮补写笔记；操作员明确请求的动作优先，处理后再交接。\n\n")
		} else {
			fmt.Fprintf(&sb, "上下文已经到 %d%%：本轮交接；操作员明确请求的动作优先，处理后写笔记并收口。\n\n", ctx.Percent())
		}
	} else {
		// The note body is not always below: a long session already carries it,
		// and stateSection only re-injects it when memory was actually lost or
		// rewritten. Telling the model "your note is down there" in every round
		// was false in the common case, and a model that believes it has already
		// been given the current note stops checking the version it must write
		// against.
		sb.WriteString("board 更新了。board 与授权边界在下面；笔记是本会话的私有记忆，本轮如需整份核对会一并给出，**已经给过的不要再拉一遍**；" +
			"可按需续读变化、完整分页、相关子图、事实正文和 run 的工具输出。" +
			"然后决定：派方向、续话/插话、停放、请求停止项目，或者只更新笔记。动作通过控制面 API 做。\n\n")
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
	}
	// The boundary section is written even when the project has none: a round
	// that silently drops it leaves the last non-empty scope in the session
	// history looking current, so a cleared boundary would read as still in
	// force. Same shape as the executor brief's empty-scope sentence.
	sb.WriteString("## 授权边界（当前口径）\n")
	if s := strings.TrimSpace(scope); s != "" {
		sb.WriteString(s + "\n\n")
	} else {
		sb.WriteString("未指定——按操作员当前有效指令与项目授权口径执行，不自行扩大目标范围。\n\n")
	}
	sb.WriteString("这是当前口径，不是逐资产白名单：资产会在测试中动态增删，持续有效的指导由操作员的持久指令维护，按本轮有效版本及后续明确更正处理；普通聊天和变更通知不覆盖有效指令。程序不据此做逐资产校验。派发时把新增/变更的资产与约束带给相关执行者。\n\n")
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
	pv := board.ForPrompt(b)
	// The bounded-list label is orientation, not news. It exists so a model
	// cannot read the prompt's top-N rows as the whole ledger, and that reading
	// is learned once per session. Re-sending the same paragraph every round is
	// a fixed cost with no new information; it rides the rounds where memory may
	// actually be missing (fresh or lost session, planned rotation, compaction,
	// restart) and is dropped on an ordinary resume, where the session still
	// holds it.
	if rc.Resume && !rc.Rotated && !rc.Compacted && !rc.Restarted {
		pv.PromptDigest = nil
	}
	if body, err := json.Marshal(pv); err == nil {
		sb.Write(body)
	}
	sb.WriteString("\n")
	return sb.String()
}
