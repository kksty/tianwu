// Package project makes the engagement the unit of isolation.
//
// One process serves several engagements. Each one is a directory under
// <data>/projects/<id> and owns its ledger, decider memory, workspaces, imports
// and bundles. Two projects share no state: a fact declared in one is invisible
// to the other because each decider reads only its own store (docs/02, D33).
//
// The instance-level directories stay shared on purpose: arsenal (tools),
// pocs (templates) and skills (house style) are tooling, not findings. A tool
// installed once is available everywhere; a credential found once is not.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/store"
)

// ManifestFile is the identity of a project on disk. The directory name is the
// id used in URLs; the manifest carries the display name and the authorised
// scope, which belong to the engagement rather than to a flag.
const ManifestFile = "project.json"

// DefaultID is created on first start so the console is usable immediately.
const DefaultID = "default"

// PauseKind values are persisted next to the paused flag so a restart can still
// say who stopped the project. They are machine codes: the console and the
// resume contract switch on them, never on the human-readable reason prose.
// api.pauseKind* and cmd/tw's boot reason derive from these constants, so the
// runtime and the manifest cannot drift apart.
const (
	PauseKindManualPause       = "manual_pause"
	PauseKindCompletionStandby = "completion_standby"
	PauseKindCommanderStandby  = "commander_standby"
)

type Manifest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Scope is the operator's free-text current boundary for this engagement.
	// It is injected into briefs as a snapshot of the current wording and is
	// never parsed, matched against targets, or enforced: assets are added and
	// removed during a test, and the operator updates the boundary through
	// messages/the dispatch window instead of editing a whitelist (D59).
	Scope string `json:"scope,omitempty"`
	// Description is the operator's prose for this engagement: authorised
	// boundaries, forbidden actions, platform rules, how to treat target output.
	// The program injects it into every brief and never parses it (D59).
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Paused      bool      `json:"paused,omitempty"`
	// PauseKind is the structured cause of Paused: manual_pause,
	// completion_standby or commander_standby. It is written by the control plane
	// (the project package does not import api), only meaningful while Paused, and
	// cleared on resume. Persisting it is what keeps a commander stop from coming
	// back as "the operator paused the project" after a restart (types.ts
	// project_pause_kind is the machine code the console classifies from).
	PauseKind string `json:"pause_kind,omitempty"`
	// MaxRuns overrides the instance --max-runs for this engagement. Nil means
	// "inherit the instance default"; a pointer is what distinguishes that from
	// 0, which is a deliberate "uncapped". It is a dispatch knob, not a fact:
	// changing it never touches the ledger.
	MaxRuns *int `json:"max_runs,omitempty"`
	// VerifyAuto overrides the instance --verify-auto for this engagement: true
	// starts one review after each intent run finishes, false leaves
	// verification to the operator (the vulns page has single and batch
	// buttons), nil inherits the instance default. It is a dispatch policy, not
	// a fact: changing it never touches the ledger.
	VerifyAuto *bool `json:"verify_auto,omitempty"`
	// RoundTimeout overrides the instance --round-timeout for this engagement:
	// a Go duration ("15m", "90s"). Empty means "inherit the instance default";
	// "0" is a deliberate "no wall-clock cap". It bounds one commander decision
	// round, so a session that parks itself inside a round cannot hold every
	// later decision behind it (D91). ParseRoundTimeout is the decoder.
	RoundTimeout string `json:"round_timeout,omitempty"`
	// Nodes is the allowlist of execution locations this engagement may use.
	// A human verifies reachability on the node (terminal) and then binds it;
	// the decider never picks a location (docs/14-nodes.md).
	Nodes []string `json:"nodes,omitempty"`
	// DefaultNode is where intents without an explicit node run. It is always
	// one of Nodes, or empty when nothing is bound.
	DefaultNode string `json:"default_node,omitempty"`
	// NodesConfigured distinguishes "never bound" (false) from "deliberately
	// cleared" (true, with an empty Nodes). It lets a single-node instance
	// bind itself exactly once without overriding a human's choice to unbind.
	NodesConfigured bool `json:"nodes_configured,omitempty"`
	// WorkerProfile / StrongProfile are the project-level model defaults for
	// the two tiers: worker is where intents and child intents that name no
	// profile run (cheap default), strong is what the commander prefers for
	// itself and for hard directions. Empty means "inherit the instance active
	// profile", which is the pre-tier behaviour and keeps old projects working.
	WorkerProfile string `json:"worker_profile,omitempty"`
	StrongProfile string `json:"strong_profile,omitempty"`
	// PeerPolicy governs who may message whom inside the project. Nil or an
	// empty mode means "open": every participant may message every other one,
	// which is the pre-policy behaviour and keeps old projects working. In
	// "allowlist" mode only the operator-listed (from,to) pairs may message;
	// the commander channel and a participant talking to itself are exempt
	// (the commander is the operator's channel, and self-messages are refused
	// before authorization is consulted).
	PeerPolicy *PeerPolicy `json:"peer_policy,omitempty"`
	// PremisePolicy is always "verified_only" (证明优先): every necessary premise
	// must be a reviewable kind, verified (a pass review bound to its current
	// claim version) with intact original evidence, before the direction can be
	// dispatched. Legacy standard/strict manifests are migrated to this value
	// when loaded and are no longer served as a read-only mode.
	PremisePolicy string `json:"premise_policy,omitempty"`
}

