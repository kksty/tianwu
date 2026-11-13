package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/aop/tool"
	"github.com/kksty/tianwu/internal/artifact"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type loginYieldExec struct {
	s             *Server
	calls         atomic.Int32
	resumedPrompt string
}

func TestLargeImportedCredentialHasBoundedRedactionPatterns(t *testing.T) {
	value := strings.Repeat("large-browser-storage-", 50000)
	pat, ok := newSecretPattern(value)
	if !ok || len(pat.plain) > maxRedactionPrefixLen+1 || pat.plain[0] != value {
		t.Fatal("large credential lost full-value protection or allocated unbounded prefixes")
	}
}

func TestCommanderJournalPreservesNativeResultsWithoutCreatingFacts(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	value := &tool.Artifact{ResultId: "native_result", Tool: "gogo", Data: []byte("original")}
	if err := s.appendRunEvent(run.ID, executor.Event{Kind: "artifact", Native: value, NativeEventID: "event1"}); err != nil {
		t.Fatal(err)
	}
	_, raw, err := artifact.Read(s.CaseRoot, run.ID+"/native_result")
	if err != nil || string(raw) != "original" {
		t.Fatalf("original missing: %v", err)
	}
	if len(s.Store.Snapshot().Facts) != 0 {
		t.Fatal("journal promoted native result")
	}
	lines := readJournalFileLines(t, s.DataDir, run.ID)
	if len(lines) != 1 || lines[0]["artifact_ref"] != run.ID+"/native_result" {
		t.Fatalf("missing artifact reference: %+v", lines)
	}
}

func (e *loginYieldExec) Name() string { return "login-yield" }
func (e *loginYieldExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	if e.calls.Add(1) == 1 {
		in, _ := e.s.Store.GetIntent(req.IntentID)
		emit(executor.Event{Kind: "artifact", NativeEventID: "event_1", Native: &tool.Artifact{ResultId: "native_1", Tool: "gogo", Kind: "service", MediaType: "application/json", Data: []byte(`{"port":443}`)}})
		raw, _ := json.Marshal(map[string]any{"kind": "service", "title": "服务观察", "evidence": []map[string]string{{"artifact": in.RunID + "/native_1"}}})
		if err := os.WriteFile(filepath.Join(req.WorkDir, ".tw", "facts.jsonl"), append(raw, '\n'), 0600); err != nil {
			return executor.Result{}, err
		}
		request := httptest.NewRequest("POST", "/api/intents/"+in.ID+"/operator", strings.NewReader(`{"request":"请完成扫码"}`))
		request.SetPathValue("id", in.ID)
		response := httptest.NewRecorder()
		e.s.requestOperator(response, request)
		if response.Code != 202 {
			return executor.Result{}, fmt.Errorf("operator request %d: %s", response.Code, response.Body.String())
		}
		<-ctx.Done()
		return executor.Result{ExitCode: -1}, ctx.Err()
	} else {
		e.resumedPrompt = req.Prompt
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func TestLoginHandoffReleasesSlotAndResumesWithHumanReceipt(t *testing.T) {
	for _, resolution := range []string{model.OperatorCompleted, model.OperatorUnavailable} {
		t.Run(resolution, func(t *testing.T) { testLoginHandoff(t, resolution) })
	}
}

func testLoginHandoff(t *testing.T, resolution string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &loginYieldExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	ex.s = s
	s.MaxRuns = 1
	s.Timeout = 0
	t.Cleanup(func() { s.CancelAll(); s.WaitIdle(5 * time.Second) })
	in, _ := st.CreateIntent(model.Intent{Title: "登录后核对"})
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { current, _ := st.GetIntent(in.ID); return current.Status == model.StatusWaiting })
	if !s.hasCapacity() {
		t.Fatal("human wait occupies execution capacity")
	}
	s.pump()
	if ex.calls.Load() != 1 {
		t.Fatal("uncompleted human wait dispatched")
	}
	current, _ := st.GetIntent(in.ID)
	requestID := current.Wait.Operator.ID
	mux := http.NewServeMux()
	s.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/intents/"+in.ID+"/operator/complete", strings.NewReader(`{"request_id":"wrong"}`)))
	if response.Code != 409 {
		t.Fatalf("stale receipt code %d", response.Code)
	}
	receipt := "已扫码，测试账号 A"
	if resolution == model.OperatorUnavailable {
		receipt = "没有测试账号，本次仅测试未登录面"
	}
	body, _ := json.Marshal(map[string]string{"request_id": requestID, "response": receipt, "resolution": resolution})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/intents/"+in.ID+"/operator/complete", strings.NewReader(string(body))))
	if response.Code != 202 {
		t.Fatalf("complete %d %s", response.Code, response.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		current, _ := st.GetIntent(in.ID)
		return current.Status == model.StatusDone && ex.calls.Load() == 2
	})
	if !strings.Contains(ex.resumedPrompt, receipt) {
		t.Fatal("resume lost receipt or claimed verified login")
	}
	if resolution == model.OperatorUnavailable {
		if !strings.Contains(ex.resumedPrompt, "未获得所请求的访问能力") || !strings.Contains(ex.resumedPrompt, "未覆盖") || strings.Contains(ex.resumedPrompt, "人工已结束操作") {
			t.Fatal("unavailable receipt was treated as successful login")
		}
	} else if !strings.Contains(ex.resumedPrompt, "尚未验证") {
		t.Fatal("completed receipt claimed verified login")
	}
	if len(st.Evidence()) != 1 {
		t.Fatalf("native citation did not materialize exactly one evidence: %d", len(st.Evidence()))
	}
}

func TestParticipantCannotRequestHumanActionForAnotherWorker(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "ledger.json"))
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	in, _ := st.CreateIntent(model.Intent{Title: "worker"})
	_, _ = st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusRunning; i.RunID = "r_1" })
	mux := http.NewServeMux()
	s.registerAgent(mux)
	req := httptest.NewRequest("POST", "/api/intents/"+in.ID+"/operator", strings.NewReader(`{"request":"扫码"}`))
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ParticipantID: "pt_other", RunID: "r_1"}))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatalf("wrong owner code %d", response.Code)
	}
	req = httptest.NewRequest("POST", "/api/intents/"+in.ID+"/operator/complete", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 404 {
		t.Fatalf("participant can access operator completion: %d", response.Code)
	}
}

func TestAccessMetadataAndBriefDoNotContainRawLoginSecrets(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "ledger.json"))
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	entry, err := access.Create(s.CaseRoot, access.Input{Name: "A", Kind: "request", Origin: "https://example.test", Material: json.RawMessage(`{"method":"GET","url":"https://example.test/me","headers":{"Authorization":"Bearer this-is-a-long-secret-value"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(s.CaseRoot, "in_1")
	section, err := s.accessSection(work)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(section, "this-is-a-long-secret-value") {
		t.Fatal("brief contains credential")
	}
	if !strings.Contains(section, entry.ID) {
		t.Fatal("brief lacks reference")
	}
	if got := s.redactSecrets("Authorization: Bearer this-is-a-long-secret-value"); strings.Contains(got, "this-is-a-long-secret-value") {
		t.Fatal("imported credential is not redacted")
	}
	if raw, err := os.ReadFile(filepath.Join(work, ".tw", "access", entry.ID+".json")); err != nil || !strings.Contains(string(raw), "this-is-a-long-secret-value") {
		t.Fatal("worker material unavailable")
	}
}
