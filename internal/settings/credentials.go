package settings

// Credential-generation and rotation state.
//
// The key pool of a profile and the cursor that selects the key in it are
// instance-wide runtime state (profiles.go). A project has its own api.Server,
// so "is this profile rotating / cooling down / out of keys" must not be kept
// per Server: two projects dispatching on the same profile would each advance
// the shared cursor, and a failure observed by one project could pause it even
// when another project already rotated the profile onto a working key.
//
// This file therefore owns the non-sensitive bookkeeping only:
//
//   - generation: an opaque counter, bumped once per successful key rotation;
//   - the rotation state machine per profile (ready / rotating / cooling /
//     no_spare / exhausted);
//   - the generations that failed in the current failure episode.
//
// It never stores or logs key material: a generation is a number, and the pool
// size is a count. The cursor and the actual keys stay in profiles.go.

import (
	"sync"
	"time"
)

// defaultCredentialCooldown bounds how often one profile may be rotated after a
// successful rotation. A failure storm from one dead key must not burn through
// the whole pool one key per run; ten minutes is long enough for a human to
// notice the log line. Store.SetCredentialCooldown shortens it for tests.
const defaultCredentialCooldown = 10 * time.Minute

// CredentialState is the rotation state of one profile's credential pool.
type CredentialState string

const (
	// CredentialReady: no rotation is in flight and no cooldown is active; a
	// current-generation failure may rotate if the pool has an untried credential.
	CredentialReady CredentialState = "ready"
	// CredentialRotating: a rotation is in flight (single-flight claim). A second
	// failure of the same generation must not start another rotation.
	CredentialRotating CredentialState = "rotating"
	// CredentialCooling: a rotation just succeeded and another rotation is not
	// allowed yet. A failure in this window is not proof that the pool is dead.
	CredentialCooling CredentialState = "cooling"
	// CredentialNoSpare: the profile has at most one credential, so there is
	// nothing to rotate to.
	CredentialNoSpare CredentialState = "no_spare"
	// CredentialExhausted: every credential in the pool has been tried in the
	// current failure episode.
	CredentialExhausted CredentialState = "exhausted"
)

func (s CredentialState) String() string { return string(s) }

// CredentialFailure is the owner's decision for one failed run. It is a
// snapshot: the caller acts on it and then reports the result back through
// CompleteCredentialRotation or AbortCredentialRotation, so the single-flight
// claim cannot leak when the rotation cannot happen.
type CredentialFailure struct {
	// Profile is the resolved profile id the failed run was bound to.
	Profile string
	// RunGeneration is the generation frozen on the failed run at dispatch.
	RunGeneration uint64
	// Generation is the profile's current generation after this call.
	Generation uint64
	// State is the rotation state observed for this failure.
	State CredentialState
	// Rotate is true when the caller holds the single-flight rotation claim and
	// must now rotate exactly one key. The claim is released by exactly one of
	// CompleteCredentialRotation / AbortCredentialRotation.
	Rotate bool
	// Claim fences the one rotation owner, including retries at the same generation.
	Claim uint64
	// Stale is true when the run failed on an older generation than the
	// profile's current one: the episode was already handled for that
	// generation, and the caller must not rotate, fall back, retry or pause.
	Stale bool
	// PoolSize is the profile's configured credential count and Attempted is how
	// many distinct generations have failed in the current episode. Both are for
	// the operator log; PoolSize <= 1 is "no spare".
	PoolSize  int
	Attempted int
}

// credentialEpisode is the runtime state of one profile. attempted holds
// generations, never keys.
type credentialEpisode struct {
	claim       uint64
	generation  uint64
	state       CredentialState
	coolingTill time.Time
	attempted   map[uint64]bool
}

// credentialRegistry is the per-profile credential runtime state of a Store. It
// is embedded in the settings store so every project shares one answer.
type credentialRegistry struct {
	nextClaim uint64
	mu        sync.Mutex
	episodes  map[string]*credentialEpisode
	cooldown  time.Duration
	set       bool
}

