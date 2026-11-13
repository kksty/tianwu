package retrieval

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Focus is the task-scoped hot/warm/cold projection of the one ledger. It is an
// additional index in front of the complete premise directory, never a
// replacement for it: cold facts and the full premise list stay readable, and
// nothing is deleted or capped by this view.
const (
	// FocusPageBytes is the byte budget of one focus page. A single row may
	// exceed it (titles and summaries are clipped, so real rows do not), in
	// which case the page holds that one row and the cursor still advances.
	FocusPageBytes = 32 << 10

	// FocusTitleRunes and FocusSummaryRunes keep one row an index entry rather
	// than a fact body: the full text stays in the ledger and is read by id.
	FocusTitleRunes   = 160
	FocusSummaryRunes = 240
)

// Temperature values. Temperature is a per-task judgement about why this task
// is looking at a row; it says nothing about whether the fact is true or valid.
const (
	FocusHot  = "hot"
	FocusWarm = "warm"
	FocusCold = "cold"
)

// Classification reasons. Each one is a recorded reference from the task (or
// its ancestors) or a recorded entity identity — never a title, prose or
// model-output guess. A row may carry several reasons; hot wins over warm.
const (
	FocusReasonRequired   = "required"             // task.FromFactIDs: the hard prerequisites
	FocusReasonPinned     = "pinned"               // task.PinnedFactIDs: explicitly fixed by the operator/commander
	FocusReasonReference  = "reference"            // task.ContextFactIDs: explicitly referenced
	FocusReasonProvenance = "provenance"           // TaskSources: the recorded ancestry of the task
	FocusReasonSameEntity = "same_recorded_entity" // same recorded entity_ref, any branch
)

// FocusContract is the read contract every consumer of the projection gets,
// whether it reads the API page or the frozen `.tw/focus.json` written into a
// case. It is data, not a second policy: the fields below only spell out what
// the existing fields mean, so "hot" cannot be read as "true", a missing
// reference cannot be read as a probability, and a bounded page cannot be read
// as the whole layer. Both surfaces carry the identical strings (they are
// exported so the case index and the API cannot drift apart).
type FocusContract struct {
	// Heat: a per-task reference reason, not truth or verification.
	Heat string `json:"heat"`
	// Status: status values are recorded states; missing/rejected are not
	// "not loaded" and heat does not upgrade them.
	Status string `json:"status"`
	// Page: a bounded page is continued through has_more/next_cursor or the
	// layer query; absence from the page is not absence from the ledger.
	Page string `json:"page"`
}

// The contract strings name semantics only; they never suggest an attack step,
// a role or a tool. Keeping them as one exported value is what makes the model
// input and the human API describe the same projection the same way.
const (
	FocusHeatNote = "temperature 说明本任务为何引用这一行：required/pinned/reference 为 hot，provenance/same_recorded_entity 为 warm，其余为 cold。" +
		"hot 只表示任务依赖该行，不代表该事实为真、已复核或已有证据；是否可信以 status、claim_version 与证据为准。"
	FocusStatusNote = "status 是记录状态：missing 表示被引用但当前快照里没有这一行，rejected 表示该事实已被否定，unverified/verified 是复核状态；" +
		"三者与「本页未加载」不是一回事，热度也不改变任何状态。"
	FocusPageNote = "本页受条数/字节预算限制：has_more 与 next_cursor 是本层继续读取的入口，冷层用 layer=cold；" +
		"未出现在本页不代表该事实不存在、未被任务引用或在账本中消失。"
)

// FocusContractValue is the one contract value both surfaces embed.
func FocusContractValue() FocusContract {
	return FocusContract{Heat: FocusHeatNote, Status: FocusStatusNote, Page: FocusPageNote}
}

// FocusLayerQuery is the complete enumeration entry for one layer of one
// intent. The API and the on-disk manifest build their pointers from this one
// function so a reader can continue the same projection from either side.
func FocusLayerQuery(intentID, layer string) string {
	if layer == "" || layer == "focus" {
		return "/api/focus?intent=" + intentID
	}
	return "/api/focus?intent=" + intentID + "&layer=" + layer
}

