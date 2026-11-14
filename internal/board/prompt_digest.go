package board

// The commander round prompt carries the board's summary layer, and every list
// in that layer is a bounded top-N: open intents, recent facts, done titles,
// the frontier, open goals. Counts are ledger-wide but the lists are not, so a
// model reading only the prompt can mistake "the 20 rows I was shown" for "all
// the work the ledger has" (docs/35 §5 board/state row, handoff §6). This file
// adds an explicit digest next to those lists: shown/total per list plus the
// complete, paginated enumeration entries.
//
// It is prompt-only data: Build leaves PromptDigest nil, so GET /api/board and
// the console keep their exact existing shape. Only ForPrompt sets it, and it
// says nothing the ledger cannot prove.

// PromptDigest is the bounded-list label attached to the commander prompt's
// copy of the board.
type PromptDigest struct {
	// Note states the contract in one sentence: the lists are a digest, the
	// counts are the ledger, and the queries enumerate.
	Note string `json:"note"`
	// Bounded lists the top-N list names with how many rows are shown vs the
	// ledger-wide total. A reader must not treat shown as the complete set.
	Bounded []PromptBound `json:"bounded_lists"`
	// Complete enumeration entries, cursor-paginated where the API supports it.
	IntentsQuery string `json:"intents_query"`
	FactsQuery   string `json:"facts_query"`
	ChangesQuery string `json:"changes_query"`
	GraphQuery   string `json:"graph_query"`
	FocusQuery   string `json:"focus_query"`
}

// PromptBound is one bounded list represented in the prompt.
type PromptBound struct {
	// List is the JSON field name of the list in the same board payload.
	List  string `json:"list"`
	Shown int    `json:"shown"`
	Total int    `json:"total"`
	Cut   bool   `json:"cut"`
}

// promptDigest builds the digest from the already-projected view. It never
// invents a total: the counts come from the same ledger snapshot Build used, so
// a digest total and the board count cannot disagree.
func promptDigest(v View) *PromptDigest {
	intentsQuery := "/api/intents?limit=200"
	factsQuery := "/api/facts?limit=200"
	d := &PromptDigest{
		Note: "board 里的每个列表都是有界摘要（最多 20 行）；total 是账本计数，shown 是本轮实际列出的行数，cut=true 表示还有未列出的行。" +
			"完整枚举用下面的 query 分页读取（intents/facts 支持 cursor）；未列出不表示不存在，也不改变任何事实的有效性。" +
			"ports/coverage 是由事实派生的聚合视图，不是按端口/主机的判断；底层事实用 facts_query 枚举。",
		IntentsQuery: intentsQuery,
		FactsQuery:   factsQuery,
		ChangesQuery: "/api/changes?limit=100",
		GraphQuery:   "/api/graph?limit=200",
		FocusQuery:   "/api/focus?intent=in_x（旧事实加 &layer=cold）",
	}
	pendingOpen := v.Intents.Pending - v.Intents.Held
	if pendingOpen < 0 {
		pendingOpen = 0
	}
	openTotal := pendingOpen + v.Intents.Claimed
	// The frontier is not the open set: Build includes held pending directions
	// there too (it shows why a stopped direction is stopped), so it is counted
	// from the raw status counts, not from openTotal.
	frontierTotal := v.Intents.Pending + v.Intents.Claimed + v.Intents.Failed + v.Intents.ReviewFailed
	openGoals := v.Goals.Total - v.Goals.Met
	if openGoals < 0 {
		openGoals = 0
	}
	add := func(list string, shown, total int) {
		d.Bounded = append(d.Bounded, PromptBound{List: list, Shown: shown, Total: total, Cut: shown < total})
	}
	add("open_intents", len(v.Open), openTotal)
	add("held_intents", len(v.Held), v.Intents.Held)
	add("waiting_intents", len(v.Waiting), v.Intents.Waiting)
	add("reviewing_intents", len(v.Reviewing), v.Intents.Reviewing)
	add("review_failed_intents", len(v.ReviewFailed), v.Intents.ReviewFailed)
	add("running_intents", len(v.Running), v.Intents.Running)
	add("recent_facts", len(v.RecentFacts), v.Facts.Total)
	add("done_intents", len(v.Done), v.Intents.Done)
	add("frontier", len(v.Frontier), frontierTotal)
	add("goals.open", len(v.Goals.Open), openGoals)
	return d
}