// cooldownWindow returns the configured cooling window. LoadAt installs the
// production default; a zero-value Store that never called the setter still
// behaves like production instead of silently disabling the cooldown.
func (r *credentialRegistry) cooldownWindow() time.Duration {
	if !r.set {
		return defaultCredentialCooldown
	}
	return r.cooldown
}

func (r *credentialRegistry) episode(profile string) *credentialEpisode {
	if r.episodes == nil {
		r.episodes = map[string]*credentialEpisode{}
	}
	ep := r.episodes[profile]
	if ep == nil {
		ep = &credentialEpisode{state: CredentialReady, attempted: map[uint64]bool{}}
		r.episodes[profile] = ep
	}
	return ep
}

// begin records one failed generation and returns the shared decision. It is
// the only place the state machine advances, so stale/duplicate detection and
// the single-flight rotation claim cannot disagree across projects.
func (r *credentialRegistry) begin(profile string, runGeneration uint64, poolSize int) CredentialFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep := r.episode(profile)
	now := time.Now()
	plan := CredentialFailure{Profile: profile, RunGeneration: runGeneration, PoolSize: poolSize}

	// An expired cooldown is ready again. The transition lives here (not in a
	// timer goroutine) so it happens exactly once and only when asked.
	if ep.state == CredentialCooling && !now.Before(ep.coolingTill) {
		ep.state = CredentialReady
	}
	// no_spare / exhausted describe the configuration and episode that produced
	// them. Adding credentials is the operator's fix; it must give the episode
	// another credential to try instead of leaving the profile stuck.
	if ep.state == CredentialNoSpare || ep.state == CredentialExhausted {
		if poolSize > len(ep.attempted) {
			ep.state = CredentialReady
		}
	}

	// A failure bound to an older generation is a duplicate of an episode that
	// was already handled: acting on it would let a stale callback rotate again,
	// fall back, retry or pause a project that already runs on a newer key.
	if runGeneration < ep.generation {
		plan.Stale = true
		plan.Generation = ep.generation
		plan.State = ep.state
		plan.Attempted = len(ep.attempted)
		return plan
	}
	// The counter is runtime state: after a restart a persisted run row can
	// carry a larger generation than this process has handed out. Adopt it
	// instead of treating every failure of that run as "from the future".
	if runGeneration > ep.generation {
		ep.generation = runGeneration
	}
	// Mark this generation failed in the shared episode (idempotent).
	ep.attempted[runGeneration] = true
	plan.Generation = ep.generation
	plan.Attempted = len(ep.attempted)

	switch ep.state {
	case CredentialRotating:
		// A rotation for this profile is already in flight; this failure is the
		// duplicate the old code turned into a cooldown + fallback + pause.
		plan.State = CredentialRotating
		return plan
	case CredentialCooling:
		// A rotation just succeeded. A failure inside the cooling window does not
		// prove the new key (or the whole pool) is unusable.
		plan.State = CredentialCooling
		return plan
	}

	switch {
	case poolSize <= 1:
		ep.state = CredentialNoSpare
		plan.State = CredentialNoSpare
	case len(ep.attempted) >= poolSize:
		ep.state = CredentialExhausted
		plan.State = CredentialExhausted
	default:
		ep.state = CredentialRotating
		r.nextClaim++
		ep.claim = r.nextClaim
		plan.State = CredentialRotating
		plan.Rotate = true
		plan.Claim = ep.claim
	}
	return plan
}

// complete closes a successful rotation: the generation advances and the
// cooling window opens.
func (r *credentialRegistry) complete(profile string, claim uint64) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep := r.episode(profile)
	if claim == 0 || ep.state != CredentialRotating || ep.claim != claim {
		return ep.generation, false
	}
	ep.generation++
	ep.claim = 0
	ep.state = CredentialCooling
	ep.coolingTill = time.Now().Add(r.cooldownWindow())
	return ep.generation, true
}