// Peer rule modes. Open is the default when the manifest has no policy.
const (
	PeerPolicyOpen      = "open"
	PeerPolicyAllowlist = "allowlist"
)

// PeerRule is one authorised direction of conversation: From may message To.
// Both are participant ids ("pt_in_x") or "commander"; the pair is directional.
type PeerRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PeerPolicy is the operator's message-authorization rule set. Mode is open
// (default) or allowlist; Allow is only consulted in allowlist mode.
type PeerPolicy struct {
	Mode  string     `json:"mode,omitempty"`
	Allow []PeerRule `json:"allow,omitempty"`
}

// Project is one engagement. Dir is its root; everything engagement-scoped is
// below it, so deleting the directory deletes the project and nothing else.
type Project struct {
	Manifest
	Dir     string
	CaseDir string
	Store   *store.Store

	// mu guards the manifest fields the console updates while dispatchers and
	// pollers read them (node binding, scope, description).
	mu sync.RWMutex
	// writeMu serialises the manifest rewrite. It is separate from mu because
	// several callers already hold mu when they persist.
	writeMu sync.Mutex
}

// ScopeText and DescriptionText are the locked reads. The hub used to read the
// embedded fields directly, which raced with PUT /api/projects/{id}.
func (p *Project) ScopeText() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Scope
}

func (p *Project) IsPaused() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Paused
}

// Pause is operator state, not a fact. Persist it with the manifest so a
// control-plane restart cannot silently resume an engagement and spend tokens.
//
// SetPaused keeps the existing structured cause: pausing is a transition, not a
// reclassification (a boot pause must not rewrite a persisted commander stop),
// and resuming always clears it. Callers that know the cause use
// SetPausedWithKind.
func (p *Project) SetPaused(paused bool) error {
	return p.SetPausedWithKind(paused, "")
}

// SetPausedWithKind persists the paused flag and its structured cause in one
// manifest write. An empty kind on pause keeps whatever the manifest already
// carries; a non-empty kind records the new cause; resume always clears it. A
// failed write rolls both fields back so memory and disk cannot disagree.
func (p *Project) SetPausedWithKind(paused bool, kind string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	beforePaused, beforeKind := p.Paused, p.PauseKind
	p.Paused = paused
	switch {
	case !paused:
		p.PauseKind = ""
	case kind != "":
		p.PauseKind = kind
	}
	if err := p.write(); err != nil {
		p.Paused, p.PauseKind = beforePaused, beforeKind
		return err
	}
	return nil
}

