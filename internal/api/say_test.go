package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

func writeSayMarker(t *testing.T, workDir string) {
	t.Helper()
	path := filepath.Join(workDir, ".tw")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "aop-session.json"),
		[]byte(`{"url":"http://host:18766","agent":"local","session":"sess-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSayIntentDeliversIntoLiveRun(t *testing.T) {
	s, _, in := newSteerServer(t)
	writeSayMarker(t, filepath.Join(s.CaseRoot, in.ID))

	var got model.Intent
	var gotSess aopexec.SessionInfo
	var gotText string
	var gotInterrupt bool
	s.Say = func(_ context.Context, intent model.Intent, sess aopexec.SessionInfo, text string, interrupt bool) (string, error) {
		got, gotSess, gotText, gotInterrupt = intent, sess, text, interrupt
		return "msg-1", nil
	}

	id, err := s.SayIntent(context.Background(), in.ID, "收窄到登录口", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !gotInterrupt || gotSess.Session != "sess-1" {
		t.Fatalf("id=%q got=%+v text=%q interrupt=%v", id, got, gotText, gotInterrupt)
	}
	if !strings.Contains(gotText, "收窄到登录口") || !strings.Contains(gotText, "from=operator") {
		t.Fatalf("text = %q", gotText)
	}
	// The say is one message row now (D86): the caller gets the message id, the
	// IOA receipt is the delivery id, and interrupt is a message property. The
	// intent only keeps the counter — interjections stopped being a second copy
	// of the same history.
	msg, ok := s.Store.MessageByID(id)
	if !ok {
		t.Fatalf("say message %q not recorded", id)
	}
	if msg.FromParticipantID != "operator" || msg.ToParticipantID != collab.ParticipantID(in.ID) ||
		msg.Kind != "say" || !msg.Interrupt || msg.Status != model.MessageAccepted || msg.DeliveryID != "msg-1" {
		t.Fatalf("message = %+v", msg)
	}
	after, _ := s.Store.GetIntent(in.ID)
	if after.SayCount != 1 || len(after.Interjections) != 0 {
		t.Fatalf("say_count=%d interjections=%+v", after.SayCount, after.Interjections)
	}
	s.CancelAll()
	if !s.WaitIdle(2 * time.Second) {
		t.Fatal("run did not stop")
	}
}

func TestSayIntentHasNoHardCapAndMissingSession(t *testing.T) {
	s, _, in := newSteerServer(t)
	writeSayMarker(t, filepath.Join(s.CaseRoot, in.ID))
	s.Say = func(context.Context, model.Intent, aopexec.SessionInfo, string, bool) (string, error) {
		return "msg", nil
	}
	for i := 0; i < 6; i++ {
		if _, err := s.SayIntent(context.Background(), in.ID, "补充信息", "", false); err != nil {
			t.Fatalf("say %d: %v", i, err)
		}
	}
	if after, _ := s.Store.GetIntent(in.ID); after.SayCount != 6 {
		t.Fatalf("say_count = %d, want 6", after.SayCount)
	}

	// A run without a trajectory pointer cannot be interrupted; it must not
	// fall back to a fresh dispatch.
	pending, err := s.Store.CreateIntent(model.Intent{Title: "没有会话", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SayIntent(context.Background(), pending.ID, "x", "", false); !errors.Is(err, ErrSayNotRunning) {
		t.Fatalf("pending say = %v, want ErrSayNotRunning", err)
	}
	s.CancelAll()
	if !s.WaitIdle(2 * time.Second) {
		t.Fatal("run did not stop")
	}
}
