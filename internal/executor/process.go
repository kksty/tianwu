package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// runProcess starts one harness process, streams its events and reports the
// exit. Audit uses it: it speaks `--output-format stream-json` on stdout and
// needs the same cancel/stderr handling an agent run used to.
//
// Provider deltas are folded here instead of emitted. A long turn produces
// thousands of them, they carry nothing a reader needs one by one, and the
// harness usually ends the turn with a complete assistant message. When it only
// streamed deltas, the folded text is emitted as that message.
func runProcess(ctx context.Context, bin string, args []string, dir string, env []string, emit func(Event)) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	setProcAttr(cmd)
	// Cancel must take the whole tree down: an agent usually leaves tools
	// (browser, scanners) behind.
	cmd.Cancel = func() error { killGroup(cmd); return nil }
	cmd.WaitDelay = 5 * time.Second

	// Never block the child on a full stderr pipe.
	var stderr strings.Builder
	var stderrMu sync.Mutex
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{ExitCode: -1}, err
	}

	if err := cmd.Start(); err != nil {
		return Result{ExitCode: -1}, err
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := errPipe.Read(buf)
			if n > 0 {
				stderrMu.Lock()
				if stderr.Len() < 64<<10 {
					stderr.Write(buf[:n])
				}
				stderrMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	res := Result{}
	var deltas strings.Builder
	sawMessage := false
	readFailed := false
	// Cancellation kills the process tree, but a tool it spawned (browser,
	// scanner) can keep the stdout write end open. The read loop would then
	// block forever before Wait is even called, so WaitDelay could not help.
	// Closing our read end on cancellation unblocks it; WaitDelay bounds the
	// rest. Events emitted so far are kept.
	stopWatcher := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = outPipe.Close()
		case <-stopWatcher:
		}
	}()
	defer close(stopWatcher)

	reader := bufio.NewReaderSize(outPipe, 1<<20)
	for {
		line, readErr := reader.ReadString('\n')
		if len(strings.TrimSpace(line)) > 0 {
			ev := parseEvent(line)
			switch {
			case ev.Kind == KindMessageDelta:
				if deltas.Len() < 8<<10 {
					deltas.WriteString(ev.Text)
				}
			default:
				if ev.Kind == KindMessage && ev.Role == "assistant" && ev.Text != "" {
					sawMessage = true
				}
				res.Events++
				absorb(&res, ev)
				emit(ev)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				// A broken stream is not a successful run: remember it, because
				// the exit-code assignment below used to overwrite this -1 with
				// the exit status and report a truncated run as success.
				readFailed = true
			}
			break
		}
	}
	if !sawMessage && deltas.Len() > 0 {
		ev := Event{Kind: KindMessage, Role: "assistant", Text: truncate(deltas.String(), 400), At: time.Now()}
		res.Events++
		absorb(&res, ev)
		emit(ev)
	}

	waitErr := cmd.Wait()
	stderrMu.Lock()
	stderrText := strings.TrimSpace(stderr.String())
	stderrMu.Unlock()
	res = finishResult(res, waitErr, stderrText, readFailed)
	return res, nil
}

// finishResult turns the exit status, our own read result and the child's
// stderr into the one attribution the caller shows.
//
// Three failures used to be conflated here: a stderr string overwrote the
// "stream was cut" message unconditionally, and when stderr was empty the
// exit-status fallback called Error() on a nil waitErr. The second happens when
// a cancellation closes our read end of stdout at the moment the child exits
// cleanly — a narrow window, but it panics the whole control plane. Keeping the
// attribution here also makes the precedence explicit and testable without
// spawning a process: our own read failure first, then the child's stderr, then
// the exit status.
func finishResult(res Result, waitErr error, stderr string, readFailed bool) Result {
	res.ExitCode = 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	if readFailed {
		// A broken stream is not a successful run: a truncated event stream
		// must never read as exit 0, even when the child itself exited cleanly.
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
		detail := stderr
		if detail == "" && waitErr != nil {
			detail = waitErr.Error()
		}
		if detail == "" {
			res.Output = "读取 harness 事件流失败"
		} else {
			res.Output = "读取 harness 事件流失败：" + detail
		}
		return res
	}
	if stderr != "" {
		res.Output = stderr
		return res
	}
	if res.ExitCode != 0 && res.Output == "" && waitErr != nil {
		res.Output = waitErr.Error()
	}
	return res
}