// PauseKindOf returns the persisted pause cause ("" for a legacy pause or a
// running project). It is a machine code, never display prose.
func (p *Project) PauseKindOf() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.PauseKind
}

func (p *Project) DescriptionText() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Description
}

// NodeBinding returns the project's node allowlist and default node.
func (p *Project) NodeBinding() ([]string, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.Nodes...), p.DefaultNode
}

// MaxRunsOverride reports the project's own concurrency cap. ok is false when
// the manifest has none, in which case dispatch uses the instance --max-runs.
func (p *Project) MaxRunsOverride() (int, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.MaxRuns == nil {
		return 0, false
	}
	return *p.MaxRuns, true
}

// VerifyAutoOverride reports the project's own review policy. ok is false when
// the manifest has none, in which case the hub uses the instance --verify-auto.
func (p *Project) VerifyAutoOverride() (bool, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.VerifyAuto == nil {
		return false, false
	}
	return *p.VerifyAuto, true
}

// RoundTimeoutRaw is the project's own commander round wall clock, as stored
// (a duration string). Empty means "inherit the instance --round-timeout";
// ParseRoundTimeout turns it into the duration the decider applies.
func (p *Project) RoundTimeoutRaw() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.RoundTimeout
}

// ProfileDefaults returns the project's model-tier defaults. Empty strings mean
// "inherit the instance active profile", not "an empty profile id".
func (p *Project) ProfileDefaults() (worker, strong string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.WorkerProfile, p.StrongProfile
}

// PeerPolicyView returns the effective peer policy: an absent manifest policy
// is reported as open rather than as an empty mode, so callers (console, agent
// roster) can show what actually applies.
func (p *Project) PeerPolicyView() PeerPolicy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return effectivePeerPolicy(p.PeerPolicy)
}

// PeerAllowed reports whether from may message to under the current policy. It
// is the one authorization rule the message path uses; policy edits and reads
// share the project lock, so a send cannot observe half an update.
func (p *Project) PeerAllowed(from, to string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pol := effectivePeerPolicy(p.PeerPolicy)
	if pol.Mode != PeerPolicyAllowlist {
		return true
	}
	for _, rule := range pol.Allow {
		if rule.From == from && rule.To == to {
			return true
		}
	}
	return false
}

