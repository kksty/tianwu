package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// The commander's inbox is the same message service the executors use, with one
// difference in delivery: an intent is a short-lived run, so a message for it
// can sit queued and be delivered to the next run; the commander is a long
// session that wakes for reasons other than mail, so a queued message has to
// ride the next round's prompt instead of waiting for a run that may not come.
// Neither path adds a channel: delivery is IOA, the record is the ledger (98 §9).

// inboxLimit bounds how many queued messages one run carries. The rest stay
// queued and ride later runs, so a backlog cannot turn a prompt into the whole
// conversation (98 §9.3).
const inboxLimit = 10
const inboxByteBudget = 128 << 10

// Mechanical notices are references, not conversation. They get their own
// bounded block so a burst of fact changes cannot consume the ten message slots
// and starve an operator instruction or a peer question.
const (
	inboxNoticeLimit  = 8
	inboxNoticeBudget = 16 << 10
)

// liveCommanderRun is the commander round that is running right now, if any.
// It is the only condition under which IOA can reach the commander mid-turn.
func (s *Server) liveCommanderRun() (model.Run, bool) {
	for _, r := range s.Store.Runs() {
		if r.Purpose == "commander" && r.Status == model.RunRunning {
			return r, true
		}
	}
	return model.Run{}, false
}

// deliverToCommander puts a message into the commander's live session, or wakes
// the decider so the next round carries it. It never invents an untracked
// harness turn: IOA into an idle session would start a run the ledger does not
// know about (the reason P2 keeps idle targets queued).
func (s *Server) deliverToCommander(ctx context.Context, msg model.Message) model.Message {
	// Delivery to a live AOP session can itself trigger model work. A paused
	// project keeps the durable message queued instead of injecting a new turn.
	if s.paused.Load() || s.closing.Load() || s.Decider != nil && s.Decider.Paused() {
		return msg
	}
	run, live := s.liveCommanderRun()
	sess, hasSession := aopexec.ReadSession(s.CommanderDir())
	if !live || !hasSession || (s.Say == nil && s.SayEvent == nil) {
		if s.Decider != nil {
			if msg.FromParticipantID == collab.OperatorID {
				s.Decider.Wake("operator")
			} else {
				s.Decider.Wake("message")
			}
		}
		return msg
	}
	claimed, acquired, claimErr := s.Store.BeginMessageDelivery(msg.ID)
	if claimErr != nil {
		log.Printf("消息 %s 投递准入失败：%v", msg.ID, claimErr)
		return msg
	}
	if !acquired {
		return claimed
	}
	profile := ""
	if run.ProfileSource == "commander_strong" {
		profile = run.ResolvedProfile
	}
	deliveryID, sayErr := s.sayText(ctx, model.Intent{Node: run.Node, Profile: profile}, sess, formatInboxMessage(claimed), false, claimed.EventID)
	errText := ""
	if sayErr != nil {
		errText = sayErr.Error()
	}
	final, uerr := s.Store.FinishMessageDelivery(msg.ID, deliveryID, run.ID, "ioa", errText)
	if uerr != nil {
		log.Printf("消息 %s 投递结果写盘失败：%v", msg.ID, uerr)
		return claimed
	}
	// A commander round is a run like any other: record the delivery in its
	// event stream too, so "who interrupted the commander, and when" is
	// answerable from the run log (journal.go). Without a live journal (the
	// round settled between the check and the append) the message row is
	// already the record and nothing else is needed.
	s.appendCollabEvent(run.ID, final, "in", final.Status)
	return final
}

