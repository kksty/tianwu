package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func providerTestServer(t *testing.T, profile string) (*Server, model.Intent, model.Run) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, filepath.Join(dir, "cases"))
	s.LocalNode = "local"

	in, err := st.CreateIntent(model.Intent{Title: "测试方向"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = ""
	}); err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{
		IntentID: in.ID, Node: "local", Profile: profile,
		ResolvedProfile: profile, ProfileSource: "explicit",
	})
	if err != nil {
		t.Fatal(err)
	}
	// AddRun always starts a run as running; the watchdog hook only sees settled
	// runs (finish already wrote the status).
	if _, err := st.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunFailed }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	return s, in, run
}

// addFailedRun creates another settled failed run for the same intent.
func addFailedRun(t *testing.T, s *Server, intentID, profile string) model.Run {
	t.Helper()
	run, err := s.Store.AddRun(model.Run{
		IntentID: intentID, Node: "local", Profile: profile,
		ResolvedProfile: profile, ProfileSource: "explicit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunFailed }); err != nil {
		t.Fatal(err)
	}
	return run
}

func authFailure() executor.Result {
	return executor.Result{
		IsError:    true,
		StopReason: "error",
		Output:     "API error (401): invalid_api_key provided",
	}
}

func TestClassifyAuthFailure(t *testing.T) {
	cases := []string{
		"API error (401): bad key",
		"API error (403): forbidden",
		"invalid_api_key",
		"Unauthorized: check your API key",
		"authentication_error: token expired",
		"invalid token",
	}
	for _, text := range cases {
		class := classifyProviderFailure(executor.Result{IsError: true, StopReason: "error", Output: text})
		if class != providerFailureAuth {
			t.Fatalf("%q classified as %s, want auth", text, class)
		}
		if !class.rotatable() {
			t.Fatalf("auth must be rotatable")
		}
	}
}

func TestClassifyQuotaFailure(t *testing.T) {
	cases := []string{
		"API error (402): payment required",
		"insufficient_quota: you exceeded your current quota",
		"insufficient balance",
		"quota exceeded for this key",
		"billing: please add credits",
		"账户余额不足",
		"账号欠费",
		// WorkBuddy reports exhausted credit as HTTP 429 with a quota body; the
		// quota rule must win over the generic "api error (429)" rate rule.
		`API error (429): {"error":{"message":"WorkBuddy: 额度已用尽，请购买加量包","type":"insufficient_quota","code":14018}}`,
		`API error (429): {"error":{"message":"额度已用尽，请充值"}}`,
	}
	for _, text := range cases {
		class := classifyProviderFailure(executor.Result{IsError: true, StopReason: "error", Output: text})
		if class != providerFailureQuota {
			t.Fatalf("%q classified as %s, want quota", text, class)
		}
	}
}

// A rate limit means the key is fine (sapper's watchdog distinguishes it the
// same way): rotating would burn a valid credential for nothing.
func TestRateLimitDoesNotRotate(t *testing.T) {
	for _, text := range []string{
		"API error (429): rate limit reached",
		"rate_limit exceeded",
		"too many requests",
		"overloaded_error: upstream is overloaded",
	} {
		class := classifyProviderFailure(executor.Result{IsError: true, StopReason: "error", Output: text})
		if class != providerFailureRate {
			t.Fatalf("%q classified as %s, want rate", text, class)
		}
		if class.rotatable() {
			t.Fatal("rate limit must not rotate")
		}
	}

	s, in, run := providerTestServer(t, "fast")
	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { t.Fatal("rate limit restarted a host"); return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { t.Fatal("rate limit retried"); return "", nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, executor.Result{
		IsError: true, StopReason: "error", Output: "API error (429): rate limit",
	})
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0", rotations)
	}
}

func TestTransientFailureDoesNotRotate(t *testing.T) {
	class := classifyProviderFailure(executor.Result{
		IsError: true, StopReason: "error", Output: "connection reset by peer",
	})
	if class != providerFailureTransient {
		t.Fatalf("class = %s, want transient", class)
	}
	if class.rotatable() {
		t.Fatal("transient failure must not rotate")
	}

	s, in, run := providerTestServer(t, "fast")
	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, executor.Result{
		IsError: true, StopReason: "error", Output: "connection reset by peer",
	})
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0", rotations)
	}
}

// A run that succeeded is never classified, even if its stderr mentions an API
// error: the run verdict already said "done" and the watchdog does not overrule
// it (agent.md §1: no task judgement from text).
func TestSuccessfulRunIsNotClassified(t *testing.T) {
	res := executor.Result{ExitCode: 0, Output: "API error (401): stale warning from an earlier attempt"}
	if class := classifyProviderFailure(res); class != providerFailureNone {
		t.Fatalf("successful run classified as %s", class)
	}
	s, in, run := providerTestServer(t, "fast")
	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunDone, res)
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0", rotations)
	}
}