// SetPeerPolicy validates and persists the operator's message-authorization
// rules. An empty mode is normalised to open; unknown modes and empty rule
// sides are errors rather than a silent downgrade (a typo must not quietly
// re-open or close the project's channels).
func (p *Project) SetPeerPolicy(pol PeerPolicy) (PeerPolicy, error) {
	normalized, err := normalizePeerPolicy(pol)
	if err != nil {
		return PeerPolicy{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.PeerPolicy = &normalized
	if err := p.write(); err != nil {
		return PeerPolicy{}, err
	}
	return normalized, nil
}

// effectivePeerPolicy fills in the open default. Caller holds p.mu (or owns the
// value, as at load time).
func effectivePeerPolicy(pol *PeerPolicy) PeerPolicy {
	if pol == nil {
		return PeerPolicy{Mode: PeerPolicyOpen}
	}
	out := PeerPolicy{Mode: strings.ToLower(strings.TrimSpace(pol.Mode)), Allow: append([]PeerRule(nil), pol.Allow...)}
	if out.Mode == "" {
		out.Mode = PeerPolicyOpen
	}
	return out
}

func normalizePeerPolicy(pol PeerPolicy) (PeerPolicy, error) {
	mode := strings.ToLower(strings.TrimSpace(pol.Mode))
	switch mode {
	case "":
		mode = PeerPolicyOpen
	case PeerPolicyOpen, PeerPolicyAllowlist:
	default:
		return PeerPolicy{}, fmt.Errorf("peer_policy.mode 只能是 open 或 allowlist（收到 %q）", pol.Mode)
	}
	seen := make(map[PeerRule]bool, len(pol.Allow))
	allow := make([]PeerRule, 0, len(pol.Allow))
	for _, rule := range pol.Allow {
		from, to := strings.TrimSpace(rule.From), strings.TrimSpace(rule.To)
		if from == "" || to == "" {
			return PeerPolicy{}, errors.New("peer_policy.allow 的 from 与 to 都必填")
		}
		if from == to {
			return PeerPolicy{}, fmt.Errorf("peer_policy.allow 不允许自己发给自己：%s", from)
		}
		clean := PeerRule{From: from, To: to}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		allow = append(allow, clean)
	}
	return PeerPolicy{Mode: mode, Allow: allow}, nil
}

// SetNodeBinding replaces the allowlist. The empty list is legal and means
// "nothing can run here yet"; the default must be one of the bound nodes.
func (p *Project) SetNodeBinding(nodes []string, def string) error {
	seen := make(map[string]bool, len(nodes))
	clean := make([]string, 0, len(nodes))
	for _, id := range nodes {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	def = strings.TrimSpace(def)
	if def != "" && !seen[def] {
		return errors.New("默认节点必须在绑定列表里")
	}
	if len(clean) > 0 && def == "" {
		return errors.New("绑定了节点就要选一个默认节点")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Nodes, p.DefaultNode, p.NodesConfigured = clean, def, true
	return p.write()
}

// BindingConfigured reports whether a human (or the single-node auto-bind) has
// written a binding. An unconfigured project is eligible for the one-machine
// default; a configured empty one is a deliberate "nothing may run here".
func (p *Project) BindingConfigured() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.NodesConfigured
}

func (p *Project) LedgerPath() string    { return filepath.Join(p.Dir, "ledger.sqlite") }
func (p *Project) StatePath() string     { return filepath.Join(p.Dir, "state.md") }
func (p *Project) DecisionsPath() string { return filepath.Join(p.Dir, "decisions.jsonl") }
func (p *Project) RunsDir() string       { return filepath.Join(p.Dir, "runs") }

// Manager owns the open projects of one instance. It never opens the same
// ledger twice: two Store values over one file would overwrite each other.
type Manager struct {
	Root string

	mu   sync.RWMutex
	byID map[string]*Project
	ids  []string
}

// Open loads every project directory below root, creating root if needed.
// A directory without a manifest is adopted: hand-made directories are a
// legitimate way to restore an engagement.
func Open(root string) (*Manager, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	m := &Manager{Root: root, byID: map[string]*Project{}}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p, err := load(root, e.Name())
		if err != nil {
			return nil, err
		}
		m.byID[p.ID] = p
		m.ids = append(m.ids, p.ID)
	}
	m.sort()
	return m, nil
}

func load(root, dir string) (*Project, error) {
	d := filepath.Join(root, dir)
	if _, err := os.Stat(filepath.Join(d, "ledger.sqlite")); err != nil {
		return nil, fmt.Errorf("project %s requires a current SQLite ledger: %w", dir, err)
	}
	p := &Project{Dir: d, CaseDir: filepath.Join(d, "cases")}
	migrated := false
	if b, err := os.ReadFile(filepath.Join(d, ManifestFile)); err == nil {
		if err := json.Unmarshal(b, &p.Manifest); err != nil {
			return nil, fmt.Errorf("parse %s: %w", d, err)
		}
	}
	// One-way, one-time migration: standard/strict/no policy all mean
	// verified_only now. The old value is not served as a compatibility mode;
	// writing it here makes the migration explicit in the manifest and keeps the
	// in-memory project consistent with disk before any dispatch.
	if strings.TrimSpace(p.PremisePolicy) != PremisePolicyVerifiedOnly {
		p.PremisePolicy = PremisePolicyVerifiedOnly
		migrated = true
	}
	// The directory name is the address the console uses; trust it over a
	// manifest that may have been copied from another project.
	p.ID = dir
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.Created.IsZero() {
		if info, err := os.Stat(filepath.Join(d, "ledger.sqlite")); err == nil {
			p.Created = info.ModTime()
		} else {
			p.Created = time.Now()
		}
	}
	if err := os.MkdirAll(p.CaseDir, 0o755); err != nil {
		return nil, err
	}
	st, err := store.Open(p.LedgerPath())
	if err != nil {
		return nil, err
	}
	p.Store = st
	if _, err := os.Stat(filepath.Join(d, ManifestFile)); os.IsNotExist(err) {
		_ = p.write()
	} else if migrated {
		if err := p.write(); err != nil {
			return nil, fmt.Errorf("migrate %s to verified_only: %w", d, err)
		}
		log.Printf("project %s: premise_policy migrated to verified_only", p.ID)
	}
	return p, nil
}

func (m *Manager) sort() {
	sort.SliceStable(m.ids, func(i, j int) bool {
		a, b := m.byID[m.ids[i]], m.byID[m.ids[j]]
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
}

// All returns the projects in creation order.
func (m *Manager) All() []*Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Project, 0, len(m.ids))
	for _, id := range m.ids {
		out = append(out, m.byID[id])
	}
	return out
}

func (m *Manager) Get(id string) *Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byID[id]
}

func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.ids)
}