// FocusPremisesQuery is the ledger enumeration of the task's complete required
// premise catalogue: the ancestors of the task in the fact-intent graph, which
// include every recorded `from_fact_ids` recursively. It is the
// session-independent alternative to the case-relative `.tw/context.json`
// written for a participant's case, so a commander (no case directory) and an
// operator can still walk the whole premise list. Depth 0 means "all depths".
func FocusPremisesQuery(intentID string) string {
	return "/api/graph?root=intent:" + intentID + "&direction=ancestors&depth=0"
}

// FocusReasonCounts counts how many projected rows carry each recorded reason.
// A row may carry several reasons, so the counts do not sum to the row count;
// they are a layer-shape summary, not a verdict.
func FocusReasonCounts(rows []FocusFact) map[string]int {
	out := map[string]int{}
	for _, row := range rows {
		for _, reason := range row.Reasons {
			out[reason]++
		}
	}
	return out
}

// FocusQuery is one page request over the projection. Cursor and Limit are
// transport details, not part of the query identity: a cursor must stay valid
// when a later request asks for a different page size.
type FocusQuery struct {
	Intent string `json:"intent"`
	Layer  string `json:"layer"`
	Cursor string `json:"-"`
	Limit  int    `json:"-"`
}

// FocusFact is one index row. It carries the summary layer only — the doc body
// (commands, raw output) stays in the ledger and is read by fact id, and a
// missing record_version is honest: it is omitted rather than reported as 0.
type FocusFact struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind,omitempty"`
	Title         string     `json:"title,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	Status        string     `json:"status"`
	Temperature   string     `json:"temperature"`
	Reasons       []string   `json:"reasons"`
	ClaimVersion  uint64     `json:"claim_version,omitempty"`
	RecordVersion *uint64    `json:"record_version,omitempty"`
	SourceRun     string     `json:"source_run_id,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	// Query is the read path for the full record. A missing/rejected premise
	// keeps the same pointer so the reason it is absent is inspectable.
	Query string `json:"query"`
}

// FocusPage is one bounded page plus the whole projection's counts. The counts
// describe the frozen snapshot, not the page, so an operator can see the shape
// without walking every page.
type FocusPage struct {
	Intent     string      `json:"intent_id"`
	Layer      string      `json:"layer"`
	Facts      []FocusFact `json:"facts"`
	Hot        int         `json:"hot"`
	Warm       int         `json:"warm"`
	Cold       int         `json:"cold"`
	IndexQuery string      `json:"index_query"`
	HotQuery   string      `json:"hot_query"`
	WarmQuery  string      `json:"warm_query"`
	ColdQuery  string      `json:"cold_query"`
	FactQuery  string      `json:"fact_query"`
	// Premises is the case-relative complete catalogue a participant's run can
	// read. PremisesQuery is the same list from the ledger, so a commander (who
	// has no case directory) and an operator can enumerate it without the file.
	Premises      string `json:"premises"`
	PremisesQuery string `json:"premises_query"`
	Experience    string `json:"experience_query"`
	// ReasonCounts and Contract make the "why" and the semantics of the page
	// explicit on the same surface that carries the rows; the on-disk focus
	// manifest carries the identical values.
	ReasonCounts map[string]int `json:"reason_counts"`
	Contract     FocusContract  `json:"contract"`
	PageInfo
}

// focusLayerAll is the internal selector for "every layer", used by FocusItems
// (the whole-projection consumer) so the classification lives in one place.
const focusLayerAll = "all"

// FocusItems builds the whole projection. It never drops a fact: the returned
// slice contains every fact in the snapshot, ordered hot → warm → cold and then
// by id. Age does not reorder rows and does not demote a prerequisite: adding
// unrelated new facts cannot evict an old hot clue, which is the regression
// docs/32 stage 3 names.
//
// versions is the frozen record-version map of the same snapshot (see
// store.FactVersionsAt). A fact whose version is absent keeps RecordVersion
// nil; a referenced premise that is not in the snapshot gets an explicit
// missing row. Both states stay distinguishable from a real version.
func FocusItems(db store.DB, intentID string, versions map[string]uint64) ([]FocusFact, error) {
	plan, err := newFocusPlan(db, intentID, focusLayerAll, versions)
	if err != nil {
		return nil, err
	}
	out := make([]FocusFact, 0, len(plan.order))
	for _, item := range plan.order {
		out = append(out, plan.materialize(item))
	}
	return out, nil
}

