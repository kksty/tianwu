package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

type chatResponse struct {
	To        string `json:"to"`
	Commander *struct {
		MessageID string `json:"message_id"`
		Status    string `json:"status"`
	} `json:"commander"`
	Delivered []chatItem `json:"delivered"`
	Skipped   []chatItem `json:"skipped"`
}

// A chat addressed to the commander is one ledger row on the real inbox path:
// the next commander round reads it, a paused project keeps it queued, and the
// 通信 page shows it as part of the same thread.
func TestChatToCommanderStoresOneQueuedMessageRow(t *testing.T) {
	s, _ := newBindingServer(t)
	rec := call(t, s, http.MethodPost, "/api/chat", map[string]any{"to": "commander", "text": "优先测 /irs/front/search"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat = %d: %s", rec.Code, rec.Body.String())
	}
	var out chatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.To != "commander" || out.Commander == nil || out.Commander.MessageID == "" {
		t.Fatalf("response = %+v", out)
	}
	if len(out.Delivered) != 0 || len(out.Skipped) != 0 {
		t.Fatalf("commander-only chat named executors: %+v", out)
	}
	msg, ok := s.Store.MessageByID(out.Commander.MessageID)
	if !ok || msg.FromParticipantID != collab.OperatorID ||
		msg.ToParticipantID != collab.CommanderID || msg.Kind != "say" ||
		msg.Text != "优先测 /irs/front/search" || msg.Status != model.MessageQueued {
		t.Fatalf("commander message = %+v ok=%v", msg, ok)
	}
}

// "指挥官 + 全体执行者" means the commander row plus every direction that can
// still receive a sentence: a live run gets it now, a pending direction carries
// it in its next prompt, and a finished direction is named as skipped instead of
// being revived by mail.
func TestChatAllReachesLiveAndPendingAndSkipsFinishedDirections(t *testing.T) {
	s, _, running := newSteerServer(t)
	writeSayMarker(t, filepath.Join(s.CaseRoot, running.ID))
	s.Say = func(_ context.Context, _ model.Intent, _ aopexec.SessionInfo, _ string, _ bool) (string, error) {
		return "ioa-chat", nil
	}
	pending, err := s.Store.CreateIntent(model.Intent{Title: "待跑方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Store.CreateIntent(model.Intent{Title: "已收口方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodPost, "/api/chat", map[string]any{"to": "all", "text": "这个站点也可以测，先别碰内网"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chat = %d: %s", rec.Code, rec.Body.String())
	}
	var out chatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Commander == nil || out.Commander.MessageID == "" {
		t.Fatalf("commander half missing: %+v", out)
	}
	byIntent := map[string]chatItem{}
	for _, item := range out.Delivered {
		byIntent[item.IntentID] = item
	}
	if len(byIntent) != 2 {
		t.Fatalf("delivered = %+v, want running+pending", out.Delivered)
	}
	if got := byIntent[running.ID]; got.MessageID == "" || got.Status != model.MessageAccepted {
		t.Fatalf("running direction = %+v", got)
	}
	if got := byIntent[pending.ID]; got.MessageID == "" || got.Status != model.MessageQueued {
		t.Fatalf("pending direction = %+v", got)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].IntentID != done.ID {
		t.Fatalf("skipped = %+v, want the done direction", out.Skipped)
	}
	// Every delivered half is a real ledger row from the operator, so the thread
	// and the receipt cannot disagree.
	for _, item := range out.Delivered {
		msg, ok := s.Store.MessageByID(item.MessageID)
		if !ok || msg.FromParticipantID != collab.OperatorID || msg.Text != "这个站点也可以测，先别碰内网" {
			t.Fatalf("delivered message %s = %+v ok=%v", item.MessageID, msg, ok)
		}
	}
}

func TestChatValidatesInputAndIsIdempotentOnRetry(t *testing.T) {
	s, _ := newBindingServer(t)
	for _, body := range []map[string]any{
		{"to": "commander", "text": "  "},
		{"to": "everyone", "text": "x"},
		{"to": "commander", "text": strings.Repeat("长", maxChatText+1)},
	} {
		if rec := call(t, s, http.MethodPost, "/api/chat", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %+v = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	before := len(s.Store.Messages(0))
	first := call(t, s, http.MethodPost, "/api/chat", map[string]any{
		"to": "commander", "text": "只发一次", "idempotency_key": "chat-retry",
	})
	if first.Code != http.StatusAccepted {
		t.Fatalf("first = %d %s", first.Code, first.Body.String())
	}
	var once chatResponse
	if err := json.Unmarshal(first.Body.Bytes(), &once); err != nil {
		t.Fatal(err)
	}
	second := call(t, s, http.MethodPost, "/api/chat", map[string]any{
		"to": "commander", "text": "只发一次", "idempotency_key": "chat-retry",
	})
	if second.Code != http.StatusAccepted {
		t.Fatalf("retry = %d %s", second.Code, second.Body.String())
	}
	var twice chatResponse
	if err := json.Unmarshal(second.Body.Bytes(), &twice); err != nil {
		t.Fatal(err)
	}
	if twice.Commander == nil || once.Commander == nil || twice.Commander.MessageID != once.Commander.MessageID {
		t.Fatalf("retry created a second row: first=%+v second=%+v", once.Commander, twice.Commander)
	}
	if got := len(s.Store.Messages(0)); got != before+1 {
		t.Fatalf("messages = %d, want %d (one retryable row)", got, before+1)
	}
}
