package resource

import (
	"strconv"
	"time"

	"github.com/kksty/tianwu/internal/access"
	"github.com/kksty/tianwu/internal/model"
)

// resourceForEntry projects one `.access/` entry. A browser entry is an owned
// execution site; a state/request entry is read-only material with no lease.
func (s *Service) resourceForEntry(entry access.Entry) (model.Resource, error) {
	if entry.Kind == "browser" {
		return s.resourceForBrowser(entry)
	}
	return s.resourceForMaterial(entry), nil
}

// resourceForBrowser derives held/busy/recovery exactly like the console's
// lease view, from persisted facts rather than from the lease file's own
// "state" label (which is always "held" on disk). Reading never rewrites the
// lease, so an ended run stays reserved for explicit operator recovery.
func (s *Service) resourceForBrowser(entry access.Entry) (model.Resource, error) {
	detail := map[string]string{}
	setDetail(detail, "name", entry.Name)
	setDetail(detail, "profile", entry.Profile)
	setDetail(detail, "site", entry.Origin)
	row := model.Resource{
		ID:        ID(model.ResourceKindBrowser, entry.ID),
		Kind:      model.ResourceKindBrowser,
		Ref:       entry.Origin,
		IntentID:  entry.IntentID,
		State:     model.ResourceStateFree,
		UpdatedAt: entry.CreatedAt,
	}
	lease := entry.Lease
	if lease == nil {
		row.Detail = emptyToNil(detail)
		row.Query = queryFor(row.ID)
		return row, nil
	}
	// The lease is the fence: a caller holding an older lease id must be
	// rejected by the owner's release/recover path even if the intent's
	// generation happens to repeat.
	row.Version = lease.ID
	row.HolderRunID = lease.RunID
	row.Generation = lease.Generation
	row.UpdatedAt = lease.AcquiredAt
	if lease.IntentID != "" {
		row.IntentID = lease.IntentID
	}
	setDetail(detail, "holder", lease.Owner)
	row.State = model.ResourceStateHeld

	// A native command in flight is busy even when the holder run is still
	// live: the site is actively executing, not merely reserved.
	if s.Runtime != nil && s.Runtime.BrowserCommandActive(entry.ID) {
		row.State = model.ResourceStateBusy
		row.Detail = emptyToNil(detail)
		row.Query = queryFor(row.ID)
		return row, nil
	}
	recovery := false
	if lease.RunID != "" {
		run, ok := s.Ledger.GetRun(lease.RunID)
		if !ok || run.Status != model.RunRunning {
			// The holder ended (or restarted away): nothing auto-releases it.
			recovery = true
		} else if lease.IntentID != "" && run.IntentID != "" && run.IntentID != lease.IntentID {
			// The lease's direction and its holder run's direction disagree:
			// ownership cannot be trusted from these records, so it is not
			// reported as held for either side.
			recovery = true
			setDetail(detail, "issue", "占用记录与持有 run 不一致，需操作员核对")
		} else if lease.IntentID != "" {
			if in, ok := s.Ledger.GetIntent(lease.IntentID); ok && in.RunID != "" && in.RunID != lease.RunID {
				// The direction has moved on to another run; the old lease is
				// no longer its current execution site.
				recovery = true
				setDetail(detail, "issue", "占用记录不是该方向当前 run，需操作员核对")
			}
		}
	}
	if !recovery && s.Runtime != nil {
		pending, err := s.Runtime.BrowserControlPending(entry.ID)
		if err != nil || pending {
			// An unconfirmed command means the site's actual state is unknown;
			// only an operator who confirmed idle may recover it.
			recovery = true
		}
	}
	if recovery {
		row.State = model.ResourceStateRecovery
	}
	row.Detail = emptyToNil(detail)
	row.Query = queryFor(row.ID)
	return row, nil
}

// resourceForMaterial projects state/request login material. It has no owner
// record of its own, so it is always "free": the registry says nothing about
// whether its cookies are still valid, because the program cannot prove that.
func (s *Service) resourceForMaterial(entry access.Entry) model.Resource {
	detail := map[string]string{}
	setDetail(detail, "name", entry.Name)
	setDetail(detail, "material_kind", entry.Kind)
	setDetail(detail, "profile", entry.Profile)
	setDetail(detail, "site", entry.Origin)
	row := model.Resource{
		ID:        ID(model.ResourceKindMaterial, entry.ID),
		Kind:      model.ResourceKindMaterial,
		Ref:       entry.Origin,
		IntentID:  entry.IntentID,
		State:     model.ResourceStateFree,
		UpdatedAt: entry.CreatedAt,
		Detail:    emptyToNil(detail),
	}
	row.Query = queryFor(row.ID)
	return row
}

