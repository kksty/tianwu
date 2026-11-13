package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

func operatorStore(t *testing.T) (*Store, model.Intent) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "登录后测试"})
	if err != nil {
		t.Fatal(err)
	}
	in, err = s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusRunning; i.RunID = "r_login" })
	if err != nil {
		t.Fatal(err)
	}
	return s, in
}

func TestPreparedBrowserAssociationSurvivesRestartAndRejectsStaleRequest(t *testing.T) {
	s, in := operatorStore(t)
	wait, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "扫码"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddOperatorAccess(in.ID, "stale", "ac_A"); err == nil {
		t.Fatal("accepted stale request")
	}
	if err := s.AddOperatorAccess(in.ID, wait.ID, "ac_A"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOperatorAccess(in.ID, wait.ID, "ac_A"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetIntent(in.ID)
	if len(current.Wait.Operator.AccessIDs) != 1 || current.Wait.Operator.AccessIDs[0] != "ac_A" {
		t.Fatal("lost or duplicated browser association")
	}
	if err := s.CompleteOperatorWait(in.ID, wait.ID, "完成"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOperatorAccess(in.ID, wait.ID, "ac_B"); err == nil {
		t.Fatal("changed completed request")
	}
}

func TestOperatorWaitSurvivesRestartAndDoesNotWakeWithoutReceipt(t *testing.T) {
	s, in := operatorStore(t)
	request, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "请扫码", AccessIDs: []string{"ac_demo"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.SettleIntentForRun(in.ID, in.RunID, model.RunDone)
	if err != nil || out.Status != model.StatusWaiting {
		t.Fatalf("settle %+v %v", out, err)
	}
	s, err = Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	state, ok := s.EvaluateWait(in.ID)
	if !ok || state.Ready {
		t.Fatalf("wait incorrectly ready %+v", state)
	}
	if err := s.CompleteOperatorWait(in.ID, "op_stale", "完成"); err == nil {
		t.Fatal("accepted stale receipt")
	}
	if err := s.CompleteOperatorWait(in.ID, request.ID, "已扫码，账号 A"); err != nil {
		t.Fatal(err)
	}
	state, _ = s.EvaluateWait(in.ID)
	if !state.Ready || state.Reason != "operator_completed" {
		t.Fatalf("receipt not ready %+v", state)
	}
	if _, _, err := s.SatisfyWait(in.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetIntent(in.ID)
	if !got.Wait.WakePending || got.Wait.Operator.Response != "已扫码，账号 A" {
		t.Fatalf("lost receipt %+v", got.Wait)
	}
}

func TestOperatorCompletionBeforeTurnEndWakesOnceAtSettlement(t *testing.T) {
	s, in := operatorStore(t)
	request, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "验证码"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetIntent(in.ID)
	if err := s.CompleteOperatorWait(in.ID, request.ID, "完成"); err != nil {
		t.Fatal(err)
	}
	if before.Wait.Operator.CompletedAt != nil {
		t.Fatal("completion mutated a detached snapshot")
	}
	if got, _ := s.GetIntent(in.ID); got.Status != model.StatusRunning {
		t.Fatal("completion changed live turn state")
	}
	out, err := s.SettleIntentForRun(in.ID, in.RunID, model.RunDone)
	if err != nil || !out.Wake || out.Status != model.StatusPending {
		t.Fatalf("settle %+v %v", out, err)
	}
}

func TestOperatorWaitCannotReplaceChildWaitAndCannotBeReplaced(t *testing.T) {
	s, in := operatorStore(t)
	if _, err := s.RegisterOperatorWait(in.ID, "r_old", model.OperatorWait{Request: "扫码"}); err == nil {
		t.Fatal("old run registered wait")
	}
	if _, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "扫码"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterOperatorWait(in.ID, in.RunID, model.OperatorWait{Request: "换一个请求"}); err == nil {
		t.Fatal("overwrote pending request")
	}
	if _, err := s.RegisterWait(in.ID, in.RunID, model.Wait{Children: []string{"child"}}); err == nil {
		t.Fatal("child wait replaced operator")
	}
	got, _ := s.GetIntent(in.ID)
	got.Wait.Operator.Request = "not persisted"
	got.Wait.Deadline = func() *time.Time { v := time.Now().Add(-time.Hour); return &v }()
	if got, _ := s.GetIntent(in.ID); got.Wait.Operator.Request != "扫码" {
		t.Fatal("caller mutated stored wait")
	}
}