// formatInboxMessage is the text the receiving model sees, for the commander and
// for an executor alike. IOA carries only a session target, so the addressing
// has to be in the text or the sentence arrives anonymous. A message the control
// plane put back on the queue after a failure carries a [重投] marker: the same
// text may already be in the session transcript (a resumed turn keeps it), so
// the model dedupes by message_id instead of acting twice.
func formatInboxMessage(m model.Message) string {
	// A mechanical notice injected through the inbox path (rare: the prompt
	// aggregates notices separately, but IOA delivery and a redelivered row can
	// still reach here) is not peer speech; say so instead of an anonymous
	// "[消息] from=ledger".
	if m.NoticeFactID != "" {
		version := ""
		if m.NoticeVersion > 0 {
			version = fmt.Sprintf(" v%d", m.NoticeVersion)
		}
		return fmt.Sprintf("[机械通知] fact=%s%s message_id=%s\n%s（按事实 ID 读取当前状态；不是对话，不授权执行）",
			m.NoticeFactID, version, m.ID, m.Text)
	}
	var b strings.Builder
	if m.Redeliveries > 0 {
		b.WriteString("[重投] ")
	}
	b.WriteString("[消息] from=" + m.FromParticipantID)
	// Where the sender sits is a fact of the delivery path, not something the
	// receiving model should have to infer from an id: the console/operator
	// channel and the commander both write through the same project API, and
	// an executor writes with its own participant id. The note is prompt text
	// only — the ledger row, its sender and its body are unchanged.
	b.WriteString(senderOriginNote(m.FromParticipantID))
	if id, ok := collab.IntentIDFromParticipant(m.FromParticipantID); ok {
		b.WriteString(" intent=" + id)
	}
	if m.Kind != "" {
		b.WriteString(" kind=" + m.Kind)
	}
	b.WriteString(" message_id=" + m.ID)
	if m.InReplyTo != "" {
		b.WriteString(" in_reply_to=" + m.InReplyTo)
	}
	if len(m.FactRefs) > 0 {
		b.WriteString(" facts=" + strings.Join(m.FactRefs, ","))
	}
	b.WriteString("\n")
	b.WriteString(m.Text)
	return b.String()
}

// formatQuoteLine renders the request a reply/result quotes. The inbox line
// already carries in_reply_to=<id>, but an id alone does not let a model (or a
// compacted session) know what was asked or answer it with the right context;
// the quoted excerpt is bounded to 160 runes and is read-only. A missing quoted
// row is stated as missing rather than dropped: the association is then visibly
// incomplete instead of silently anonymous.
func (s *Server) formatQuoteLine(m model.Message) string {
	if m.InReplyTo == "" {
		return ""
	}
	quoted, ok := s.Store.MessageByID(m.InReplyTo)
	if !ok {
		return "（引用的消息 " + m.InReplyTo + " 已不存在，无法核对原请求）"
	}
	kind := quoted.Kind
	if kind == "" {
		kind = "未标注"
	}
	return fmt.Sprintf("（引用 %s from=%s kind=%s：「%s」）",
		quoted.ID, quoted.FromParticipantID, kind, clipRunes(quoted.Text, 160))
}

// senderOriginNote answers "who actually wrote this" next to the sender id. The
// operator and the commander share the project API credential, and a message
// sent from the console is recorded as `operator` even when a model was the
// author; without the note a receiving model reads an operator row as its own
// commander speaking. It is an annotation on the injected prompt, not a new
// message field: the ledger keeps one sender id and one body (D86).
func senderOriginNote(from string) string {
	switch from {
	case collab.OperatorID:
		return "（操作员/控制台令牌，不是指挥官）"
	case collab.CommanderID:
		return "（本项目指挥官模型）"
	case model.NoticeSenderID:
		return "（账本机械通知：只反映记录变化与结果引用，不是对话，也不授权执行）"
	}
	if id, ok := collab.IntentIDFromParticipant(from); ok {
		return "（执行者方向 " + id + "）"
	}
	return ""
}

