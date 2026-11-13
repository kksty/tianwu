package store

// Behaviour tests for the durable fact-change outbox: who is notified, how
// repeated changes coalesce, what happens when a queue is full, and how a
// restart resumes from the persisted checkpoint. They use a temporary SQLite
// ledger and never start a server, a model or a browser.

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func openRoutingStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustIntent(t *testing.T, s *Store, in model.Intent) model.Intent {
	t.Helper()
	out, err := s.CreateIntent(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func addRoutingFact(t *testing.T, s *Store, f model.Fact) model.Fact {
	t.Helper()
	if f.Kind == "" {
		f.Kind = "host"
	}
	if f.Title == "" {
		f.Title = "记录"
	}
	out, err := s.AddFact(f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fillRecipientQueue plants queued rows directly, in one transaction. Going
// through AddMessage would be correct but would pay one durable commit per row,
// which is not what the capacity test is about.
func fillRecipientQueue(t *testing.T, s *Store, to string, n int) []string {
	t.Helper()
	s.mu.Lock()
	now := time.Now()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := NewID("m")
		ids = append(ids, id)
		s.db.Messages = append(s.db.Messages, model.Message{
			ID: id, Seq: s.nextMessageSeqLocked(), EventID: id,
			FromParticipantID: "pt_flood", ToParticipantID: to,
			Text: "占位消息", Status: model.MessageQueued, CreatedAt: now, UpdatedAt: now,
		})
	}
	err := s.flushNoRev("message")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// queuedNoticesFor returns the queued fact notices addressed to one participant.
func queuedNoticesFor(s *Store, recipient string) []model.Message {
	out := []model.Message{}
	for _, m := range s.PendingFactNotices() {
		if m.ToParticipantID == recipient {
			out = append(out, m)
		}
	}
	return out
}

func queueStatusFor(t *testing.T, s *Store, recipient string) QueueStats {
	t.Helper()
	for _, q := range s.QueueStatus(recipient) {
		if q.Recipient == recipient {
			return q
		}
	}
	t.Fatalf("没有 %s 的队列状态：%+v", recipient, s.QueueStatus(""))
	return QueueStats{}
}

func TestFactChangeNotifiesRelatedDirectionOnce(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, s, model.Intent{
		Title:              "依赖方向",
		Description:        "d",
		InterestEntityRefs: []string{"host:10.0.0.5"},
	})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	f := addRoutingFact(t, s, model.Fact{
		IntentID:  producer.ID,
		Kind:      "host",
		Title:     "10.0.0.5 在线",
		Summary:   "记录正文不应出现在通知里",
		EntityRef: "host:10.0.0.5",
	})
	// Repeated passes must not create a second row: the cursor is the dedupe.
	for i := 0; i < 3; i++ {
		if err := s.RouteFactChanges(64); err != nil {
			t.Fatal(err)
		}
	}
	got := queuedNoticesFor(s, collab.ParticipantID(waiter.ID))
	if len(got) != 1 {
		t.Fatalf("相关方向应收到一条通知，得到 %d 条：%+v", len(got), got)
	}
	notice := got[0]
	if notice.FromParticipantID != model.NoticeSenderID {
		t.Fatalf("通知发送者应为机械身份 %q，得到 %q", model.NoticeSenderID, notice.FromParticipantID)
	}
	if notice.Kind != model.NoticeKindFact || !notice.Durable || notice.Status != model.MessageQueued {
		t.Fatalf("通知行语义不对：%+v", notice)
	}
	if notice.NoticeFactID != f.ID || notice.NoticeVersion == 0 || !slices.Contains(notice.FactRefs, f.ID) {
		t.Fatalf("通知没有指向事实与记录版本：%+v", notice)
	}
	if !strings.Contains(notice.Text, f.ID) {
		t.Fatalf("通知正文必须带事实 ID：%q", notice.Text)
	}
	// No broadcast of bodies: the title/summary stay behind the record read.
	if strings.Contains(notice.Text, f.Title) || strings.Contains(notice.Text, f.Summary) {
		t.Fatalf("通知正文泄露事实正文：%q", notice.Text)
	}
	if owner := queuedNoticesFor(s, collab.ParticipantID(producer.ID)); len(owner) != 0 {
		t.Fatalf("产出方向不应收到自己写入的回声：%+v", owner)
	}
}

func TestUnrelatedDirectionIsNotNotified(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	interested := mustIntent(t, s, model.Intent{Title: "关心 A", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	stranger := mustIntent(t, s, model.Intent{Title: "无关方向", Description: "d"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.9"})
	for i := 0; i < 2; i++ {
		if err := s.RouteFactChanges(64); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []model.Intent{interested, stranger} {
		if got := queuedNoticesFor(s, collab.ParticipantID(in.ID)); len(got) != 0 {
			t.Fatalf("%s 不应收到无关事实通知：%+v", in.Title, got)
		}
	}
}

func TestCoalescingKeepsOneQueuedNoticeAndCountsChanges(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, s, model.Intent{Title: "依赖方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	f := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	first := queuedNoticesFor(s, collab.ParticipantID(waiter.ID))
	if len(first) != 1 {
		t.Fatalf("initial notice = %+v", first)
	}
	if _, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Summary = "补充了证据" }); err != nil {
		t.Fatal(err)
	}
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	merged := queuedNoticesFor(s, collab.ParticipantID(waiter.ID))
	if len(merged) != 1 {
		t.Fatalf("合并后应只有一条排队通知，得到 %d 条：%+v", len(merged), merged)
	}
	row := merged[0]
	if row.ID != first[0].ID {
		t.Fatalf("合并应更新原行 %s，却新建了 %s", first[0].ID, row.ID)
	}
	if row.Coalesced < 1 {
		t.Fatalf("合并计数没有增加：%+v", row)
	}
	if row.NoticeVersion <= first[0].NoticeVersion {
		t.Fatalf("通知版本没有推进：before=%d after=%d", first[0].NoticeVersion, row.NoticeVersion)
	}
}

func TestCoalescingDoesNotRewriteDeliveredNotices(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, s, model.Intent{Title: "依赖方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	f := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	queued := queuedNoticesFor(s, collab.ParticipantID(waiter.ID))
	if len(queued) != 1 {
		t.Fatalf("initial notice = %+v", queued)
	}
	if _, err := s.UpdateMessage(queued[0].ID, func(m *model.Message) { m.Status = model.MessageAccepted }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Summary = "又变了" }); err != nil {
		t.Fatal(err)
	}
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	rows := []model.Message{}
	for _, m := range s.Messages(0) {
		if m.ToParticipantID == collab.ParticipantID(waiter.ID) && m.NoticeFactID == f.ID {
			rows = append(rows, m)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("已投递行不应被合并改写，应新增一条排队通知，得到 %d 行：%+v", len(rows), rows)
	}
	var accepted, pending model.Message
	for _, m := range rows {
		switch m.Status {
		case model.MessageAccepted:
			accepted = m
		case model.MessageQueued:
			pending = m
		}
	}
	if accepted.ID != queued[0].ID || accepted.Coalesced != 0 {
		t.Fatalf("已投递通知被改写：%+v", accepted)
	}
	if pending.Coalesced != 0 || pending.NoticeVersion <= accepted.NoticeVersion {
		t.Fatalf("新通知行不对：%+v", pending)
	}
}

func TestFullQueueDefersCheckpointAndDeliversWhenRoomAppears(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, s, model.Intent{Title: "依赖方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	to := collab.ParticipantID(waiter.ID)
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	before := queueStatusFor(t, s, to)
	ids := fillRecipientQueue(t, s, to, MaxQueuedMessages)
	if got := queueStatusFor(t, s, to); got.Pending != MaxQueuedMessages {
		t.Fatalf("占位队列 = %+v", got)
	}
	f := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	deferred := queueStatusFor(t, s, to)
	if !deferred.Deferred {
		t.Fatalf("满队列应标记 Deferred：%+v", deferred)
	}
	if deferred.Cursor != before.Cursor {
		t.Fatalf("满队列不应推进检查点：before=%d after=%d", before.Cursor, deferred.Cursor)
	}
	if got := queuedNoticesFor(s, to); len(got) != 0 {
		t.Fatalf("满队列不应丢下通知：%+v", got)
	}
	// Make room by delivering some of the placeholder traffic, then retry.
	for _, id := range ids[:5] {
		if _, err := s.UpdateMessage(id, func(m *model.Message) { m.Status = model.MessageAccepted }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	got := queuedNoticesFor(s, to)
	if len(got) != 1 || got[0].NoticeFactID != f.ID {
		t.Fatalf("腾出空间后应补发那条变化：%+v", got)
	}
	final := queueStatusFor(t, s, to)
	if final.Deferred {
		t.Fatalf("补发后不应再是 Deferred：%+v", final)
	}
	if final.Cursor <= before.Cursor {
		t.Fatalf("补发后检查点应推进：before=%d after=%d", before.Cursor, final.Cursor)
	}
}

func TestRoutingResumesFromPersistedCheckpointAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	producer := mustIntent(t, first, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, first, model.Intent{Title: "依赖方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	to := collab.ParticipantID(waiter.ID)
	if err := first.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	f1 := addRoutingFact(t, first, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if err := first.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	cursorBefore := queueStatusFor(t, first, to).Cursor
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	// The queued notice and the cursor are ledger state, not process memory.
	if got := queuedNoticesFor(second, to); len(got) != 1 || got[0].NoticeFactID != f1.ID {
		t.Fatalf("重启后通知未保留：%+v", got)
	}
	if got := queueStatusFor(t, second, to).Cursor; got != cursorBefore {
		t.Fatalf("重启后检查点 = %d，期望 %d", got, cursorBefore)
	}
	f2 := addRoutingFact(t, second, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if err := second.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	notices := queuedNoticesFor(second, to)
	if len(notices) != 2 {
		t.Fatalf("重启后应从检查点续读，得到 %d 条：%+v", len(notices), notices)
	}
	seen := map[string]bool{}
	for _, m := range notices {
		if seen[m.NoticeFactID] {
			t.Fatalf("同一事实重复通知：%+v", m)
		}
		seen[m.NoticeFactID] = true
	}
	if !seen[f1.ID] || !seen[f2.ID] {
		t.Fatalf("续读没有覆盖新旧事实：%+v", seen)
	}
}

func TestAncestorFactChangeNotifiesLiveDescendant(t *testing.T) {
	s := openRoutingStore(t)
	parent := mustIntent(t, s, model.Intent{Title: "父方向", Description: "d"})
	child := mustIntent(t, s, model.Intent{Title: "子方向", Description: "d", ParentID: parent.ID})
	stranger := mustIntent(t, s, model.Intent{Title: "无关方向", Description: "d"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	f := addRoutingFact(t, s, model.Fact{IntentID: parent.ID, EntityRef: "host:10.0.0.5"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	got := queuedNoticesFor(s, collab.ParticipantID(child.ID))
	if len(got) != 1 || got[0].NoticeFactID != f.ID {
		t.Fatalf("后代方向应收到祖先的事实变化：%+v", got)
	}
	if got := queuedNoticesFor(s, collab.ParticipantID(stranger.ID)); len(got) != 0 {
		t.Fatalf("无关方向不应收到通知：%+v", got)
	}
}

func TestFactRecordedBeforeDirectionCreationIsNotReplayed(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	old := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	waiter := mustIntent(t, s, model.Intent{Title: "后建方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	if got := queuedNoticesFor(s, collab.ParticipantID(waiter.ID)); len(got) != 0 {
		t.Fatalf("后建方向不应回放创建前的历史：%+v", got)
	}
	// A change recorded after creation is still routed: the checkpoint is a
	// subscription start, not a mute.
	if _, err := s.UpdateFact(old.ID, func(x *model.Fact) { x.Summary = "创建后的新变化" }); err != nil {
		t.Fatal(err)
	}
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	got := queuedNoticesFor(s, collab.ParticipantID(waiter.ID))
	if len(got) != 1 || got[0].NoticeFactID != old.ID {
		t.Fatalf("创建后的变化应被路由：%+v", got)
	}
}

func TestSettledDirectionIsNotNotified(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	parked := mustIntent(t, s, model.Intent{Title: "已停放方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	if _, err := s.UpdateIntent(parked.ID, func(in *model.Intent) { in.Status = model.StatusParked }); err != nil {
		t.Fatal(err)
	}
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	if got := queuedNoticesFor(s, collab.ParticipantID(parked.ID)); len(got) != 0 {
		t.Fatalf("已结束方向不应收到通知：%+v", got)
	}
}

// The automatic write path must be enough: AddFact alone routes its own change
// without a caller, the notice is durable, and no run is created by routing.
func TestAddFactAutomaticallyNotifiesRelatedDirection(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, s, model.Intent{Title: "依赖方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	// No RouteFactChanges call here: the store's own fact commit path must file
	// the notice and the checkpoint.
	f := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	to := collab.ParticipantID(waiter.ID)
	got := queuedNoticesFor(s, to)
	if len(got) != 1 || got[0].NoticeFactID != f.ID {
		t.Fatalf("事实提交后应自动入队一条关联通知：%+v", got)
	}
	if runs := s.Runs(); len(runs) != 0 {
		t.Fatalf("路由与入队不应创建运行：%+v", runs)
	}
	// The checkpoint moved with the notice: a later explicit pass is a no-op.
	before := queueStatusFor(t, s, to)
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	if got := len(queuedNoticesFor(s, to)); got != 1 {
		t.Fatalf("显式补跑不应重复入队，得到 %d 条", got)
	}
	if after := queueStatusFor(t, s, to); after.Cursor != before.Cursor {
		t.Fatalf("无新变化时检查点不应移动：before=%d after=%d", before.Cursor, after.Cursor)
	}
}

// A real backlog (queue at the bound) leaves two relevant changes unqueued. The
// per-recipient batch size then decides how many the next pass files, one at a
// time, without ever advancing past a change it could not enqueue.
func TestBackloggedBatchResumesOneChangePerPass(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	waiter := mustIntent(t, s, model.Intent{Title: "依赖方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	to := collab.ParticipantID(waiter.ID)
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	fillRecipientQueue(t, s, to, MaxQueuedMessages)
	// Both facts commit while the queue is full: routing runs from the fact
	// commit path, defers, and leaves the checkpoint unadvanced.
	f1 := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	f2 := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if got := queueStatusFor(t, s, to); !got.Deferred {
		t.Fatalf("满队列应留下 Deferred 检查点：%+v", got)
	}
	if got := queuedNoticesFor(s, to); len(got) != 0 {
		t.Fatalf("满队列时不应入队：%+v", got)
	}
	// Free enough room for both notices, then let the batch limit pace them.
	freeRoomForRouting(t, s, to, 3)
	if err := s.RouteFactChanges(1); err != nil {
		t.Fatal(err)
	}
	got := queuedNoticesFor(s, to)
	if len(got) != 1 || got[0].NoticeFactID != f1.ID {
		t.Fatalf("每批只应处理一条变化：%+v", got)
	}
	if err := s.RouteFactChanges(1); err != nil {
		t.Fatal(err)
	}
	got = queuedNoticesFor(s, to)
	if len(got) != 2 {
		t.Fatalf("下一批应继续处理，得到 %d 条：%+v", len(got), got)
	}
	if got[0].NoticeFactID != f1.ID || got[1].NoticeFactID != f2.ID {
		t.Fatalf("续读顺序不对：%+v", got)
	}
	if final := queueStatusFor(t, s, to); final.Deferred {
		t.Fatalf("补发后不应再是 Deferred：%+v", final)
	}
}

// freeRoomForRouting delivers placeholder rows so the router has admission room.
// It only touches the synthetic rows planted by fillRecipientQueue.
func freeRoomForRouting(t *testing.T, s *Store, to string, n int) {
	t.Helper()
	freed := 0
	for _, m := range s.PendingFor(to) {
		if freed >= n {
			return
		}
		if m.FromParticipantID != "pt_flood" {
			continue
		}
		if _, err := s.UpdateMessage(m.ID, func(x *model.Message) { x.Status = model.MessageAccepted }); err != nil {
			t.Fatal(err)
		}
		freed++
	}
	if freed < n {
		t.Fatalf("只腾出 %d 个位置，需要 %d", freed, n)
	}
}

// A settled direction has no delivery opportunity. Its queued mechanical notices
// are reconciled on the next routing pass (the pointer would be a queue leak),
// while a still-live direction's notices and any ordinary message are untouched.
func TestSettledDirectionDropsUndeliverableNotices(t *testing.T) {
	s := openRoutingStore(t)
	producer := mustIntent(t, s, model.Intent{Title: "产出方向", Description: "d"})
	settling := mustIntent(t, s, model.Intent{Title: "将结束方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	survivor := mustIntent(t, s, model.Intent{Title: "仍在运行方向", Description: "d", InterestEntityRefs: []string{"host:10.0.0.5"}})
	settlingTo := collab.ParticipantID(settling.ID)
	survivorTo := collab.ParticipantID(survivor.ID)
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	f1 := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	for _, to := range []string{settlingTo, survivorTo} {
		if got := queuedNoticesFor(s, to); len(got) != 1 || got[0].NoticeFactID != f1.ID {
			t.Fatalf("%s 的第一条通知 = %+v", to, got)
		}
	}
	// An already-delivered notice is not the router's to delete: it is history.
	first := queuedNoticesFor(s, settlingTo)[0]
	if _, err := s.UpdateMessage(first.ID, func(m *model.Message) { m.Status = model.MessageAccepted }); err != nil {
		t.Fatal(err)
	}
	f2 := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	if got := queuedNoticesFor(s, settlingTo); len(got) != 1 || got[0].NoticeFactID != f2.ID {
		t.Fatalf("结束前的第二条通知 = %+v", got)
	}
	if _, err := s.UpdateIntent(settling.ID, func(in *model.Intent) { in.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	f3 := addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})

	// The settled direction keeps only the delivered notice: the queued pointer is
	// gone and the direction no longer occupies a queue row.
	if got := queuedNoticesFor(s, settlingTo); len(got) != 0 {
		t.Fatalf("已结束方向的排队通知应被清理：%+v", got)
	}
	rows := noticeRowsForStore(s, settlingTo)
	if len(rows) != 1 || rows[0].ID != first.ID || rows[0].Status != model.MessageAccepted {
		t.Fatalf("已投递通知不应被删除或改写：%+v", rows)
	}
	if got := s.QueueStatus(settlingTo); len(got) != 0 {
		t.Fatalf("已结束方向不应再出现在队列视图：%+v", got)
	}
	// The live direction is untouched: one notice per fact, still queued.
	survivorNotices := queuedNoticesFor(s, survivorTo)
	if len(survivorNotices) != 3 {
		t.Fatalf("仍在运行方向应保留 3 条通知，得到 %d 条：%+v", len(survivorNotices), survivorNotices)
	}
	seen := map[string]bool{}
	for _, m := range survivorNotices {
		seen[m.NoticeFactID] = true
	}
	for _, f := range []model.Fact{f1, f2, f3} {
		if !seen[f.ID] {
			t.Fatalf("仍在运行方向缺少事实 %s 的通知：%+v", f.ID, survivorNotices)
		}
	}

	// Ordinary participant messages are separate state, even for a settled
	// direction; a later routing pass must not sweep them with the notices.
	if _, _, err := s.AddMessage(testMessage("pt_peer", settlingTo, "k_ordinary", "普通消息")); err != nil {
		t.Fatal(err)
	}
	addRoutingFact(t, s, model.Fact{IntentID: producer.ID, EntityRef: "host:10.0.0.5"})
	queued := s.PendingFor(settlingTo)
	if len(queued) != 1 || queued[0].ID == first.ID || queued[0].NoticeFactID != "" || queued[0].Text != "普通消息" {
		t.Fatalf("普通消息不应被通知清理删除：%+v", queued)
	}
	if got := s.QueueStatus(settlingTo); len(got) != 1 || got[0].Pending != 1 {
		t.Fatalf("只剩普通消息时队列应计数 1：%+v", got)
	}
}

// noticeRowsForStore returns every notice row (any status) addressed to one
// participant, for assertions about what the purge must not touch.
func noticeRowsForStore(s *Store, recipient string) []model.Message {
	out := []model.Message{}
	for _, m := range s.Messages(0) {
		if m.ToParticipantID == recipient && m.NoticeFactID != "" {
			out = append(out, m)
		}
	}
	return out
}

func TestQueueStatusReportsBacklogAndLimits(t *testing.T) {
	s := openRoutingStore(t)
	waiter := mustIntent(t, s, model.Intent{Title: "依赖方向", Description: "d"})
	to := collab.ParticipantID(waiter.ID)
	if err := s.RouteFactChanges(64); err != nil {
		t.Fatal(err)
	}
	fillRecipientQueue(t, s, to, 3)
	rows := s.QueueStatus(to)
	if len(rows) != 1 {
		t.Fatalf("QueueStatus(%q) = %+v", to, rows)
	}
	q := rows[0]
	if q.Pending != 3 || q.Bytes == 0 || q.Oldest == nil || q.Deferred {
		t.Fatalf("队列状态 = %+v", q)
	}
	if q.Cursor == 0 {
		t.Fatalf("检查点没有随着方向创建建立：%+v", q)
	}
	// The operator view is all recipients; a stranger filter stays empty.
	if !slices.ContainsFunc(s.QueueStatus(""), func(other QueueStats) bool { return other.Recipient == to }) {
		t.Fatalf("操作员视图缺少 %s", to)
	}
	if got := s.QueueStatus("pt_missing"); len(got) != 0 {
		t.Fatalf("未知收件人不应返回队列：%+v", got)
	}
}

func TestAddMessageRefusesWhenRecipientQueueIsFull(t *testing.T) {
	s := openRoutingStore(t)
	to := "pt_target"
	fillRecipientQueue(t, s, to, MaxQueuedMessages)
	_, created, err := s.AddMessage(testMessage("pt_a", to, "k1", "hello"))
	if !errors.Is(err, ErrMessageBackpressure) {
		t.Fatalf("满队列应返回 ErrMessageBackpressure，得到 %v", err)
	}
	if created {
		t.Fatal("被拒绝的消息不能标记为已创建")
	}
}

func TestAddMessageRefusesWhenRecipientByteBudgetIsFull(t *testing.T) {
	s := openRoutingStore(t)
	to := "pt_target"
	s.mu.Lock()
	id := NewID("m")
	s.db.Messages = append(s.db.Messages, model.Message{
		ID: id, Seq: s.nextMessageSeqLocked(), EventID: id,
		FromParticipantID: "pt_flood", ToParticipantID: to,
		Text: strings.Repeat("x", MaxQueuedBytes), Status: model.MessageQueued,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	err := s.flushNoRev("message")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.AddMessage(testMessage("pt_a", to, "k1", "hello"))
	if !errors.Is(err, ErrMessageBackpressure) {
		t.Fatalf("字节预算已满应返回 ErrMessageBackpressure，得到 %v", err)
	}
}

func TestAddMessageRetryOfStoredMessageIsNotBackpressure(t *testing.T) {
	s := openRoutingStore(t)
	first, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "same", "hello"))
	if err != nil || !created {
		t.Fatalf("first = %+v created=%v err=%v", first, created, err)
	}
	fillRecipientQueue(t, s, "pt_b", MaxQueuedMessages)
	again, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "same", "hello"))
	if err != nil {
		t.Fatalf("幂等重试不应被队列容量拒绝：%v", err)
	}
	if created || again.ID != first.ID {
		t.Fatalf("幂等重试应返回原行：%+v created=%v", again, created)
	}
}
