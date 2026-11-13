package fleet

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/decider"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestBuildAggregatesProjectsAndNodes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}

	live, err := st.CreateIntent(model.Intent{Title: "扫 8080", Description: "确认端口"})
	if err != nil {
		t.Fatal(err)
	}
	liveRun, err := st.AddRun(model.Run{IntentID: live.ID, Node: "lab", Events: 4, Tokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(live.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.Node = "requested" // the run records where it actually landed
		i.RunID = liveRun.ID
	}); err != nil {
		t.Fatal(err)
	}

	done, err := st.CreateIntent(model.Intent{Title: "收尾", Description: "整理"})
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-time.Hour)
	doneRun, err := st.AddRun(model.Run{IntentID: done.ID, Node: "local", Tokens: 50, Events: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(doneRun.ID, func(r *model.Run) {
		r.Status = "done"
		r.StartedAt = ended.Add(-2 * time.Minute)
		r.EndedAt = &ended
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	checked := now.Add(-time.Second)
	snap := Build(now, []Source{{
		ID: "p1", Name: "项目一", Scope: "10.0.0.0/24", Store: st,
		Decider: decider.Status{Enabled: true, Rounds: 2, Tokens: 900},
	}}, []executor.NodeInfo{
		{ID: "lab", Transport: "aop", Endpoint: "http://host:18766"},
		{ID: "local", Transport: "aop", Endpoint: "http://127.0.0.1:18766"},
	}, map[string]Health{
		"lab": {Online: true, Agents: 2, CheckedAt: &checked},
	})

	if len(snap.Projects) != 1 || len(snap.Nodes) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	p := snap.Projects[0]
	if p.Intents.Running != 1 || p.Intents.Total != 2 || p.Intents.Done != 1 {
		t.Fatalf("intents = %+v", p.Intents)
	}
	if len(p.Running) != 1 {
		t.Fatalf("running = %+v", p.Running)
	}
	liveTask := p.Running[0]
	if liveTask.ID != live.ID || liveTask.Node != "lab" || liveTask.RunID != liveRun.ID {
		t.Fatalf("running task = %+v", liveTask)
	}
	if liveTask.Pulse == nil || liveTask.Pulse.Events != 4 {
		t.Fatalf("pulse = %+v", liveTask.Pulse)
	}
	if len(p.RecentRuns) != 2 || p.RecentRuns[0].ID != liveRun.ID {
		t.Fatalf("recent runs = %+v", p.RecentRuns)
	}
	if p.RecentRuns[1].Title != "收尾" || p.RecentRuns[1].EndedAt == nil {
		t.Fatalf("finished run = %+v", p.RecentRuns[1])
	}
	if p.Tokens != 150 {
		t.Fatalf("tokens = %d", p.Tokens)
	}
	if p.Decider.Rounds != 2 || !p.Decider.Enabled {
		t.Fatalf("decider = %+v", p.Decider)
	}
	if snap.Nodes[0].Running != 1 || snap.Nodes[1].Running != 0 {
		t.Fatalf("node running = %+v", snap.Nodes)
	}
	if !snap.Nodes[0].Health.Online || snap.Nodes[0].Health.Agents != 2 || snap.Nodes[0].Health.CheckedAt == nil {
		t.Fatalf("node health = %+v", snap.Nodes[0].Health)
	}
	if len(p.TokenSeries) == 0 {
		t.Fatal("token series is empty")
	}
	seriesTokens := 0
	for _, point := range p.TokenSeries {
		seriesTokens += point.Tokens
		if point.Node == "" {
			t.Fatalf("token point lost its node: %+v", point)
		}
	}
	if seriesTokens != 150 {
		t.Fatalf("series tokens = %d (%+v)", seriesTokens, p.TokenSeries)
	}

	// The page consumes this as JSON; a nil slice or a zero time would be a
	// frontend foot-gun, so encode it here once.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["projects"] == nil || decoded["nodes"] == nil {
		t.Fatalf("empty collections should encode as arrays: %s", raw)
	}
}