func TestRotationRetriesAtMostOnce(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	var rotations, restarts, retries int
	s.RotateProfileKey = func(profile string) (bool, error) {
		if profile != "fast" {
			t.Fatalf("rotated unexpected profile %q", profile)
		}
		rotations++
		return true, nil
	}
	s.RestartProfileHost = func(string) error { restarts++; return nil }
	s.retryProviderIntentFn = func(intentID, note, profileOverride, fallbackFrom string) (string, error) {
		if intentID != in.ID {
			t.Fatalf("retried unexpected intent %q", intentID)
		}
		if note != providerRetryNote {
			t.Fatalf("retry note = %q", note)
		}
		if profileOverride != "" || fallbackFrom != "" {
			t.Fatalf("rotation retry carried a fallback binding (%q/%q)", profileOverride, fallbackFrom)
		}
		retries++
		return "r_new", nil
	}

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	// The watchdog hook can race with a duplicate finish in a real process; a
	// second call for the same run must not rotate or retry again.
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())

	if rotations != 1 {
		t.Fatalf("rotations = %d, want 1", rotations)
	}
	if restarts != 1 {
		t.Fatalf("restarts = %d, want 1", restarts)
	}
	if retries != 1 {
		t.Fatalf("retries = %d, want 1", retries)
	}
}

// Two different runs failing on the same profile inside the cooldown window:
// only the first rotates, so a dead key cannot burn the whole pool.
func TestRotationCooldownSuppressesSecondAttempt(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	second := addFailedRun(t, s, in.ID, "fast")
	var rotations, restarts, retries int
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { restarts++; return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { retries++; return "r_new", nil }

	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	s.afterProviderFailure(second.ID, in.ID, model.RunFailed, authFailure())

	if rotations != 1 || restarts != 1 {
		t.Fatalf("rotations=%d restarts=%d, want 1/1", rotations, restarts)
	}
	if retries != 1 {
		t.Fatalf("retries = %d, want 1", retries)
	}
}

// "No spare key" is not an error and it must not consume the cooldown: once an
// operator adds a key, the next failure may rotate.
func TestNoSpareKeyDoesNotBlockLaterRotation(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	second := addFailedRun(t, s, in.ID, "fast")
	var rotations, restarts, retries int
	s.RotateProfileKey = func(string) (bool, error) {
		rotations++
		return rotations > 1, nil // first call: single key, no rotation
	}
	s.RestartProfileHost = func(string) error { restarts++; return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { retries++; return "r_new", nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, authFailure())
	s.afterProviderFailure(second.ID, in.ID, model.RunFailed, authFailure())
	if rotations != 2 {
		t.Fatalf("rotations = %d, want the cooldown released after no_spare", rotations)
	}
	// Only the run whose rotation succeeded is restarted and retried.
	if restarts != 1 || retries != 1 {
		t.Fatalf("restarts=%d retries=%d, want 1/1", restarts, retries)
	}
}

// The provider error text usually reaches the classifier only through the run
// journal's error event (Result.Output carries stderr, which the AOP transport
// leaves empty). This pins that path: a journal-only 401 must rotate.
func TestRotationReadsJournalErrorEvents(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	logDir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := []executor.Event{
		{Kind: executor.KindMessage, Role: "assistant", Text: "我正在扫描目标"},
		{Kind: executor.KindError, Text: "API error (401) invalid api key"},
	}
	path := filepath.Join(logDir, run.ID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.RestartProfileHost = func(string) error { return nil }
	s.retryProviderIntentFn = func(string, string, string, string) (string, error) { return "r_new", nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, executor.Result{
		IsError: true, StopReason: "error", // no Output: the text is only in the journal
	})
	if rotations != 1 {
		t.Fatalf("rotations = %d, want 1 (journal error event was ignored)", rotations)
	}
}

// Model messages and tool output in the journal are not provider errors; a run
// whose only "API error" text is the model talking must not rotate a key.
func TestRotationIgnoresModelMessages(t *testing.T) {
	s, in, run := providerTestServer(t, "fast")
	logDir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logDir, run.ID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, ev := range []executor.Event{
		{Kind: executor.KindMessage, Role: "assistant", Text: "假想 API error (401) 不是真的"},
		{Kind: executor.KindToolResult, Text: "API error (402) 出现在目标响应里"},
	} {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.afterProviderFailure(run.ID, in.ID, model.RunFailed, executor.Result{
		IsError: true, StopReason: "error",
	})
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0: model/tool text must not drive credential actions", rotations)
	}
}

// Remote-node runs have their own credentials; this instance must not rotate a
// local profile because a run elsewhere failed.
func TestRotationIgnoresRemoteNodeRuns(t *testing.T) {
	s, in, _ := providerTestServer(t, "fast")
	remote, err := s.Store.AddRun(model.Run{
		IntentID: in.ID, Node: "node-2", ResolvedProfile: "fast", Status: model.RunFailed,
	})
	if err != nil {
		t.Fatal(err)
	}
	rotations := 0
	s.RotateProfileKey = func(string) (bool, error) { rotations++; return true, nil }
	s.afterProviderFailure(remote.ID, in.ID, model.RunFailed, authFailure())
	if rotations != 0 {
		t.Fatalf("rotations = %d, want 0 for a remote node run", rotations)
	}
}