// focusOrder is one classified row before its summary is materialized. fact is
// an index into the plan's fact snapshot (-1 for a referenced-but-missing row,
// whose row is built once during classification).
type focusOrder struct {
	id          string
	temperature string
	fact        int
	missing     *FocusFact
}

// focusPlan is one whole-projection classification. Every page has to see the
// same hot/warm/cold counts (they describe the snapshot, not the page), so the
// classify pass is linear in the snapshot no matter what. What the plan avoids
// is the per-page cost the audit measured: materializing and sorting a
// FocusFact for every fact on every page. Only the requested layer is ordered,
// and rows are materialized lazily as the page is cut (docs/32: measure first,
// optimize the measured bottleneck, never claim constant complexity).
type focusPlan struct {
	db              store.DB
	versions        map[string]uint64
	reasons         map[string][]string
	hot, warm, cold int
	reasonCounts    map[string]int
	order           []focusOrder
}

func newFocusPlan(db store.DB, intentID, layer string, versions map[string]uint64) (*focusPlan, error) {
	reasons, err := focusReasons(db, intentID)
	if err != nil {
		return nil, err
	}
	p := &focusPlan{db: db, versions: versions, reasons: reasons, reasonCounts: map[string]int{}}
	// Only the requested layer is ordered. Cold/all expect most facts, so size
	// the order once; hot/warm are usually small and should not reserve the
	// whole ledger.
	orderCap := 64
	if layer == FocusCold || layer == focusLayerAll {
		orderCap = len(db.Facts)
	}
	p.order = make([]focusOrder, 0, orderCap)
	// Reason counts are a property of the recorded references, not of the page:
	// count them once from the same reason map the rows carry.
	for _, why := range reasons {
		for _, reason := range why {
			p.reasonCounts[reason]++
		}
	}
	// Only referenced ids are candidates for "missing", so seed the pending set
	// from the reasons map instead of building an id map over every fact: the
	// latter copied the whole ledger once per page (the measured bottleneck).
	pending := make(map[string]struct{}, len(reasons))
	for id := range reasons {
		pending[id] = struct{}{}
	}
	for i := range db.Facts {
		f := db.Facts[i]
		delete(pending, f.ID)
		temp := focusTemperature(reasons[f.ID])
		p.count(temp)
		if focusLayerMatches(layer, temp) {
			p.order = append(p.order, focusOrder{id: f.ID, temperature: temp, fact: i})
		}
	}
	// Referenced premises that are not in the snapshot are explicit rows, not
	// silence: the dispatch gate refuses such a direction, but a historical or
	// cross-branch view must be able to say "missing" and let the reader check.
	// The temperature follows the reason that referenced it: a missing hard
	// prerequisite is hot (the task depends on it), a missing provenance id is
	// warm (it is a recorded source, not a dependency).
	for id := range pending {
		why := reasons[id]
		temp := focusTemperature(why)
		p.count(temp)
		if !focusLayerMatches(layer, temp) {
			continue
		}
		row := FocusFact{
			ID:          id,
			Status:      "missing",
			Temperature: temp,
			Reasons:     append([]string{}, why...),
			Query:       "/api/facts/" + id,
		}
		p.order = append(p.order, focusOrder{id: id, temperature: temp, fact: -1, missing: &row})
	}
	rank := focusRank()
	sort.Slice(p.order, func(i, j int) bool {
		a, b := p.order[i], p.order[j]
		if rank[a.temperature] != rank[b.temperature] {
			return rank[a.temperature] < rank[b.temperature]
		}
		return a.id < b.id
	})
	return p, nil
}

