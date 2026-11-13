package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/kksty/tianwu/internal/aop"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/ws"
)

// terminalControl is a text frame from the browser. Binary frames are the
// keystrokes themselves.
type terminalControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// nodeTerminal bridges the console to a PTY on a node.
//
// Every execution node is an AOP host, so the endpoint comes from the node
// registry and the bytes are the host PTY's. An audit node is the one local
// process with no host to dial, so it has no terminal — the console must not
// pretend otherwise. The control-plane machine is no exception: tw starts an
// `aiscan web` for it and registers it as node `local` (docs/14-nodes.md, D64).
//
// mode=repl attaches the host's singleton `main-repl` instead of opening a new
// shell: the conversation lives in the host, so a page refresh detaches and a
// later open reattaches to the same session. One `main-repl` belongs to the
// host process, not to a project, so projects sharing a host share the
// conversation; that is a documented property, not a bug.
func (s *Server) nodeTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.HasNode(id) {
		writeErr(w, http.StatusNotFound, "没有这个节点："+id)
		return
	}
	tn, ok := s.Exec.(executor.TerminalNode)
	if !ok {
		writeErr(w, http.StatusBadRequest, "这个实例没有 AOP 节点，也就没有宿主终端")
		return
	}
	base, token, agent, ok := tn.TerminalEndpoint(id)
	if !ok {
		writeErr(w, http.StatusBadRequest, "只有 AOP 宿主能开终端：给节点配一个 aop（本机也一样，用 aiscan web 的内置 agent）")
		return
	}
	cols, rows := terminalSize(r)
	mode := "shell"
	if r.URL.Query().Get("mode") == "repl" {
		mode = "repl"
	}

	conn, err := ws.Upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	// Shells need a live host, not a working model provider. Report process
	// failure in the same exit frame as a failed AOP dial, before opening a PTY.
	if id == s.LocalNode && s.LocalHostStatus != nil && !s.LocalHostStatus().Online {
		_ = conn.WriteMessage(terminalExit(s.CheckLocalHost(), true))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// The PTY can end while the browser keeps the socket open (suspended tab,
	// half-open TCP). Cancel alone does not unblock conn.ReadMessageType, and
	// the deferred term.Close would never run: close the socket so the read
	// loop returns and the terminal is released.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	kind, name, repl := "shell", "", false
	if mode == "repl" {
		// The host's interactive agent is a singleton PTY named main-repl.
		// Attaching instead of opening a second one keeps one conversation.
		kind, name, repl = "repl", "main-repl", true
	}
	term, err := aop.OpenTerminalKind(ctx, base, token, agent, cols, rows, kind, name, repl)
	if err != nil {
		_ = conn.WriteMessage(terminalExit(err, true))
		return
	}
	// A refresh or a tab switch closes this WebSocket, not the work: the REPL
	// detaches and the host keeps the conversation, so opening it again
	// reattaches instead of restarting aiscan (and losing what it was doing).
	// A shell has no such contract — it dies with its stream.
	defer func() {
		if repl {
			_ = term.Detach()
			return
		}
		_ = term.Close()
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-term.Done():
				_ = conn.WriteMessage(terminalExit(terminalErr(term), false))
				cancel()
				return
			case data := <-term.Output():
				if err := conn.WriteBinary(data); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		op, payload, err := conn.ReadMessageType()
		if err != nil {
			return
		}
		if op == ws.OpText {
			var ctl terminalControl
			if json.Unmarshal(payload, &ctl) == nil && ctl.Type == "resize" {
				_ = term.Resize(ctl.Cols, ctl.Rows)
			}
			continue
		}
		if err := term.Write(payload); err != nil {
			return
		}
	}
}

func terminalSize(r *http.Request) (int, int) {
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 || cols > 1000 {
		cols = 100
	}
	if rows <= 0 || rows > 1000 {
		rows = 30
	}
	return cols, rows
}

// terminalErr reads the terminal's exit error without blocking: a normal close
// is io.EOF and not worth showing.
func terminalErr(t *aop.Terminal) error {
	select {
	case err := <-t.Errors():
		if err != nil && err != io.EOF {
			return err
		}
	default:
	}
	return nil
}

func terminalExit(err error, fatal bool) []byte {
	body := map[string]any{"type": "exit"}
	if err != nil {
		body["message"] = err.Error()
	}
	if fatal {
		body["fatal"] = true
	}
	out, _ := json.Marshal(body)
	return out
}
