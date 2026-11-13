// Package collab issues and resolves the short-lived identity a running
// executor uses to talk to its peers and read the live project graph.
//
// The console token is an instance credential: every local process that
// inherits the host environment could read it, and it does not say which run is
// calling. A participant token is the opposite: one run, one intent, one
// generation, one project, revoked when the run ends. The token never comes
// from the request body; the registry resolves it to the caller, so a run
// cannot claim to be another participant.
package collab

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Identity is the server-side truth about one running executor. Token is only
// returned to the caller that issues it (it goes into the run's case file); the
// API's participant views omit it.
type Identity struct {
	Token         string    `json:"token"`
	ProjectID     string    `json:"project_id"`
	ParticipantID string    `json:"participant_id"`
	IntentID      string    `json:"intent_id"`
	RunID         string    `json:"run_id"`
	Generation    int       `json:"generation"`
	APIBase       string    `json:"api_base"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// CommanderID is the project commander's participant id. It is stable and
// reserved: an intent cannot mint it because participant ids are derived from
// intent ids.
const CommanderID = "commander"

// OperatorID is the person at the console. It is a sender, not a recipient: the
// operator has no session to deliver into, so a message addressed to it is only
// a record. Keeping one id means "who said this" has the same shape for the
// console and the commander, so a say from either is one message row (D86).
const OperatorID = "operator"

// ParticipantID is the stable collaboration identity of an intent. It survives
// run generations: the same direction keeps one address, while the token's
// generation says which holder is currently speaking.
func ParticipantID(intentID string) string { return "pt_" + intentID }

// IntentIDFromParticipant reverses ParticipantID. ok=false means the id is not
// an intent participant (for example the commander).
func IntentIDFromParticipant(participant string) (string, bool) {
	const prefix = "pt_"
	if len(participant) <= len(prefix) || participant[:len(prefix)] != prefix {
		return "", false
	}
	return participant[len(prefix):], true
}

// Registry holds the live tokens of one process. It is deliberately in-memory:
// a token belongs to a live run, and a restart stops that run too. A token that
// survived a restart would be a credential nobody can reason about any more.
type Registry struct {
	ttl time.Duration

	mu     sync.RWMutex
	byHash map[[32]byte]Identity
	byRun  map[string][][32]byte
}

func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Registry{
		ttl:    ttl,
		byHash: map[[32]byte]Identity{},
		byRun:  map[string][][32]byte{},
	}
}

// Issue creates a token for one run. The caller fills ProjectID, ParticipantID,
// IntentID, RunID and Generation; Token and ExpiresAt are assigned here.
func (r *Registry) Issue(in Identity) (Identity, error) {
	if r == nil {
		return Identity{}, errors.New("协作身份未启用")
	}
	if in.ProjectID == "" || in.ParticipantID == "" || in.IntentID == "" || in.RunID == "" {
		return Identity{}, errors.New("协作身份缺少 project/participant/intent/run")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Identity{}, err
	}
	in.Token = hex.EncodeToString(raw[:])
	in.ExpiresAt = time.Now().Add(r.ttl)
	key := sha256.Sum256([]byte(in.Token))
	r.mu.Lock()
	r.byHash[key] = in
	r.byRun[in.RunID] = append(r.byRun[in.RunID], key)
	r.mu.Unlock()
	return in, nil
}

// Resolve maps a bearer token to its identity. Expired entries are dropped on
// the way out, so a long-lived process does not accumulate dead tokens.
func (r *Registry) Resolve(token string) (Identity, bool) {
	if r == nil || token == "" {
		return Identity{}, false
	}
	key := sha256.Sum256([]byte(token))
	r.mu.RLock()
	in, ok := r.byHash[key]
	r.mu.RUnlock()
	if !ok {
		return Identity{}, false
	}
	if !in.ExpiresAt.IsZero() && time.Now().After(in.ExpiresAt) {
		r.revokeHash(key)
		return Identity{}, false
	}
	return in, true
}

// RevokeRun ends every token of one run. It is called when the run settles, so
// a stale executor cannot keep talking after its turn is over.
func (r *Registry) RevokeRun(runID string) {
	if r == nil || runID == "" {
		return
	}
	r.mu.Lock()
	keys := append([][32]byte(nil), r.byRun[runID]...)
	delete(r.byRun, runID)
	for _, key := range keys {
		delete(r.byHash, key)
	}
	r.mu.Unlock()
}

func (r *Registry) revokeHash(key [32]byte) {
	r.mu.Lock()
	delete(r.byHash, key)
	r.mu.Unlock()
}