// First returns the project the console opens on / when --project is unset.
func (m *Manager) First() *Project {
	all := m.All()
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

// Create makes a new engagement. The directory id is a short random id,
// independent of the display name: renaming the engagement never moves its
// directory, and a name/brief naming a target host or URL does not leak into the
// on-disk path or the console URL. The readable name lives in the manifest.
func (m *Manager) Create(name, scope string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("项目名不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.create(newProjectID(), name, scope)
}

// newProjectID is the stable, opaque directory address. It is deliberately
// short so URLs stay readable even though they no longer spell the project name.
func newProjectID() string { return "p-" + shortID()[:8] }

// EnsureDefault returns the default project, creating it on first start so the
// console has something to show. It is always called with the fixed id so the
// documented /p/default/ address holds.
func (m *Manager) EnsureDefault(name, scope string) (*Project, error) {
	if p := m.Get(DefaultID); p != nil {
		return p, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.byID[DefaultID]; ok {
		return p, nil
	}
	p, err := m.create(DefaultID, name, scope)
	if err != nil {
		return nil, err
	}
	// A fresh default is a new engagement: proof-first. An existing default
	// manifest is untouched and stays legacy standard until the operator
	// explicitly migrates it in project settings.
	if _, err := p.SetPremisePolicy(PremisePolicyVerifiedOnly); err != nil {
		return nil, err
	}
	return p, nil
}

// create must be called with the write lock held.
func (m *Manager) create(base, name, scope string) (*Project, error) {
	id := m.uniqueID(base)
	d := filepath.Join(m.Root, id)
	if err := os.MkdirAll(filepath.Join(d, "cases"), 0o755); err != nil {
		return nil, err
	}
	p := &Project{
		Manifest: Manifest{ID: id, Name: name, Scope: strings.TrimSpace(scope), Created: time.Now(), PremisePolicy: PremisePolicyVerifiedOnly},
		Dir:      d,
		CaseDir:  filepath.Join(d, "cases"),
	}
	st, err := store.OpenSQLite(filepath.Join(p.Dir, "ledger.sqlite"))
	if err != nil {
		return nil, err
	}
	p.Store = st
	if err := p.write(); err != nil {
		return nil, err
	}
	m.byID[id] = p
	m.ids = append(m.ids, id)
	m.sort()
	return p, nil
}

func (m *Manager) uniqueID(base string) string {
	if base == "" {
		base = "prj"
	}
	if _, taken := m.byID[base]; !taken {
		if _, err := os.Stat(filepath.Join(m.Root, base)); os.IsNotExist(err) {
			return base
		}
	}
	for i := 0; i < 64; i++ {
		id := base + "-" + shortID()
		if _, taken := m.byID[id]; taken {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.Root, id)); os.IsNotExist(err) {
			return id
		}
	}
	return base + "-" + shortID()
}

// Remove forgets a project whose directory is already gone. It does not touch
// the disk: deleting the tree is the caller's job, because only the caller
// knows whether a run is still writing into it.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return
	}
	delete(m.byID, id)
	for i, v := range m.ids {
		if v == id {
			m.ids = append(m.ids[:i], m.ids[i+1:]...)
			break
		}
	}
}

