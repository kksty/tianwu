package access

// Browser lease ownership fence (item 9 audit).
//
// A lease is identified by its own unique id (bl_…) plus the persisted holder
// identity: owner + run + intent. That is the fence release/recover compare
// against. Generation (the intent's attempt counter) is recorded for display and
// for the API's "is this the current run" check, but it is deliberately NOT part
// of the release fence: attempts can move backwards when a cancelled run
// settles (store.SettleIntentForRun), so using it as a fence can both reject a
// legitimate holder and, if it were the only check, accept a stale one. The
// unique lease id is what makes "old holder cannot release or overwrite a new
// holder" structural: a new acquisition always writes a new id.
//
// Unknown state is never replayed: acquiring over an existing lease returns an
// error, and replacing a lease with unconfirmed native commands is refused by
// the API (operator must confirm the site is idle first). This package has no
// TTL: time passing cannot prove a browser operation ended.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/store"
)

// BrowserLease is a cooperative ownership record for one registered browser
// resource. It has no TTL: time passing cannot prove a browser operation ended.
// Callers serialize mutations with the same lock used for browser commands.
type BrowserLease struct {
	ID string `json:"id"`
	// Owner is the persisted participant id (pt_<intent>) or "operator". It is
	// assigned by the control plane, never inferred from a title/process/port.
	Owner string `json:"owner"`
	// RunID is the holder run for an executor lease; empty for an operator
	// lease. Release requires it to match: a new run of the same intent is a
	// different holder.
	RunID string `json:"run_id,omitempty"`
	// IntentID is the direction the holder run belongs to; empty for an
	// operator lease. Together with Owner and RunID it is the holder identity.
	IntentID string `json:"intent_id,omitempty"`
	// Generation is the intent attempt the holder was on when it acquired. It is
	// display/consistency metadata, not a release fence (attempts can revert).
	Generation int       `json:"generation,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	Note       string    `json:"note,omitempty"`
	// State is a read projection. The API derives recovery when a run is no
	// longer live; reading metadata never modifies resource files.
	State string `json:"state"`
	Busy  bool   `json:"busy,omitempty"`
}

// ErrLeaseHeld reports that a different persisted holder already owns the site.
var ErrLeaseHeld = errors.New("浏览器现场已被其他持有者占用")

// ErrLeaseIdentity reports a malformed holder identity: an executor lease must
// name both its run and its intent, an operator lease neither.
var ErrLeaseIdentity = errors.New("占用者身份不完整")

func readLease(dir string) (*BrowserLease, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "lease.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lease BrowserLease
	if err := json.Unmarshal(raw, &lease); err != nil {
		return nil, fmt.Errorf("浏览器占用记录损坏：%w", err)
	}
	if lease.ID == "" || lease.Owner == "" || lease.AcquiredAt.IsZero() || lease.State != "held" {
		return nil, fmt.Errorf("浏览器占用记录无效，不能假定现场空闲")
	}
	return &lease, nil
}

// sameHolder reports whether two lease records name the same persisted holder
// identity. Generation is intentionally excluded; lease id is compared
// separately by release.
func sameHolder(a, b BrowserLease) bool {
	return a.Owner == b.Owner && a.RunID == b.RunID && a.IntentID == b.IntentID
}

// validateHolder checks that the ownership identity is explicit and internally
// consistent. A half-filled holder (run without intent, or intent without run)
// would make the fence ambiguous, so it is refused instead of guessed.
func validateHolder(owner BrowserLease) error {
	if strings.TrimSpace(owner.Owner) == "" {
		return fmt.Errorf("%w：缺少占用者", ErrLeaseIdentity)
	}
	if (owner.RunID == "") != (owner.IntentID == "") {
		return fmt.Errorf("%w：执行者占用必须同时给出 run 与 intent", ErrLeaseIdentity)
	}
	if owner.RunID != "" && !strings.HasPrefix(owner.Owner, "pt_") {
		return fmt.Errorf("%w：执行者占用的 owner 必须是参与者 id", ErrLeaseIdentity)
	}
	return nil
}

// AcquireBrowser is idempotent for the same persisted holder: the same owner,
// run and intent re-acquiring gets the existing lease back. A different holder
// (including a new run of the same intent) is refused; it never overwrites.
func AcquireBrowser(cases, id string, owner BrowserLease) (BrowserLease, error) {
	entry, err := Get(cases, id)
	if err != nil {
		return BrowserLease{}, err
	}
	if entry.Kind != "browser" {
		return BrowserLease{}, fmt.Errorf("只有浏览器现场需要占用")
	}
	if err := validateHolder(owner); err != nil {
		return BrowserLease{}, err
	}
	if entry.Lease != nil {
		lease := *entry.Lease
		if sameHolder(lease, owner) {
			return lease, nil
		}
		return BrowserLease{}, fmt.Errorf("%w（%s）", ErrLeaseHeld, lease.Owner)
	}
	owner.ID, owner.State, owner.AcquiredAt = store.NewID("bl"), "held", time.Now()
	dir, _ := path(cases, id)
	raw, err := json.Marshal(owner)
	if err != nil {
		return BrowserLease{}, err
	}
	f, err := os.CreateTemp(dir, ".lease-*")
	if err != nil {
		return BrowserLease{}, err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return BrowserLease{}, err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return BrowserLease{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return BrowserLease{}, err
	}
	if err := f.Close(); err != nil {
		return BrowserLease{}, err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "lease.json")); err != nil {
		return BrowserLease{}, err
	}
	return owner, nil
}

// ReleaseBrowser compares the acquisition identity (lease id) and the persisted
// holder identity. A response from a previous turn/tab cannot release a newer
// acquisition: its lease id no longer exists. Generation is not compared; see
// the package comment for why it is not a fence.
func ReleaseBrowser(cases, id, leaseID string, owner BrowserLease) error {
	entry, err := Get(cases, id)
	if err != nil {
		return err
	}
	lease := entry.Lease
	if lease == nil || leaseID == "" || lease.ID != leaseID || !sameHolder(*lease, owner) {
		return fmt.Errorf("占用凭据或持有者已变化，请刷新")
	}
	dir, _ := path(cases, id)
	return os.Remove(filepath.Join(dir, "lease.json"))
}