// abort releases a rotation claim whose rotation did not happen. It does not
// open the cooling window: no new key was tried, so the next failure may retry.
func (r *credentialRegistry) abort(profile string, claim uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ep := r.episodes[profile]; ep != nil && claim != 0 && ep.claim == claim && ep.state == CredentialRotating {
		ep.state = CredentialReady
		ep.claim = 0
		return true
	}
	return false
}

// succeed ends the current failure episode: the credential worked, so a later
// failure starts from scratch instead of inheriting an "already tried" set. It
// reports whether there was an episode to end, so the caller only logs a reset
// when one actually happened.
func (r *credentialRegistry) succeed(profile string, generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep := r.episodes[profile]
	if ep == nil || generation != ep.generation || ep.state == CredentialRotating {
		return false
	}
	changed := len(ep.attempted) > 0 || ep.state != CredentialReady || !ep.coolingTill.IsZero()
	ep.state = CredentialReady
	ep.coolingTill = time.Time{}
	ep.attempted = map[uint64]bool{}
	return changed
}

func (r *credentialRegistry) generation(profile string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ep := r.episodes[profile]; ep != nil {
		return ep.generation
	}
	return 0
}

// state returns the current state, normalising an expired cooling window so a
// caller/test never observes "cooling" after the window has passed.
func (r *credentialRegistry) state(profile string) (CredentialState, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep := r.episodes[profile]
	if ep == nil {
		return CredentialReady, 0
	}
	if ep.state == CredentialCooling && !time.Now().Before(ep.coolingTill) {
		return CredentialReady, ep.generation
	}
	return ep.state, ep.generation
}

// ---- Store entry points ----
//
// These are the shared owner's API for the api layer. They never read or expose
// key material: the profile's pool is read only to count its entries.

// CredentialGeneration is the profile's current credential generation. A
// dispatch freezes this on the run so a provider failure can be attributed to
// the generation the run actually used.
func (s *Store) CredentialGeneration(profile string) uint64 {
	return s.creds.generation(profile)
}

// BeginCredentialFailure records that one run bound to runGeneration failed on a
// credential error and returns the shared episode's decision. An unknown profile
// is an error: the caller must not guess a pool size.
func (s *Store) BeginCredentialFailure(profile string, runGeneration uint64) (CredentialFailure, error) {
	p, err := s.SavedProfile(profile)
	if err != nil {
		return CredentialFailure{}, err
	}
	return s.creds.begin(p.ID, runGeneration, len(keyPool(p))), nil
}

// CompleteCredentialRotation reports that the single-flight rotation claim was
// honoured and a new key is current. It returns the new generation.
func (s *Store) CompleteCredentialRotation(profile string, claim uint64) (uint64, bool) {
	return s.creds.complete(profile, claim)
}

// AbortCredentialRotation releases the claim when no new key was tried (no
// spare, rotation error), so a later failure may try again.
func (s *Store) AbortCredentialRotation(profile string, claim uint64) bool {
	return s.creds.abort(profile, claim)
}

// NoteCredentialSuccess ends the profile's current failure episode. It is called
// when a run on that profile succeeds: a working credential proves the outage is
// over, so the next failure must not inherit the old "every key was tried" set.
// It reports whether an episode was actually reset.
func (s *Store) NoteCredentialSuccess(profile string, generation uint64) bool {
	return s.creds.succeed(profile, generation)
}

// CredentialState reports one profile's rotation state and generation. It is
// read-only observation for tests and logging, never a credential.
func (s *Store) CredentialState(profile string) (CredentialState, uint64) {
	return s.creds.state(profile)
}

// SetCredentialCooldown overrides the cooling window after a successful
// rotation. The default is ten minutes; tests use a shorter (or zero) window to
// exercise the cooling/exhausted transitions without waiting.
func (s *Store) SetCredentialCooldown(d time.Duration) {
	s.creds.mu.Lock()
	defer s.creds.mu.Unlock()
	s.creds.cooldown = d
	s.creds.set = true
}