// absorb keeps the run-level facts the ledger records: what the assistant said,
// why the turn stopped, how much it cost. The exit code alone cannot carry
// them — a harness can exit 0 after a failed turn.
func absorb(res *Result, ev Event) {
	if ev.ContextWindow > 0 {
		res.ContextWindow = ev.ContextWindow
	}
	if ev.ContextTokens > 0 {
		res.FinalContextTokens = ev.ContextTokens
		if ev.ContextTokens > res.ContextTokens {
			res.ContextTokens = ev.ContextTokens
		}
	}
	switch ev.Kind {
	case KindTurnEnd:
		res.StopReason = ev.StopReason
		res.IsError = !ev.OK
		if ev.Tokens > 0 {
			res.Tokens = ev.Tokens
		}
	case KindUsage:
		if res.Tokens == 0 && ev.Tokens > 0 {
			res.Tokens = ev.Tokens
		}
	case KindError:
		res.IsError = true
		if res.StopReason == "" {
			res.StopReason = "error"
		}
	case KindStatus:
		if ev.State != "" {
			res.LastState = ev.State
			if IsCompactionState(ev.State) {
				res.Compacted = true
				// The pre-compaction size is no longer the session's size; a
				// later usage event, if any, reports the compacted one.
				res.FinalContextTokens = 0
			}
		}
	case KindMessage:
		if ev.Role == "assistant" && ev.Text != "" {
			res.Text = ev.Text
		}
	}
}

// parseEvent turns one stream-json line into an Event.
//
// The line is protojson for cyber-harness's aop.Event with UseProtoNames, so
// the payload key is exactly the oneof field name: message / tool_call /
// tool_result / turn_started / turn_ended / usage / error / status /
// session_started. Unknown payloads stay `raw` instead of being guessed at.
// ParseLine decodes one stream-json line from a harness's stdout. The AOP
// executor maps the same payload names onto the same Event, so the ledger has
// one wire format to read.
func ParseLine(line string) Event { return parseEvent(line) }

