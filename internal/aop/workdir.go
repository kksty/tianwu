package aop

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/types/known/anypb"
)

// WorkDirExtensionURL is the Tianwu fork's per-session workspace extension on
// OpenSessionRequest. Upstream AOP has no session working directory, and one
// agent process serves every run, so without this a session resolves relative
// paths against the agent's process directory (tianwu's <data>). The fork reads
// the value (tianwu.WorkDir, JSON with work_dir and single_task) and binds the
// workspace plus the native mailbox scheduling policy (D110, D129).
const WorkDirExtensionURL = "type.googleapis.com/tianwu.WorkDir"

type workDirPayload struct {
	WorkDir    string `json:"work_dir"`
	SingleTask bool   `json:"single_task"`
}

// WorkDirExtension encodes one session's workspace. Empty returns nil, which
// keeps the upstream behavior (agent process directory) for callers without a
// case.
func WorkDirExtension(dir string) *anypb.Any {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	// The control plane owns turn admission. Native messages may enter the
	// mailbox, but only a tracked RunTurn may start model execution.
	raw, err := json.Marshal(workDirPayload{WorkDir: dir, SingleTask: true})
	if err != nil {
		return nil
	}
	return &anypb.Any{TypeUrl: WorkDirExtensionURL, Value: raw}
}