// resourceForWait projects the intent's persisted operator wait. The wait's
// identity is the `op_` request id; its fence token is the receipt, so a stale
// console cannot "complete" a newer request by naming an older one.
func (s *Service) resourceForWait(in model.Intent) (model.Resource, bool) {
	if in.Wait == nil || in.Wait.Operator == nil {
		return model.Resource{}, false
	}
	operator := in.Wait.Operator
	row := model.Resource{
		ID:          ID(model.ResourceKindWait, operator.ID),
		Kind:        model.ResourceKindWait,
		IntentID:    in.ID,
		HolderRunID: in.Wait.RegisteredByRun,
		State:       model.ResourceStatePending,
		UpdatedAt:   timestamp(nil, in.Wait.RegisteredAt),
	}
	if run, ok := s.Ledger.GetRun(in.Wait.RegisteredByRun); ok {
		row.Generation = run.Generation
	}
	detail := map[string]string{}
	setDetail(detail, "access_count", strconv.Itoa(len(operator.AccessIDs)))
	if operator.CompletedAt != nil {
		resolution := operator.Resolution
		if resolution == "" {
			// Older receipts predate the resolution field; they were recorded as
			// completed actions, so an empty value reads as completed.
			resolution = model.OperatorCompleted
		}
		if resolution == model.OperatorUnavailable {
			row.State = model.ResourceStateUnavailable
		} else {
			row.State = model.ResourceStateCompleted
		}
		row.Version = operator.CompletedAt.UTC().Format(time.RFC3339Nano) + "/" + resolution
		row.UpdatedAt = *operator.CompletedAt
		setDetail(detail, "resolution", resolution)
	}
	row.Detail = emptyToNil(detail)
	row.Query = queryFor(row.ID)
	return row, true
}

// resourceForAction projects one ExternalAction. Reads never resolve, replay or
// duplicate it: an unknown action stays unknown until a receipt or an explicit
// reconciliation names the CAS version it saw.
func (s *Service) resourceForAction(action model.ExternalAction) model.Resource {
	row := model.Resource{
		ID:          ID(model.ResourceKindAction, action.ID),
		Kind:        model.ResourceKindAction,
		Ref:         action.Name,
		IntentID:    action.IntentID,
		HolderRunID: action.RunID,
		Generation:  action.Generation,
		Version:     number(action.Version),
		UpdatedAt:   action.UpdatedAt,
	}
	switch action.State {
	case model.ActionStarted:
		row.State = model.ResourceStateBusy
	case model.ActionCompleted:
		row.State = model.ResourceStateCompleted
	default:
		// unknown (or an unrecognised historical value) must not be guessed
		// into completed: the external side effect may or may not have happened.
		row.State = model.ResourceStateUnknown
	}
	detail := map[string]string{}
	setDetail(detail, "name", action.Name)
	setDetail(detail, "result_ref", action.ResultRef)
	setDetail(detail, "resolved_by_run", action.ResolvedByRun)
	row.Detail = emptyToNil(detail)
	row.Query = queryFor(row.ID)
	return row
}

// resourceForSession projects the direction's current model session. Releasing
// an execution slot does not destroy the session, so an ended run's session is
// still named here as the site to hand over; only an unreadable or crashed
// session is reported as recovery/unknown.
func (s *Service) resourceForSession(in model.Intent) (model.Resource, bool) {
	if in.RunID == "" {
		return model.Resource{}, false
	}
	run, ok := s.Ledger.GetRun(in.RunID)
	if !ok || run.Purpose != "" {
		return model.Resource{}, false
	}
	return s.resourceForRun(run), true
}

// resourceForRun maps the run status to a kind-agnostic state. The session id
// is the run id: the native harness session id is not the control plane's
// identity and must not be used to find a resource.
func (s *Service) resourceForRun(run model.Run) model.Resource {
	row := model.Resource{
		ID:          ID(model.ResourceKindSession, run.ID),
		Kind:        model.ResourceKindSession,
		IntentID:    run.IntentID,
		HolderRunID: run.ID,
		Generation:  run.Generation,
		Version:     run.ID,
		UpdatedAt:   timestamp(run.EndedAt, run.StartedAt),
	}
	switch run.Status {
	case model.RunRunning:
		row.State = model.ResourceStateBusy
	case model.RunDone:
		row.State = model.ResourceStateFree
	case model.RunCanceled:
		if run.CancelCause == model.CancelCauseRestart {
			// The process that owned this harness session is gone; the AOP
			// session pointer may be stale, so reuse needs an explicit step.
			row.State = model.ResourceStateRecovery
		} else {
			row.State = model.ResourceStateFree
		}
	case model.RunFailed:
		row.State = model.ResourceStateUnknown
	default:
		row.State = model.ResourceStateUnknown
	}
	detail := map[string]string{}
	setDetail(detail, "profile", run.Profile)
	setDetail(detail, "resolved_profile", run.ResolvedProfile)
	setDetail(detail, "model", run.Model)
	setDetail(detail, "continued_from", run.ContinuedFrom)
	row.Detail = emptyToNil(detail)
	row.Query = queryFor(row.ID)
	return row
}

// queryFor returns the read path that reproduces one projection.
func queryFor(id string) string { return "/api/resources/" + id }

// emptyToNil keeps the JSON response free of empty maps.
func emptyToNil(detail map[string]string) map[string]string {
	if len(detail) == 0 {
		return nil
	}
	return detail
}
