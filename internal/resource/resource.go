// Package resource projects execution-site resources from the mechanisms that
// already own them. It is deliberately read-only: listing resources never
// acquires or releases a browser, never answers an operator wait, never resumes
// an action and never writes a lease. Mutations stay on the existing owner
// handlers (`POST /access/{id}/lease`, operator receipts, action results), so
// there is exactly one place that enforces their gates.
//
// Identity always comes from the persisted owner record: an access entry's
// `ac_` id, an operator request's `op_` id, an external action's `ax_` id or a
// run's `r_` id. Process ids, window titles, ports and tool output are never
// used, so a restarted console cannot "recognize" a resource it did not record.
package resource

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/model"
)

// ErrInvalidID reports a resource id that is not `<kind>:<owner-id>` with a
// known kind and a safe owner id. The API maps it to 400, not 404: the caller
// asked with a malformed identity, not for a missing resource.
var ErrInvalidID = errors.New("资源 id 无效")

// ErrUnknownKind reports an unsupported kind filter. The API maps it to 400.
var ErrUnknownKind = errors.New("资源类型无效")

// Ledger is the read side of the single project ledger that the projection
// needs. *store.Store satisfies it; tests may use a fake without a database.
type Ledger interface {
	Runs() []model.Run
	GetRun(id string) (model.Run, bool)
	ListIntents() []model.Intent
	GetIntent(id string) (model.Intent, bool)
	Actions(intentID string) []model.ExternalAction
	GetAction(id string) (model.ExternalAction, bool)
}

// Runtime is the part of the browser view that is not in the persisted lease:
// a native command in flight and an unconfirmed control marker. Both belong to
// the server that is currently operating the site, so the API supplies them;
// a nil Runtime (or nil methods through a typed nil is the caller's mistake)
// means "no command in flight, no marker".
type Runtime interface {
	BrowserCommandActive(accessID string) bool
	BrowserControlPending(accessID string) (bool, error)
}

// Service is the projection over one project's execution-site resources.
type Service struct {
	// Cases is the project's subject case root, where `.access/` lives. Empty
	// means the file-backed resources are not projected (bare test servers);
	// only ledger resources are returned.
	Cases string
	// Ledger is required. Nothing in this package writes to it.
	Ledger Ledger
	// Runtime is optional; see the type comment.
	Runtime Runtime
}

// List returns the project's resources, optionally filtered by intent and kind.
//
// An empty intentID means project-wide (operator/commander). A participant
// handler must never pass an empty intentID: that is a visibility decision the
// HTTP layer makes from identity, not something this read model can infer.
func (s *Service) List(intentID, kind string) ([]model.Resource, error) {
	if kind != "" && !model.IsResourceKind(kind) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	if s.Ledger == nil {
		return nil, fmt.Errorf("资源投影缺少账本读数")
	}
	intents := s.intentsFor(intentID)
	rows := []model.Resource{}

	// Browser and material resources live in the project's `.access/` tree.
	if s.Cases != "" {
		entries, err := access.List(s.Cases)
		if err != nil {
			return nil, fmt.Errorf("读取项目资源目录失败：%w", err)
		}
		for _, entry := range entries {
			row, err := s.resourceForEntry(entry)
			if err != nil {
				return nil, err
			}
			if kind != "" && row.Kind != kind {
				continue
			}
			if intentID != "" && row.IntentID != intentID {
				continue
			}
			rows = append(rows, row)
		}
	}

	// Operator waits are a projection of the intent's persisted Wait.Operator.
	if kind == "" || kind == model.ResourceKindWait {
		for _, in := range intents {
			if row, ok := s.resourceForWait(in); ok {
				rows = append(rows, row)
			}
		}
	}

	// External actions are read-only history; an unknown action stays unknown.
	if kind == "" || kind == model.ResourceKindAction {
		for _, action := range s.Ledger.Actions(intentID) {
			rows = append(rows, s.resourceForAction(action))
		}
	}

	// A session is the model conversation behind the direction's current run.
	// Historical runs stay in the run ledger; projecting every old run here
	// would duplicate that history instead of naming the site to hand over.
	if kind == "" || kind == model.ResourceKindSession {
		for _, in := range intents {
			if row, ok := s.resourceForSession(in); ok {
				rows = append(rows, row)
			}
		}
	}

	sortResources(rows)
	return rows, nil
}