// SetScope rewrites the authorised scope of an engagement without touching the
// ledger: scope is a property of the engagement, not a fact about it.
func (m *Manager) SetScope(id, scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	p.Scope = strings.TrimSpace(scope)
	err := p.write()
	p.mu.Unlock()
	return err
}

// SetDescription writes the operator's prose for this engagement. The program
// never parses it; it only carries it into every brief (D59).
func (m *Manager) SetDescription(id, description string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	p.Description = strings.TrimSpace(description)
	err := p.write()
	p.mu.Unlock()
	return err
}

// SetMaxRuns writes the project's own concurrency cap; nil restores "inherit
// the instance default". 0 is legal and means uncapped. The value is copied so
// the caller cannot mutate the manifest after the write.
func (m *Manager) SetMaxRuns(id string, limit *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if limit == nil {
		p.MaxRuns = nil
	} else {
		v := *limit
		p.MaxRuns = &v
	}
	return p.write()
}

// SetVerifyAuto writes the project's own review policy; nil restores "inherit
// the instance --verify-auto". The value is copied so the caller cannot mutate
// the manifest after the write. It only affects runs started from now on, and
// never interrupts a review already in flight.
func (m *Manager) SetVerifyAuto(id string, auto *bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if auto == nil {
		p.VerifyAuto = nil
	} else {
		v := *auto
		p.VerifyAuto = &v
	}
	return p.write()
}

// ParseRoundTimeout decodes a manifest / console round wall-clock value. Empty
// means "inherit the instance default" (ok=false). "0" is a deliberate
// unlimited (ok=true, zero duration). Malformed and negative values are errors:
// a typo must not silently become "inherit".
func ParseRoundTimeout(raw string) (time.Duration, bool, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false, fmt.Errorf("round_timeout 不是合法时长（如 15m / 90s / 0）：%q", s)
	}
	if d < 0 {
		return 0, false, errors.New("round_timeout 不能是负数（0 = 不限）")
	}
	return d, true, nil
}

// SetRoundTimeout writes the project's own commander round wall clock; "" clears
// it and restores "inherit the instance --round-timeout", "0" means unlimited.
// The value is validated here so an invalid file cannot reach the decider
// through the console.
func (m *Manager) SetRoundTimeout(id, raw string) error {
	s := strings.TrimSpace(raw)
	if _, _, err := ParseRoundTimeout(s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RoundTimeout = s
	return p.write()
}

// SetProfileDefaults writes the project's tier defaults; "" clears one and
// restores "inherit the instance active profile". Validation of the ids (exists
// and runnable) belongs to the control plane, which owns the profile catalogue:
// the manifest is a pointer, not a second copy of that list.
func (m *Manager) SetProfileDefaults(id, worker, strong string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("没有这个项目：%s", id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.WorkerProfile = strings.TrimSpace(worker)
	p.StrongProfile = strings.TrimSpace(strong)
	return p.write()
}

func (p *Project) write() error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	b, err := json.MarshalIndent(&p.Manifest, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replace: two saves (scope vs node binding) used to race on a plain
	// WriteFile, and a half-written project.json makes the whole instance fail
	// to start on the next boot.
	path := filepath.Join(p.Dir, ManifestFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func shortID() string { return store.NewID("p")[2:] }