// inboxSection is the queued-inbox prefix for one run, for the commander and for
// an executor alike. deliveryID says which path carried the text (IOA, or this
// run's prompt), so the sender's "queued" state ends at exactly one place and a
// message is never injected twice. runID is the run the text rides, recorded on
// the row so a failure can find and requeue the messages it may not have read.
//
// Mechanical fact-change notices are separated from conversation: they are one
// line per fact id/version, not peer speech, and they must not crowd real
// messages out of the inbox. Normal messages are delivered first (seq order),
// then a bounded notice block; whatever does not fit stays queued for the next
// prompt. maxRunes (>0) is the remaining prompt budget from the caller and
// bounds the whole section.
func (s *Server) inboxSection(participant, deliveryID, runID, closing string, maxRunes int) string {
	pending := s.Store.PendingFor(participant)
	if len(pending) == 0 {
		return ""
	}
	normal := make([]model.Message, 0, len(pending))
	notices := make([]model.Message, 0)
	for _, m := range pending {
		if m.NoticeFactID != "" || m.Kind == model.NoticeKindFact {
			notices = append(notices, m)
			continue
		}
		normal = append(normal, m)
	}
	if maxRunes > 0 && maxRunes < 256 {
		// Not enough room for even a short section: leave everything queued
		// rather than promising text that the prompt cannot carry.
		return ""
	}
	shown := make([]model.Message, 0, inboxLimit)
	usedBytes, usedRunes := 0, 0
	for _, m := range normal {
		if len(shown) >= inboxLimit {
			break
		}
		text := formatInboxMessage(m) + s.formatQuoteLine(m)
		nBytes, nRunes := len(text)+4, len([]rune(text))+4
		if usedBytes+nBytes > inboxByteBudget {
			break
		}
		if maxRunes > 0 && usedRunes+nRunes > maxRunes {
			break
		}
		shown = append(shown, m)
		usedBytes += nBytes
		usedRunes += nRunes
	}
	noticeShown := make([]model.Message, 0, inboxNoticeLimit)
	noticeBytes, noticeRunes := 0, 0
	for _, m := range notices {
		if len(noticeShown) >= inboxNoticeLimit {
			break
		}
		line := formatFactNoticeLine(m)
		nBytes, nRunes := len(line)+1, len([]rune(line))+1
		if noticeBytes+nBytes > inboxNoticeBudget {
			break
		}
		if maxRunes > 0 && usedRunes+noticeRunes+nRunes > maxRunes {
			break
		}
		noticeShown = append(noticeShown, m)
		noticeBytes += nBytes
		noticeRunes += nRunes
	}
	if len(shown) == 0 && len(noticeShown) == 0 {
		return ""
	}
	var b strings.Builder
	if len(shown) > 0 {
		b.WriteString("\n## 未读消息\n")
		if len(normal) > len(shown) {
			fmt.Fprintf(&b, "（按发送顺序列 %d/%d 条，其余留在队列；可 GET /api/messages 按 after 续读）\n", len(shown), len(normal))
		}
		for _, m := range shown {
			if claimed, ok := s.claimInboxRow(m, deliveryID, runID); ok {
				b.WriteString("- " + strings.ReplaceAll(formatInboxMessage(claimed)+s.formatQuoteLine(claimed), "\n", " ") + "\n")
			}
		}
		if strings.TrimSpace(closing) != "" {
			b.WriteString(closing)
		}
	}
	if len(noticeShown) > 0 {
		b.WriteString("\n## 共享发现变化（机械通知）\n")
		b.WriteString("from=" + model.NoticeSenderID + " kind=" + model.NoticeKindFact + "：以下只是事实 ID 与记录版本的变化提示，不是事实正文、不是对话，也不授权执行；按 ID 读取当前状态、版本与证据。\n")
		if len(notices) > len(noticeShown) {
			fmt.Fprintf(&b, "（只列 %d/%d 条，其余留在队列）\n", len(noticeShown), len(notices))
		}
		for _, m := range noticeShown {
			if claimed, ok := s.claimInboxRow(m, deliveryID, runID); ok {
				b.WriteString("- " + strings.ReplaceAll(formatFactNoticeLine(claimed), "\n", " ") + "\n")
			}
		}
	}
	return b.String()
}

// claimInboxRow marks one queued row as carried by this prompt and returns the
// claimed row. A row already claimed elsewhere is skipped: the delivery state is
// the single-winner record.
func (s *Server) claimInboxRow(m model.Message, deliveryID, runID string) (model.Message, bool) {
	claimed, acquired, err := s.Store.BeginMessageDelivery(m.ID)
	if err != nil {
		log.Printf("未读消息 %s 认领失败：%v", m.ID, err)
		return model.Message{}, false
	}
	if !acquired {
		return model.Message{}, false
	}
	if _, err := s.Store.FinishMessageDelivery(m.ID, deliveryID, runID, "prompt", ""); err != nil {
		log.Printf("未读消息 %s 标记投递失败：%v", m.ID, err)
		return model.Message{}, false
	}
	return claimed, true
}

