package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

type fakeSay struct {
	mu    sync.Mutex
	calls []fakeSayCall
	err   error
}

type fakeSayCall struct {
	intent  string
	session string
	text    string
}

func (f *fakeSay) fn(_ context.Context, in model.Intent, sess aopexec.SessionInfo, text string, interrupt bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeSayCall{intent: in.ID, session: sess.Session, text: text})
	if f.err != nil {
		return "", f.err
	}
	return "ioa_" + in.ID, nil
}

func (f *fakeSay) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSay) last() fakeSayCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeSayCall{}
	}
	return f.calls[len(f.calls)-1]
}

type agentFixture struct {
	srv    *Server
	p      *project.Project
	say    *fakeSay
	src    model.Intent
	target model.Intent
	srcID  collab.Identity
	tgtID  collab.Identity
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	s, p := newBindingServer(t)
	s.Collab = collab.NewRegistry(time.Hour)
	say := &fakeSay{}
	s.Say = say.fn
	src, err := p.Store.CreateIntent(model.Intent{Title: "来源方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := p.Store.CreateIntent(model.Intent{Title: "目标方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(target.ID, func(i *model.Intent) { i.Attempts = 1 }); err != nil {
		t.Fatal(err)
	}
	target, _ = p.Store.GetIntent(target.ID)
	srcID, err := s.Collab.Issue(collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID(src.ID),
		IntentID: src.ID, RunID: "r_src", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	tgtID, err := s.Collab.Issue(collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID(target.ID),
		IntentID: target.ID, RunID: "r_tgt", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &agentFixture{srv: s, p: p, say: say, src: src, target: target, srcID: srcID, tgtID: tgtID}
}

func (f *agentFixture) makeLive(t *testing.T) model.Run {
	t.Helper()
	run, err := f.p.Store.AddRun(model.Run{IntentID: f.target.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.target.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.p.CaseDir, f.target.ID, ".tw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"url":"http://127.0.0.1:18766","agent":"local","session":"s_target"}`
	if err := os.WriteFile(filepath.Join(dir, "aop-session.json"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	return run
}

func (f *agentFixture) call(t *testing.T, id collab.Identity, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req = req.WithContext(WithIdentity(req.Context(), id))
	rec := httptest.NewRecorder()
	f.srv.AgentHandler().ServeHTTP(rec, req)
	return rec
}

func sendBody(to, key, text string) map[string]any {
	return map[string]any{
		"to": to, "kind": "question", "text": text,
		"idempotency_key": key,
	}
}

func TestAgentSendDeliversToLiveRun(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.ParticipantID(f.target.ID), "k1", "你测的端口是多少？"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Message  model.Message `json:"message"`
		Created  bool          `json:"created"`
		Delivery string        `json:"delivery"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Created || resp.Delivery != model.MessageAccepted || resp.Message.Status != model.MessageAccepted ||
		resp.Message.DeliveryID == "" || resp.Message.Seq == 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if f.say.count() != 1 {
		t.Fatalf("say calls = %d", f.say.count())
	}
	call := f.say.last()
	if call.intent != f.target.ID || call.session != "s_target" {
		t.Fatalf("delivery = %+v", call)
	}
	for _, want := range []string{"message_id=" + resp.Message.ID, "from=" + f.srcID.ParticipantID, "kind=question", "你测的端口是多少？"} {
		if !strings.Contains(call.text, want) {
			t.Fatalf("peer text missing %q:\n%s", want, call.text)
		}
	}
}

func TestAgentSendRetryDoesNotDeliverTwice(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	body := sendBody(collab.ParticipantID(f.target.ID), "same", "hello")
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", body); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created":false`) {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if f.say.count() != 1 {
		t.Fatalf("retry delivered again: %d", f.say.count())
	}
	other := sendBody(collab.ParticipantID(f.target.ID), "same", "different")
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", other); rec.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentSendQueuesWhenTargetIsNotRunning(t *testing.T) {
	f := newAgentFixture(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.ParticipantID(f.target.ID), "k", "晚点看"))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"delivery":"queued"`) {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	if f.say.count() != 0 {
		t.Fatal("queued message was delivered into a session")
	}
	// The target sees it on its next read; nothing silently started a turn.
	rec = f.call(t, f.tgtID, http.MethodGet, "/api/messages?after=0", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "晚点看") {
		t.Fatalf("target read: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentSendRefusesFinishedTargetAndStaleGeneration(t *testing.T) {
	f := newAgentFixture(t)
	if _, err := f.p.Store.UpdateIntent(f.target.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.ParticipantID(f.target.ID), "k", "复活一下"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "已结束") {
		t.Fatalf("terminated: %d %s", rec.Code, rec.Body.String())
	}

	f2 := newAgentFixture(t)
	body := sendBody(collab.ParticipantID(f2.target.ID), "k", "旧代")
	body["to_generation"] = 9
	rec = f2.call(t, f2.srcID, http.MethodPost, "/api/messages", body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "换代") {
		t.Fatalf("stale gen: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentSendValidatesFactsRepliesAndTarget(t *testing.T) {
	f := newAgentFixture(t)
	body := sendBody(collab.ParticipantID(f.target.ID), "k", "x")
	body["fact_refs"] = []string{"f_missing"}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("fact ref: %d %s", rec.Code, rec.Body.String())
	}
	body = sendBody(collab.ParticipantID(f.target.ID), "k2", "x")
	body["in_reply_to"] = "m_missing"
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("reply ref: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.ParticipantID(f.src.ID), "k3", "self")); rec.Code != http.StatusBadRequest {
		t.Fatalf("self: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody("pt_in_missing", "k5", "x")); rec.Code != http.StatusNotFound {
		t.Fatalf("missing target: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentReadAndAck(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	rec := f.call(t, f.srcID, http.MethodPost, "/api/messages", sendBody(collab.ParticipantID(f.target.ID), "k", "看下这个"))
	var sent struct {
		Message model.Message `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	// The target reads what was addressed to it; the sender reads its own.
	rec = f.call(t, f.tgtID, http.MethodGet, "/api/messages?after=0", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), sent.Message.ID) {
		t.Fatalf("target read: %d %s", rec.Code, rec.Body.String())
	}
	// Only the receiver can confirm.
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/messages/"+sent.Message.ID+"/ack", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("sender ack: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.tgtID, http.MethodPost, "/api/messages/"+sent.Message.ID+"/ack", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"confirmed"`) {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.p.Store.MessageByID(sent.Message.ID)
	if got.Status != model.MessageConfirmed || got.AckedAt == nil {
		t.Fatalf("acked = %+v", got)
	}
}

func TestAgentParticipantsAndSelf(t *testing.T) {
	f := newAgentFixture(t)
	f.makeLive(t)
	rec := f.call(t, f.srcID, http.MethodGet, "/api/participants", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("participants: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{f.srcID.ParticipantID, f.tgtID.ParticipantID, collab.CommanderID, `"contactable":true`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("participants missing %q: %s", want, rec.Body.String())
		}
	}
	rec = f.call(t, f.srcID, http.MethodGet, "/api/participants/self", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), f.src.ID) {
		t.Fatalf("self: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), f.srcID.Token) {
		t.Fatal("self view leaked the token")
	}
}

// A peer must know whether it is talking to a strong or a worker direction
// without being told a role name: the roster carries the tier frozen on that
// direction's current run. Both rosters serve it (the agent's and the
// console's), and a direction with no run reports no tier instead of inventing
// one.
func TestParticipantsRosterCarriesCurrentRunTier(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeLive(t)
	if _, err := f.p.Store.UpdateRun(run.ID, func(r *model.Run) {
		r.ResolvedProfile = "cheap"
		r.Tier = "worker"
	}); err != nil {
		t.Fatal(err)
	}
	roster := func(rec *httptest.ResponseRecorder) map[string]participantView {
		t.Helper()
		var resp struct {
			Participants []participantView `json:"participants"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		byID := map[string]participantView{}
		for _, v := range resp.Participants {
			byID[v.ID] = v
		}
		return byID
	}

	agent := f.call(t, f.srcID, http.MethodGet, "/api/participants", nil)
	if agent.Code != http.StatusOK {
		t.Fatalf("agent participants: %d %s", agent.Code, agent.Body.String())
	}
	// The console roster is the same implementation; assert it too so the two
	// views cannot drift.
	console := call(t, f.srv, http.MethodGet, "/api/participants", nil)
	if console.Code != http.StatusOK {
		t.Fatalf("console participants: %d %s", console.Code, console.Body.String())
	}
	for name, byID := range map[string]map[string]participantView{"agent": roster(agent), "console": roster(console)} {
		if got := byID[f.tgtID.ParticipantID].Tier; got != "worker" {
			t.Fatalf("%s roster tier of the live direction = %q, want worker", name, got)
		}
		if got := byID[f.srcID.ParticipantID].Tier; got != "" {
			t.Fatalf("%s roster invented a tier for a direction with no run: %q", name, got)
		}
	}
}

func TestAgentFactsSummaryAndDetail(t *testing.T) {
	f := newAgentFixture(t)
	fact, err := f.p.Store.AddFact(model.Fact{
		IntentID: f.src.ID, Kind: "vuln", Title: "测试结论", Summary: "摘要",
		Doc: "完整正文", Confidence: "observed", Origin: "declared", Status: "unverified",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodGet, "/api/facts?intent="+f.src.ID, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "测试结论") {
		t.Fatalf("facts: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "完整正文") {
		t.Fatal("list leaked the fact body")
	}
	rec = f.call(t, f.srcID, http.MethodGet, "/api/facts/"+fact.ID, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "完整正文") {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentRequiresIdentity(t *testing.T) {
	f := newAgentFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/participants", nil)
	rec := httptest.NewRecorder()
	f.srv.AgentHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no identity: %d %s", rec.Code, rec.Body.String())
	}
}