func (p *focusPlan) count(temperature string) {
	switch temperature {
	case FocusHot:
		p.hot++
	case FocusWarm:
		p.warm++
	default:
		p.cold++
	}
}

func (p *focusPlan) materialize(item focusOrder) FocusFact {
	if item.missing != nil {
		return *item.missing
	}
	return focusRow(p.db.Facts[item.fact], p.reasons, p.versions)
}

// focusReasons seeds the recorded reasons for one task. It only reads recorded
// fields: the task's own premise/pin/reference lists, the provenance chain, and
// entity refs the operator/commander actually recorded. Nothing is inferred
// from titles or prose.
func focusReasons(db store.DB, intentID string) (map[string][]string, error) {
	var task *model.Intent
	for i := range db.Intents {
		if db.Intents[i].ID == intentID {
			task = &db.Intents[i]
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("意图不存在：%s", intentID)
	}
	reasons := map[string][]string{}
	addReason := func(id, reason string) {
		if id == "" || slices.Contains(reasons[id], reason) {
			return
		}
		reasons[id] = append(reasons[id], reason)
	}
	// Hot: the task's own recorded dependencies. Order matters for a stable
	// reasons list, so the groups are added in a fixed sequence.
	for _, id := range task.FromFactIDs {
		addReason(id, FocusReasonRequired)
	}
	for _, id := range task.PinnedFactIDs {
		addReason(id, FocusReasonPinned)
	}
	for _, id := range task.ContextFactIDs {
		addReason(id, FocusReasonReference)
	}
	// Warm: recorded provenance. TaskSources follows producers' prerequisites,
	// so a direct prerequisite appears here too; it stays hot because required
	// was added first.
	for _, id := range TaskSources(db, intentID) {
		addReason(id, FocusReasonProvenance)
	}
	// Warm: recorded entity identity. The seed set is only what the operator/
	// commander actually recorded (explicit interest entities and the entity
	// refs of required/pinned/referenced facts); it is deliberately not
	// expanded transitively, so a run of same-entity observations does not drag
	// in the whole ledger.
	entities := map[string]bool{}
	for _, ref := range task.InterestEntityRefs {
		if ref != "" {
			entities[ref] = true
		}
	}
	// The seed entity refs are only needed for the handful of ids the task
	// names, so scan the facts once with a small wanted set instead of copying
	// every fact into an id map.
	hotIDs := append(append(append([]string(nil), task.FromFactIDs...), task.PinnedFactIDs...), task.ContextFactIDs...)
	wanted := make(map[string]bool, len(hotIDs))
	for _, id := range hotIDs {
		wanted[id] = true
	}
	for _, f := range db.Facts {
		if f.EntityRef != "" && wanted[f.ID] {
			entities[f.EntityRef] = true
		}
	}
	for _, f := range db.Facts {
		if f.EntityRef != "" && entities[f.EntityRef] {
			addReason(f.ID, FocusReasonSameEntity)
		}
	}
	return reasons, nil
}

// focusRow materializes the summary layer for one fact. It is the only place a
// FocusFact is built from a ledger row, so the API page and the whole-projection
// consumer cannot disagree about a row.
func focusRow(f model.Fact, reasons map[string][]string, versions map[string]uint64) FocusFact {
	row := FocusFact{
		ID:        f.ID,
		Kind:      f.Kind,
		Title:     clip(f.Title, FocusTitleRunes),
		Summary:   clip(f.Summary, FocusSummaryRunes),
		Status:    f.Status,
		Reasons:   reasons[f.ID],
		SourceRun: f.SourceRunID,
		Query:     "/api/facts/" + f.ID,
	}
	if row.Reasons == nil {
		row.Reasons = []string{}
	}
	if f.ClaimVersion > 0 {
		row.ClaimVersion = f.ClaimVersion
	}
	if version, ok := versions[f.ID]; ok {
		// A version map that covers the snapshot never has zero for a live
		// row; a supplied zero would still be a real recorded revision, so it
		// is kept and not confused with "missing".
		v := version
		row.RecordVersion = &v
	}
	if f.ObservedAt != nil {
		at := *f.ObservedAt
		row.ObservedAt = &at
	}
	row.Temperature = focusTemperature(row.Reasons)
	return row
}

// focusTemperature is the projection rule: required/pinned/reference are hot,
// recorded provenance and same-recorded-entity are warm, everything else is
// cold. Cold is not "irrelevant": it is "not referenced by this task (yet)".
func focusTemperature(reasons []string) string {
	warm := false
	for _, reason := range reasons {
		switch reason {
		case FocusReasonRequired, FocusReasonPinned, FocusReasonReference:
			return FocusHot
		case FocusReasonProvenance, FocusReasonSameEntity:
			warm = true
		}
	}
	if warm {
		return FocusWarm
	}
	return FocusCold
}

func focusRank() map[string]int {
	return map[string]int{FocusHot: 0, FocusWarm: 1, FocusCold: 2}
}

// Focus returns one frozen page of the projection. Every page of one cursor
// chain is read from the same ReadVersion, and the record versions come from the
// same snapshot, so concurrent writes cannot move a page or relabel a version.
func Focus(s *store.Store, q FocusQuery) (FocusPage, error) {
	layer, err := focusLayer(q.Layer)
	if err != nil {
		return FocusPage{}, err
	}
	key := queryKey(struct {
		Type  string
		Query FocusQuery
	}{"focus", q})
	db, c, err := read(s, q.Cursor, key)
	if err != nil {
		return FocusPage{}, err
	}
	versions, err := s.FactVersionsAt(c.Version)
	if err != nil {
		return FocusPage{}, err
	}
	plan, err := newFocusPlan(db, q.Intent, layer, versions)
	if err != nil {
		return FocusPage{}, err
	}
	out := FocusPage{
		Intent:        q.Intent,
		Layer:         layer,
		Facts:         []FocusFact{},
		Hot:           plan.hot,
		Warm:          plan.warm,
		Cold:          plan.cold,
		IndexQuery:    FocusLayerQuery(q.Intent, "focus"),
		HotQuery:      FocusLayerQuery(q.Intent, FocusHot),
		WarmQuery:     FocusLayerQuery(q.Intent, FocusWarm),
		ColdQuery:     FocusLayerQuery(q.Intent, FocusCold),
		FactQuery:     "/api/facts?intent=" + q.Intent,
		Premises:      ".tw/context.json",
		PremisesQuery: FocusPremisesQuery(q.Intent),
		Experience:    "/api/experience?intent=" + q.Intent,
		ReasonCounts:  plan.reasonCounts,
		Contract:      FocusContractValue(),
	}
	rows := plan.order
	out.Total = len(rows)
	out.Version = c.Version
	if c.Offset > len(rows) {
		return out, fmt.Errorf("invalid_cursor: offset outside snapshot")
	}
	used := 0
	for c.Offset < len(rows) && len(out.Facts) < pageLimit(q.Limit) {
		row := plan.materialize(rows[c.Offset])
		raw, err := json.Marshal(row)
		if err != nil {
			return out, err
		}
		// Always take at least one row: a page that refuses to advance would
		// loop forever on a cursor pointing at an oversized row.
		if len(out.Facts) > 0 && used+len(raw) > FocusPageBytes {
			break
		}
		used += len(raw)
		out.Facts = append(out.Facts, row)
		c.Offset++
	}
	out.HasMore = c.Offset < len(rows)
	if out.HasMore {
		out.NextCursor = encode(c)
	}
	return out, nil
}

// focusLayer normalises the requested layer. "focus" is the default bounded
// index (hot + warm); cold is the continuation for everything not referenced.
func focusLayer(raw string) (string, error) {
	switch raw {
	case "", "focus":
		return "focus", nil
	case FocusHot, FocusWarm, FocusCold:
		return raw, nil
	default:
		return "", fmt.Errorf("invalid_layer: 图层只能是 focus/hot/warm/cold")
	}
}

func focusLayerMatches(layer, temperature string) bool {
	if layer == focusLayerAll {
		return true
	}
	if layer == "focus" {
		return temperature != FocusCold
	}
	return layer == temperature
}