// formatFactNoticeLine is the compact form of a mechanical fact-change notice:
// one line, fact id and record version. It never carries the fact body.
func formatFactNoticeLine(m model.Message) string {
	if m.NoticeFactID == "" {
		return formatInboxMessage(m)
	}
	version := ""
	if m.NoticeVersion > 0 {
		version = fmt.Sprintf(" v%d", m.NoticeVersion)
	}
	coalesced := ""
	if m.Coalesced > 0 {
		coalesced = fmt.Sprintf("（合并 %d 次）", m.Coalesced)
	}
	return fmt.Sprintf("%s%s%s", m.NoticeFactID, version, coalesced)
}

// Only ordinary queued chat is subject to this retention policy. Durable
// instructions/results and delivered conversation are excluded by the store.
const (
	queuedMessageTTL   = 24 * time.Hour
	maxQueuedPerTarget = 50
)

func (s *Server) sweepMessages() {
	removed, err := s.Store.SweepQueued(time.Now(), queuedMessageTTL, maxQueuedPerTarget)
	if err != nil {
		log.Printf("消息队列清理失败：%v", err)
		return
	}
	if removed > 0 {
		log.Printf("消息队列：删除 %d 条送不出去的排队消息（超过 %s，或每个收件人超过 %d 条）",
			removed, queuedMessageTTL, maxQueuedPerTarget)
	}
}

// actorOf resolves who is speaking on the console API. The gate stamps
// X-Tw-Actor from the agent token (cmd/tw auth.wrap), so a harness write is
// attributed to the commander even when the model forgets the header; a request
// on the operator credential stays the operator. The header remains a claim
// about the speaker, not a second security boundary; it exists so a say from the
// commander lands in the same message thread with the same shape as an operator
// say (D86).
func actorOf(r *http.Request) string {
	if r != nil && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Tw-Actor")), collab.CommanderID) {
		return collab.CommanderID
	}
	return collab.OperatorID
}

// senderOf is the sender for a message action: an agent request carries a
// verified identity, a console request carries only the claim above.
func senderOf(r *http.Request) string {
	if id, ok := identityOf(r); ok {
		return id.ParticipantID
	}
	return actorOf(r)
}

// retractMessage withdraws a message that nobody has read yet. That is the whole
// undo: once a message is delivered the recipient may already have acted on it,
// so there is no recall — and no automatic "ignore that" reply either, because a
// notice without new content is just noise in the thread. A sender who needs to
// correct something writes a real message.
func (s *Server) retractMessage(w http.ResponseWriter, r *http.Request) {
	msgID := r.PathValue("id")
	msg, found := s.Store.MessageByID(msgID)
	switch {
	case !found:
		writeErr(w, http.StatusNotFound, "消息不存在")
		return
	case msg.FromParticipantID != senderOf(r):
		writeErr(w, http.StatusForbidden, "只有发件人可以撤回这条消息")
		return
	case msg.Status != model.MessageQueued:
		writeErr(w, http.StatusConflict, "消息已经投递，不能撤回；要更正就再写一条新的")
		return
	}
	deleted, err := s.Store.RetractMessage(msgID, msg.FromParticipantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAction("retract", msgID, deleted.Text)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "message": deleted})
}

// messageFilters applies the console/agent query filters to one project's
// messages. from names the sender (the commander's own view), status narrows to
// one delivery state (queued = the senders that can still be retracted).
func messageFilters(msgs []model.Message, r *http.Request) []model.Message {
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if from == "" && status == "" {
		return msgs
	}
	kept := make([]model.Message, 0, len(msgs))
	for _, m := range msgs {
		if from != "" && m.FromParticipantID != from {
			continue
		}
		if status != "" && m.Status != status {
			continue
		}
		kept = append(kept, m)
	}
	return kept
}
