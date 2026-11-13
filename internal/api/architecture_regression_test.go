package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func admissionServer(t *testing.T) (*Server, *blockingExec) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	t.Cleanup(func() { s.SetPaused(true); s.CancelAll(); s.WaitIdle(3 * time.Second) })
	return s, ex
}

func TestCustomPromptCannotBypassAdmission(t *testing.T) {
	for _, gate := range []string{"project", "instance", "profile"} {
		t.Run(gate, func(t *testing.T) {
			s, _ := admissionServer(t)
			switch gate {
			case "project":
				s.MaxRuns = 1
				_, _ = s.Store.AddRun(model.Run{IntentID: "busy"})
			case "instance":
				s.MaxWorkers = 1
				s.GlobalRunning = func() int { return 1 }
			case "profile":
				s.LocalNode = "local"
				s.Profiles = func() ([]ProfileInfo, string, error) {
					return []ProfileInfo{{ID: "fast", Runnable: true, MaxConcurrency: 1}}, "fast", nil
				}
				s.GlobalProfileRunning = func(string) int { return 1 }
			}
			in, err := s.Store.CreateIntent(model.Intent{Title: "fresh"})
			if err != nil {
				t.Fatal(err)
			}
			before := len(s.Store.Runs())
			if _, err := s.startRunWith(in.ID, "custom fresh brief", "", false, true); err == nil {
				t.Fatal("custom prompt bypassed admission")
			}
			if len(s.Store.Runs()) != before {
				t.Fatal("refused dispatch created a run")
			}
		})
	}
}

func TestCrossProjectAdmissionReservesInstanceAndProfileCapacity(t *testing.T) {
	for _, gate := range []string{"instance", "profile"} {
		t.Run(gate, func(t *testing.T) {
			a, _ := admissionServer(t)
			b, _ := admissionServer(t)
			var admission sync.Mutex
			count := func() int { return a.RunningIntentCount() + b.RunningIntentCount() }
			for _, s := range []*Server{a, b} {
				s.InstanceDispatch = &admission
				if gate == "instance" {
					s.MaxWorkers = 1
					s.GlobalRunning = count
				} else {
					s.LocalNode = "local"
					s.Profiles = func() ([]ProfileInfo, string, error) {
						return []ProfileInfo{{ID: "fast", Runnable: true, MaxConcurrency: 1}}, "fast", nil
					}
					s.GlobalProfileRunning = func(string) int { return count() }
				}
			}
			ia, _ := a.Store.CreateIntent(model.Intent{Title: "a"})
			ib, _ := b.Store.CreateIntent(model.Intent{Title: "b"})
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() { <-start; _, err := a.StartRun(ia.ID); done <- err }()
			go func() { <-start; _, err := b.StartRun(ib.ID); done <- err }()
			close(start)
			success := 0
			for range 2 {
				if err := <-done; err == nil {
					success++
				}
			}
			if success != 1 || count() != 1 {
				t.Fatalf("admitted=%d active=%d", success, count())
			}
		})
	}
}

type childFinishesExec struct {
	parent string
	turns  atomic.Int32
}

func (*childFinishesExec) Name() string { return "child-finishes" }
func (e *childFinishesExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if filepath.Base(req.WorkDir) != e.parent {
		return executor.Result{StopReason: "child complete"}, nil
	}
	e.turns.Add(1)
	<-ctx.Done()
	return executor.Result{ExitCode: -1}, ctx.Err()
}