// Get returns one resource by its persisted identity. The second result is
// false when the owner record does not exist (deleted access entry, unknown
// wait/action/run) — that is a 404, not an error: absence is a real state, and
// the API must not turn it into a 500.
func (s *Service) Get(id string) (model.Resource, bool, error) {
	kind, ownerID, err := SplitID(id)
	if err != nil {
		return model.Resource{}, false, err
	}
	if s.Ledger == nil {
		return model.Resource{}, false, fmt.Errorf("资源投影缺少账本读数")
	}
	switch kind {
	case model.ResourceKindBrowser, model.ResourceKindMaterial:
		if s.Cases == "" {
			return model.Resource{}, false, nil
		}
		entry, err := access.Get(s.Cases, ownerID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return model.Resource{}, false, nil
			}
			return model.Resource{}, false, err
		}
		if (kind == model.ResourceKindBrowser) != (entry.Kind == "browser") {
			return model.Resource{}, false, nil
		}
		row, err := s.resourceForEntry(entry)
		return row, err == nil, err
	case model.ResourceKindWait:
		for _, in := range s.Ledger.ListIntents() {
			if in.Wait == nil || in.Wait.Operator == nil || in.Wait.Operator.ID != ownerID {
				continue
			}
			row, ok := s.resourceForWait(in)
			return row, ok, nil
		}
		return model.Resource{}, false, nil
	case model.ResourceKindAction:
		action, ok := s.Ledger.GetAction(ownerID)
		if !ok {
			return model.Resource{}, false, nil
		}
		return s.resourceForAction(action), true, nil
	case model.ResourceKindSession:
		run, ok := s.Ledger.GetRun(ownerID)
		if !ok {
			return model.Resource{}, false, nil
		}
		if run.Purpose != "" {
			// A verifier/report run has a model session, but it is not an
			// execution-site a direction hands over; the resource kind means the
			// direction's working session.
			return model.Resource{}, false, nil
		}
		return s.resourceForRun(run), true, nil
	}
	return model.Resource{}, false, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
}

// ID builds the stable resource id from a kind and the owner record's persisted
// id. Callers should not invent ids from anything else.
func ID(kind, ownerID string) string {
	return kind + ":" + ownerID
}

// SplitID parses `<kind>:<owner-id>`. Owner ids are generated by the ledger
// (`ac_`, `op_`, `ax_`, `r_` prefixes, hex body), so accepting only safe
// characters is enough and stops a URL path from naming a file outside the
// owner's directory.
func SplitID(id string) (kind, ownerID string, err error) {
	kind, ownerID, ok := strings.Cut(id, ":")
	if !ok || !model.IsResourceKind(kind) || ownerID == "" || len(ownerID) > 80 {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	for _, r := range ownerID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return "", "", fmt.Errorf("%w: %q", ErrInvalidID, id)
		}
	}
	return kind, ownerID, nil
}

// intentsFor returns the intents whose waits and sessions are in scope. With an
// intent filter it is one ledger read; project-wide is the existing list.
func (s *Service) intentsFor(intentID string) []model.Intent {
	if intentID != "" {
		if in, ok := s.Ledger.GetIntent(intentID); ok {
			return []model.Intent{in}
		}
		return nil
	}
	return s.Ledger.ListIntents()
}

// sortResources gives a stable, client-friendly order: kind, then persisted id.
func sortResources(rows []model.Resource) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].ID < rows[j].ID
	})
}

// boundedDetail truncates a label to a short, control-character-free value.
// Detail is a display hint, never the owner's record: everything here is
// already visible elsewhere in the console, and material contents are never
// read at all.
func boundedDetail(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	const limit = 160
	if len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}

// number renders the integer fence values (action CAS version etc.) as the
// string token the shared Resource.Version field uses.
func number(n int) string { return strconv.Itoa(n) }

// newDetail allocates a detail map only when there is something to show.
func setDetail(detail map[string]string, key, value string) {
	if value = boundedDetail(value); value != "" {
		detail[key] = value
	}
}

// timestamp is the UpdatedAt fallback rule: an explicit event time when the
// owner records one, otherwise the record's creation time. It never pretends a
// missing time is now.
func timestamp(explicit *time.Time, fallback time.Time) time.Time {
	if explicit != nil {
		return *explicit
	}
	return fallback
}
