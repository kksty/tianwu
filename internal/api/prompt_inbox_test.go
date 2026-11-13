package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestPromptPlacesInstructionsBeforeTaskMaterial(t *testing.T) {
	db := testDB()
	in := db.Intents[0]
	instructions := "\n## 当前持久指令\nINSTRUCTION-MARKER\n"
	prompt := BuildPromptWithInstructions(db, in, "10.0.0.0/24", "", "", 0, true, true, "", instructions)
	at := func(marker string) int { return strings.Index(prompt, marker) }
	identity, instr, scope, intent, handoff := at("你是授权渗透测试的执行者"), at("INSTRUCTION-MARKER"), at("## 授权边界（当前口径）"), at("## 意图"), at("## 交接（必做）")
	if instr < 0 || scope < 0 || intent < 0 || handoff < 0 {
		t.Fatalf("prompt section missing: %q", prompt)
	}
	if !(identity < instr && instr < scope && scope < intent && intent < handoff) {
		t.Fatalf("section order: identity=%d instr=%d scope=%d intent=%d handoff=%d", identity, instr, scope, intent, handoff)
	}
	// Both writers survive in one merged pointer section.
	for _, want := range []string{"## 本轮输入文件（只读）", ".tw/graph.json", ".tw/focus.json", ".tw/context.json", "不要把整份文件读进上下文"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("input pointer missing %q", want)
		}
	}
}

func TestInboxSeparatesMechanicalNoticesFromConversation(t *testing.T) {
	s, _ := newBindingServer(t)
	const to = "b"
	for i := 0; i < 12; i++ {
		if _, _, err := s.Store.AddMessage(model.Message{
			FromParticipantID: "a", ToParticipantID: to,
			Text: "message-" + twoDigits(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i, factID := range []string{"f_notice_1", "f_notice_2"} {
		if _, _, err := s.Store.AddMessage(model.Message{
			FromParticipantID: model.NoticeSenderID, ToParticipantID: to,
			Kind: model.NoticeKindFact, Durable: true,
			Text: "共享发现变化：" + factID, NoticeFactID: factID, NoticeVersion: uint64(i + 2),
		}); err != nil {
			t.Fatal(err)
		}
	}
	inbox := s.inboxSection(to, "prompt:fixture", "r_fixture", "结束语", 0)
	if !strings.Contains(inbox, "message-00") || strings.Contains(inbox, "message-10") {
		t.Fatalf("normal messages not drained oldest-first:\n%s", inbox)
	}
	if !strings.Contains(inbox, "## 共享发现变化（机械通知）") ||
		!strings.Contains(inbox, "from="+model.NoticeSenderID) ||
		!strings.Contains(inbox, "f_notice_1 v2") || !strings.Contains(inbox, "f_notice_2 v3") {
		t.Fatalf("notice block is not a compact pointer:\n%s", inbox)
	}
	if strings.Index(inbox, "message-00") > strings.Index(inbox, "## 共享发现变化") {
		t.Fatalf("notices displaced the conversation:\n%s", inbox)
	}
	next := s.inboxSection(to, "prompt:next", "r_next", "", 0)
	if !strings.Contains(next, "message-10") || strings.Contains(next, "message-00") {
		t.Fatalf("queued tail was not carried by the next prompt:\n%s", next)
	}
	if again := s.inboxSection(to, "prompt:third", "r_third", "", 0); again != "" {
		t.Fatalf("rows re-injected after they were delivered:\n%s", again)
	}
}

// inboxSection keeps its own section-level caps (row count + byte budget). This
// is a prompt-construction bound owned by Tianwu; it is deliberately not a model
// window prediction (D152 revised).
func TestInboxRespectsItsOwnSectionCaps(t *testing.T) {
	s, _ := newBindingServer(t)
	const to = "b"
	for i := 0; i < 6; i++ {
		if _, _, err := s.Store.AddMessage(model.Message{
			FromParticipantID: "a", ToParticipantID: to,
			Text: "message-" + twoDigits(i) + strings.Repeat("x", 80),
		}); err != nil {
			t.Fatal(err)
		}
	}
	tight := s.inboxSection(to, "prompt:tight", "r_tight", "", 260)
	if tight == "" {
		t.Fatal("the explicit section bound shrank the inbox to nothing")
	}
	if strings.Count(tight, "\n- ") > 5 {
		t.Fatalf("the explicit section bound did not reduce the injected rows:\n%s", tight)
	}
	wide := s.inboxSection(to, "prompt:wide", "r_wide", "", 0)
	if wide == "" {
		t.Fatal("remaining queued rows were lost")
	}
}

// The notice formatter is shared by IOA delivery and the prompt block; both
// must carry the same pointer-only contract.
func TestNoticeFormattingIsPointerOnly(t *testing.T) {
	row := model.Message{ID: "m_1", FromParticipantID: model.NoticeSenderID, Kind: model.NoticeKindFact,
		Text: "共享发现变化：事实 f_x", NoticeFactID: "f_x", NoticeVersion: 4, Coalesced: 2}
	line := formatFactNoticeLine(row)
	if line != "f_x v4（合并 2 次）" {
		t.Fatalf("notice line = %q", line)
	}
	live := formatPeerMessage(row)
	if !strings.Contains(live, "[共享发现]") || !strings.Contains(live, "fact=f_x") || !strings.Contains(live, "v=4") {
		t.Fatalf("live notice = %q", live)
	}
	if strings.Contains(formatInboxMessage(row), "[消息]") {
		t.Fatal("a notice was rendered as peer chat")
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