func TestChildWaitYieldsOnlySlotAndResumesParent(t *testing.T) {
	s, _ := admissionServer(t)
	s.MaxRuns = 1
	p, _ := s.Store.CreateIntent(model.Intent{Title: "parent"})
	ex := &childFinishesExec{parent: p.ID}
	s.Exec = ex
	first, err := s.StartRun(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return ex.turns.Load() == 1 })
	child, _, err := s.Store.CreateChildIntent(p.ID, model.Intent{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/intents/"+p.ID+"/wait", map[string]any{"children": []string{child.ID}})
	if rec.Code != 202 {
		t.Fatalf("wait=%d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 3*time.Second, func() bool { return ex.turns.Load() == 2 })
	r, _ := s.Store.GetRun(first)
	c, _ := s.Store.GetIntent(child.ID)
	parent, _ := s.Store.GetIntent(p.ID)
	if r.CancelCause != model.CancelCauseChildWait || c.Status != model.StatusDone || parent.Wait != nil || s.RunningIntentCount() != 1 {
		t.Fatalf("first=%+v child=%+v parent=%+v", r, c, parent)
	}
}

func TestMailboxFilteringPrecedesPagination(t *testing.T) {
	for _, box := range []string{"inbox", "sent"} {
		t.Run(box, func(t *testing.T) {
			f := newAgentFixture(t)
			for i := 0; i < 3; i++ {
				from, to := f.srcID.ParticipantID, f.tgtID.ParticipantID
				if (box == "inbox" && i == 2) || (box == "sent" && i < 2) {
					from, to = to, from
				}
				if _, _, err := f.p.Store.AddMessage(model.Message{FromParticipantID: from, ToParticipantID: to, Text: "hello"}); err != nil {
					t.Fatal(err)
				}
			}
			rec := f.call(t, f.srcID, "GET", "/api/messages?box="+box+"&limit=2", nil)
			var got struct {
				Messages []model.Message `json:"messages"`
				Next     uint64          `json:"next"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || len(got.Messages) != 1 || got.Next != 3 {
				t.Fatalf("page=%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLiveDeliveryPreventsRetractionAndPreservesEarlyAck(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "transport_error"}[fail], func(t *testing.T) {
			f := newAgentFixture(t)
			f.makeLive(t)
			f.srv.Say = func(_ context.Context, _ model.Intent, _ aopexec.SessionInfo, _ string, _ bool) (string, error) {
				m := f.p.Store.Messages(10)[0]
				if m.Status != model.MessageSending {
					t.Errorf("inflight=%s", m.Status)
				}
				if _, err := f.p.Store.RetractMessage(m.ID, m.FromParticipantID); !errors.Is(err, store.ErrMessageNotQueued) {
					t.Errorf("retract=%v", err)
				}
				ack := f.call(t, f.tgtID, "POST", "/api/messages/"+m.ID+"/ack", nil)
				if ack.Code != 200 {
					t.Errorf("ack=%d", ack.Code)
				}
				if fail {
					return "", errors.New("timeout")
				}
				return "delivered", nil
			}
			rec := f.call(t, f.srcID, "POST", "/api/messages", sendBody(f.tgtID.ParticipantID, "once", "hello"))
			if rec.Code != 202 {
				t.Fatalf("send=%d %s", rec.Code, rec.Body.String())
			}
			m := f.p.Store.Messages(10)[0]
			if m.Status != model.MessageConfirmed || m.AckedAt == nil || m.DeliveredRunID == "" {
				t.Fatalf("receipt=%+v", m)
			}
		})
	}
}

func TestReadyOperatorReceiptSurvivesHandoffAndExplicitRun(t *testing.T) {
	for _, mode := range []string{"handoff", "run"} {
		for _, resolution := range []string{model.OperatorCompleted, model.OperatorUnavailable} {
			t.Run(mode+"/"+resolution, func(t *testing.T) {
				f := newParentFixture(t)
				f.srv.Profiles = handoffProfiles()
				ex := &blockingExec{}
				f.srv.Exec = ex
				t.Cleanup(func() { f.srv.SetPaused(true); f.srv.CancelAll(); f.srv.WaitIdle(3 * time.Second) })
				req, err := f.p.Store.RegisterOperatorWait(f.ident.IntentID, f.run.ID, model.OperatorWait{Request: "login"})
				if err != nil {
					t.Fatal(err)
				}
				reason := "operator-constraint-preserved"
				if err := f.p.Store.ResolveOperatorWait(f.ident.IntentID, req.ID, reason, resolution); err != nil {
					t.Fatal(err)
				}
				if _, err := f.p.Store.SettleIntentForRun(f.ident.IntentID, f.run.ID, model.RunDone); err != nil {
					t.Fatal(err)
				}
				_, _ = f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.Status = model.RunDone })
				if mode == "handoff" {
					_, h, _, err := f.srv.HandoffIntent(context.Background(), f.ident.IntentID, "slow", "change model", 0)
					if err != nil {
						t.Fatal(err)
					}
					if h.Wait == nil || h.Wait.Operator.Response != reason {
						t.Fatalf("snapshot=%+v", h)
					}
				} else if _, err := f.srv.StartRun(f.ident.IntentID); err != nil {
					t.Fatal(err)
				}
				waitFor(t, 3*time.Second, func() bool { return len(ex.requests()) == 1 })
				if !strings.Contains(ex.requests()[0].Prompt, reason) {
					t.Fatal("replacement prompt lost receipt")
				}
				in, _ := f.p.Store.GetIntent(f.ident.IntentID)
				if in.Wait != nil {
					t.Fatal("receipt was not consumed")
				}
			})
		}
	}
}

func TestCommanderDeliveryPreservesReceiptAndCannotBeRetracted(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeCommanderLive(t)
	f.srv.Say = func(_ context.Context, _ model.Intent, _ aopexec.SessionInfo, _ string, _ bool) (string, error) {
		m := f.p.Store.Messages(10)[0]
		if _, err := f.p.Store.RetractMessage(m.ID, m.FromParticipantID); !errors.Is(err, store.ErrMessageNotQueued) {
			t.Errorf("retract=%v", err)
		}
		ack := f.call(t, collab.Identity{ParticipantID: collab.CommanderID}, "POST", "/api/messages/"+m.ID+"/ack", nil)
		if ack.Code != 200 {
			t.Errorf("ack=%d", ack.Code)
		}
		return "delivered", nil
	}
	rec := f.call(t, f.srcID, "POST", "/api/messages", sendBody(collab.CommanderID, "once", "hello"))
	if rec.Code != 202 {
		t.Fatalf("send=%d %s", rec.Code, rec.Body.String())
	}
	m := f.p.Store.Messages(10)[0]
	if m.Status != model.MessageConfirmed || m.DeliveredRunID != run.ID {
		t.Fatalf("delivery=%+v", m)
	}
}

func TestAdmittedRunCanBeCanceledImmediately(t *testing.T) {
	s, _ := admissionServer(t)
	in, _ := s.Store.CreateIntent(model.Intent{Title: "cancel immediately"})
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(runID); err != nil {
		t.Fatal(err)
	}
	if !s.WaitIdle(3 * time.Second) {
		t.Fatal("admitted run did not finish")
	}
	r, _ := s.Store.GetRun(runID)
	if r.Status != model.RunCanceled || r.CancelCause != model.CancelCauseOperator {
		t.Fatalf("canceled=%+v", r)
	}
}