func parseEvent(line string) Event {
	trimmed := strings.TrimSpace(line)
	ev := Event{Kind: KindRaw, At: time.Now(), Text: truncate(trimmed, 400)}
	if !json.Valid([]byte(trimmed)) {
		return ev
	}
	ev.Raw = json.RawMessage(trimmed)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return ev
	}
	_ = json.Unmarshal(obj["session_id"], &ev.SessionID)
	if ev.SessionID == "" {
		_ = json.Unmarshal(obj["sessionId"], &ev.SessionID)
	}
	payload := func(key string) bool { _, ok := obj[key]; return ok }

	switch {
	case payload("message"):
		var m struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(obj["message"], &m)
		ev.Kind, ev.Role = KindMessage, m.Role
		ev.Text = truncate(contentText(m.Content), 400)

	case payload("message_delta"):
		var md struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(obj["message_delta"], &md)
		ev.Kind, ev.Role = KindMessageDelta, "assistant"
		ev.Text = truncate(md.Text, 400)

	case payload("tool_call"):
		var tc struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(obj["tool_call"], &tc)
		ev.Kind = KindToolCall
		ev.Tool = tc.Name
		ev.CallID = tc.ID
		ev.Text = truncate(strings.TrimSpace(tc.Name+" "+tc.Kind), 400)

	case payload("tool_result"):
		var tr struct {
			CallID     string            `json:"call_id"`
			Name       string            `json:"name"`
			IsError    bool              `json:"is_error"`
			DurationMs flexInt           `json:"duration_ms"`
			Output     []json.RawMessage `json:"output"`
		}
		_ = json.Unmarshal(obj["tool_result"], &tr)
		ev.Kind, ev.Tool, ev.OK = KindToolResult, tr.Name, !tr.IsError
		// The call id and the harness-measured duration let the console pair
		// this result with its tool_call and show elapsed time. duration_ms is
		// an int64 and therefore arrives as a string in protojson, which is why
		// it is decoded through flexInt.
		ev.CallID = tr.CallID
		ev.DurationMs = int(tr.DurationMs)
		ev.Text = truncate(contentText(tr.Output), 400)

	case payload("turn_ended"):
		var te struct {
			StopReason    string         `json:"stop_reason"`
			Error         map[string]any `json:"error"`
			ContextTokens flexInt        `json:"context_tokens"`
			Usage         struct {
				TotalTokens flexInt            `json:"total_tokens"`
				Detail      map[string]flexInt `json:"detail"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(obj["turn_ended"], &te)
		ev.Kind = KindTurnEnd
		ev.StopReason = te.StopReason
		ev.OK = len(te.Error) == 0
		ev.Tokens = int(te.Usage.TotalTokens)
		ev.ContextTokens = int(te.ContextTokens)
		if ev.ContextTokens == 0 {
			ev.ContextTokens = int(te.Usage.Detail["context_tokens"])
		}
		ev.ContextWindow = int(te.Usage.Detail["context_window"])
		ev.Text = truncate(te.StopReason, 400)

	case payload("turn_started"):
		ev.Kind = KindTurnStart

	case payload("usage"):
		var u struct {
			TotalTokens flexInt            `json:"total_tokens"`
			Detail      map[string]flexInt `json:"detail"`
		}
		_ = json.Unmarshal(obj["usage"], &u)
		ev.Kind, ev.Tokens = KindUsage, int(u.TotalTokens)
		ev.ContextTokens = int(u.Detail["context_tokens"])
		ev.ContextWindow = int(u.Detail["context_window"])

	case payload("error"):
		var perr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(obj["error"], &perr)
		ev.Kind = KindError
		ev.Text = truncate(strings.TrimSpace(perr.Code+" "+perr.Message), 400)

	case payload("status"):
		var st struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(obj["status"], &st); err != nil {
			var s string
			if json.Unmarshal(obj["status"], &s) == nil {
				st.State = s
			}
		}
		ev.Kind, ev.State = KindStatus, strings.TrimSpace(st.State)
		ev.Text = truncate(ev.State, 400)

	case payload("session_started"):
		ev.Kind = KindSession

	case payload("session_ended"):
		var se struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(obj["session_ended"], &se)
		ev.Kind = KindSessionEnd
		ev.Text = truncate(se.Reason, 400)
	}
	return ev
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// flexInt reads a protojson int64, which comes across the wire as a *string*
// ("total_tokens":"5715") because 64-bit integers do not fit a JSON number in
// JavaScript. Decoding it as an int silently yields 0, which is how every run
// recorded zero tokens for months.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// A float or an unexpected shape is not worth failing the event for.
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}

// contentText joins the text of protojson content items.
//
// The items are `aop.Content`, a oneof, so the text part is a *message* and
// protojson renders it one level deep: {"text":{"text":"…"}}. The flat form
// {"text":"…"} is accepted too — it is what the docs and hand-written fixtures
// used, and tolerating it costs one branch. Items without text (tool calls,
// media, reasoning) contribute nothing.
func contentText(items []json.RawMessage) string {
	var b strings.Builder
	for _, item := range items {
		var holder struct {
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(item, &holder) != nil {
			continue
		}
		raw := strings.TrimSpace(string(holder.Text))
		switch {
		case raw == "" || raw == "null":
			continue
		case strings.HasPrefix(raw, `"`):
			var s string
			if json.Unmarshal(holder.Text, &s) == nil {
				b.WriteString(s)
			}
		default:
			var nested struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(holder.Text, &nested) == nil {
				b.WriteString(nested.Text)
			}
		}
	}
	return b.String()
}

// writePromptFile stores the prompt in the workspace and returns the path to
// hand to the harness (relative to the work dir).
func writePromptFile(workDir, name, prompt string) (string, error) {
	if workDir == "" {
		return "", errors.New("no work dir")
	}
	abs := filepath.Join(workDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(prompt), 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// ResolveBin finds the harness binary next to tw first, then on PATH. It keeps
// Windows and Linux deployments identical: no hardcoded ".exe".
func ResolveBin(name string) string {
	candidates := []string{name}
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		candidates = []string{name + ".exe", name}
	}
	// Next to the binary, then the repo's own ./bin: `go run ./cmd/tw` puts the
	// temp binary somewhere else, so the second check is what makes
	// "build once, then just go run" work from the repo root.
	dirs := []string{"bin"}
	if self, err := os.Executable(); err == nil {
		dirs = append([]string{filepath.Dir(self)}, dirs...)
	}
	for _, dir := range dirs {
		for _, c := range candidates {
			p := filepath.Join(dir, c)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// NormalizePath keeps p when it names an existing file, and on Windows retries
// with the .exe suffix so `--aiscan ./bin/aiscan-full` works without the
// extension. A path that is still missing is returned unchanged: the startup
// warning should print what the operator actually typed.
func NormalizePath(p string) string {
	if p == "" {
		return ""
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(p), ".exe") {
		if st, err := os.Stat(p + ".exe"); err == nil && !st.IsDir() {
			return p + ".exe"
		}
	}
	return p
}
