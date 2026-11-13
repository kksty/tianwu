package board

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// A report must not turn stale because writing it added a run. Run bookkeeping
// belongs to the decider's wake-up digest, not to "is this report still true".
func TestContentDigestIgnoresRunsButNotFacts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIntent(model.Intent{Title: "查一遍"}); err != nil {
		t.Fatal(err)
	}

	content := ContentDigest(Build(st, 20))
	decider := Digest(Build(st, 20))

	if _, err := st.AddRun(model.Run{Node: "local"}); err != nil {
		t.Fatal(err)
	}
	if got := ContentDigest(Build(st, 20)); got != content {
		t.Fatalf("a run changed the report digest: %s → %s", content, got)
	}
	if got := Digest(Build(st, 20)); got == decider {
		t.Fatalf("a run did not change the decider digest")
	}

	if _, err := st.AddFact(model.Fact{Kind: "vuln", Title: "新结论"}); err != nil {
		t.Fatal(err)
	}
	if got := ContentDigest(Build(st, 20)); got == content {
		t.Fatalf("a fact did not change the report digest")
	}
}

// A direction whose premise was rejected is dead work, not queued work: the
// frontier says so before the decider proposes the same thing again (D52).
func TestFrontierMarksRejectedAndMissingPremises(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	good, _ := st.AddFact(model.Fact{Kind: "service", Title: "10.0.0.5:8080"})
	bad, _ := st.AddFact(model.Fact{Kind: "vuln", Title: "被判不成立"})
	if _, err := st.ReviewFact(model.Review{FactID: bad.ID, Verdict: "reject", Notes: "复现不了"}); err != nil {
		t.Fatal(err)
	}

	ready, _ := st.CreateIntent(model.Intent{Title: "前提成立", FromFactIDs: []string{good.ID}})
	blocked, _ := st.CreateIntent(model.Intent{Title: "前提没了", FromFactIDs: []string{bad.ID, "f_gone"}})

	byID := map[string]FrontierItem{}
	for _, f := range Build(st, 20).Frontier {
		byID[f.ID] = f
	}
	if got := byID[ready.ID]; !got.Ready || len(got.Blocked) != 0 {
		t.Fatalf("ready direction = %+v", got)
	}
	if got := byID[blocked.ID]; got.Ready || len(got.Blocked) != 2 {
		t.Fatalf("blocked direction = %+v", got)
	}
}

// A verifier verdict is content, not bookkeeping: rejecting a fact must change
// the digest, otherwise the "review" wake is skipped as "nothing changed" and
// the decider never learns its premise died. The report is built from the same
// digest, so the old report also has to go stale on a reject.
func TestReviewVerdictChangesTheDigest(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, _ := st.AddFact(model.Fact{Kind: "vuln", Title: "待判定"})
	before := Digest(Build(st, 20))
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "reject", Notes: "复现不了"}); err != nil {
		t.Fatal(err)
	}
	if after := Digest(Build(st, 20)); after == before {
		t.Fatalf("a reject did not change the digest: %s", before)
	}
}

// The digest used to be counts plus the bounded id lists, so an edit that moves
// no count and no id was skipped as "nothing changed": a fact's doc or summary
// rewritten, a ref added, or any edit to a row that had already fallen out of
// the top-N. The commander was awake-able only when the board changed shape, not
// when it changed meaning (D79). A doc edit below changes facts.total,
// facts.with_evidence, facts.with_refs and facts.verified by zero, so only the
// ledger's write counter can make this test pass.
func TestDigestCoversEditsNoCountCanSee(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oldest model.Fact
	for i := 0; i < 25; i++ {
		f, err := st.AddFact(model.Fact{Kind: "note", Title: fmt.Sprintf("观察 %02d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = f
		}
	}
	before := Digest(Build(st, 20))
	if _, err := st.UpdateFact(oldest.ID, func(f *model.Fact) {
		f.Doc = "重放命令：curl -s http://10.0.0.5:8080/healthz"
	}); err != nil {
		t.Fatal(err)
	}
	after := Digest(Build(st, 20))
	if after == before {
		t.Fatalf("a doc edit outside the top-N did not change the digest: %s", before)
	}
}

// A live run's pulse is "still alive", not news: NoteEvent and ProgressRun
// deliberately do not flush, so the write counter and the digest do not move.
// Waking on them would spend one commander round per executor event.
func TestDigestIgnoresLivePulse(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "跑着"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	before := Digest(Build(st, 20))
	st.NoteEvent(run.ID, time.Now(), "tool_call", "bash", "nmap -p- 10.0.0.5")
	st.ProgressRun(run.ID, 12000)
	if after := Digest(Build(st, 20)); after != before {
		t.Fatalf("a live pulse woke the decider: %s → %s", before, after)
	}
}

// A canceled direction is pending+Hold; the auto-dispatcher will never take it
// and only an explicit retry clears Hold. The frontier must say so, or the
// decider waits for a direction it has itself stopped.
func TestHeldDirectionIsNotReady(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := st.CreateIntent(model.Intent{Title: "被取消的方向"})
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Hold = true }); err != nil {
		t.Fatal(err)
	}
	for _, f := range Build(st, 20).Frontier {
		if f.ID == in.ID && f.Ready {
			t.Fatalf("held direction advertised as ready: %+v", f)
		}
	}
}

// A failed direction is not queued work: auto-dispatch skips it and the hard
// floor must not count it. A held (canceled) direction is also not dispatchable,
// but it stays visible so the commander can retry it.
func TestFailedAndHeldAreNotOpen(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	failed, _ := st.CreateIntent(model.Intent{Title: "失败的方向"})
	if _, err := st.UpdateIntent(failed.ID, func(i *model.Intent) { i.Status = model.StatusFailed }); err != nil {
		t.Fatal(err)
	}
	held, _ := st.CreateIntent(model.Intent{Title: "取消的方向"})
	if _, err := st.UpdateIntent(held.ID, func(i *model.Intent) { i.Hold = true }); err != nil {
		t.Fatal(err)
	}
	pending, _ := st.CreateIntent(model.Intent{Title: "可派的方向"})

	v := Build(st, 20)
	if len(v.Open) != 1 || v.Open[0].ID != pending.ID {
		t.Fatalf("open = %+v", v.Open)
	}
	if len(v.Held) != 1 || v.Held[0].ID != held.ID {
		t.Fatalf("held = %+v", v.Held)
	}
	if v.Intents.Held != 1 || v.Intents.Failed != 1 {
		t.Fatalf("counts = %+v", v.Intents)
	}
	for _, f := range v.Frontier {
		if f.ID == failed.ID && (f.Ready || f.Status != model.StatusFailed) {
			t.Fatalf("failed frontier must be not-ready: %+v", f)
		}
	}
}

// Coverage is what the ledger has actually seen, with a text-derived "someone
// is working on it" flag — not a scope parser and not a target assignment.
func TestCoverageShowsKnownHosts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"10.0.0.5:80", "10.0.0.6:22", "10.0.0.5:443"} {
		if _, err := st.AddFact(model.Fact{Kind: "service", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddFact(model.Fact{Kind: "host", Title: "10.0.0.7"}); err != nil {
		t.Fatal(err)
	}
	run, _ := st.CreateIntent(model.Intent{Title: "打 10.0.0.5", Description: "80/443"})
	_, _ = st.UpdateIntent(run.ID, func(i *model.Intent) { i.Status = model.StatusRunning })
	open, _ := st.CreateIntent(model.Intent{Title: "打 10.0.0.6", Description: "22"})
	_, _ = st.UpdateIntent(open.ID, func(i *model.Intent) { i.Status = model.StatusPending })

	v := Build(st, 20)
	by := map[string]CoverageItem{}
	for _, c := range v.Coverage {
		by[c.Host] = c
	}
	if got := by["10.0.0.5"]; got.Services != 2 || !got.Running {
		t.Fatalf("10.0.0.5 coverage = %+v", got)
	}
	if got := by["10.0.0.6"]; !got.Open || got.Running {
		t.Fatalf("10.0.0.6 coverage = %+v", got)
	}
	if got := by["10.0.0.7"]; got.Open || got.Running || got.Services != 0 {
		t.Fatalf("10.0.0.7 coverage = %+v", got)
	}
	if len(v.Coverage) == 0 || v.Coverage[0].Host != "10.0.0.7" {
		t.Fatalf("uncovered host should sort first: %+v", v.Coverage)
	}
}

// The port aggregate is an observation, not a family judgment: it must group
// the service facts the ledger holds and nothing more (D54).
func TestPortsGroupHostsByPort(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"10.0.0.5:8080", "10.0.0.9:8080", "10.0.0.5:22", "http://10.0.0.7:8080/login"} {
		if _, err := st.AddFact(model.Fact{Kind: "service", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddFact(model.Fact{Kind: "vuln", Title: "10.0.0.5:8080 未授权"}); err != nil {
		t.Fatal(err)
	}

	ports := Build(st, 20).Ports
	var p8080, p22 *PortGroup
	for i := range ports {
		switch ports[i].Port {
		case "8080":
			p8080 = &ports[i]
		case "22":
			p22 = &ports[i]
		}
	}
	if p8080 == nil || len(p8080.Hosts) != 3 || len(p8080.Members) != 3 {
		t.Fatalf("8080 group = %+v (all %+v)", p8080, ports)
	}
	if p22 == nil || len(p22.Hosts) != 1 {
		t.Fatalf("single-host port must still be visible: %+v", ports)
	}
}

// A goal the decider may complete must arrive with its id and quantified
// target, not just a sentence (D54).
func TestGoalsOpenCarryIDAndTarget(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "拿到管理员权限", QuantifiedTarget: "至少一条 verified 的 vault fact"})
	if err != nil {
		t.Fatal(err)
	}
	open := Build(st, 20).Goals.Open
	if len(open) != 1 || open[0].ID != g.ID || open[0].Quantified != "至少一条 verified 的 vault fact" {
		t.Fatalf("goals open = %+v", open)
	}
}

// The commander must see which model tier each direction is on without opening
// the run API: a running direction reports the binding frozen on its run, and a
// direction that has not started reports the profile it asked for (its tier is
// not in the ledger until dispatch freezes it).
func TestItemsCarryProfileAndTier(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	running, err := st.CreateIntent(model.Intent{Title: "有同伴的攻坚", Description: "d", Profile: "strong-1"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: running.ID, Node: "local", ResolvedProfile: "strong-1", Tier: "strong"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(running.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIntent(model.Intent{Title: "还没派", Description: "d", Profile: "cheap-1"}); err != nil {
		t.Fatal(err)
	}

	v := Build(st, 20)
	if len(v.Running) != 1 || v.Running[0].Profile != "strong-1" || v.Running[0].Tier != "strong" {
		t.Fatalf("running item = %+v", v.Running)
	}
	if len(v.Open) != 1 || v.Open[0].Profile != "cheap-1" || v.Open[0].Tier != "" {
		t.Fatalf("queued item = %+v", v.Open)
	}
}

// A running intent carries a live pulse; nothing else does. Without it the
// decider cannot tell a thinking run from a dead one (D57).
func TestBuildAttachesRunPulse(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "查 8080", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	st.NoteEvent(run.ID, time.Now().Add(-7*time.Minute), "", "", "")

	v := Build(st, 20)
	if len(v.Running) != 1 || v.Running[0].Pulse == nil {
		t.Fatalf("running = %+v", v.Running)
	}
	if p := v.Running[0].Pulse; p.Run != run.ID || p.IdleSeconds < 6*60 || !v.HasIdleRun(5*time.Minute) {
		t.Fatalf("pulse = %+v", p)
	}
}

// A run's pulse carries the last meaningful event, so a round can tell
// "exploiting" from "stuck in a retry loop" without opening the journal. Usage
// and delta events are liveness, not content: they must not overwrite the
// glance.
func TestPulseCarriesLastMeaningfulEvent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "查 8080", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("nmap -sV ", 60)
	st.NoteEvent(run.ID, time.Now(), "tool_call", "bash", long)
	st.NoteEvent(run.ID, time.Now(), "usage", "", `{"tokens":123}`)

	v := Build(st, 20)
	if len(v.Running) != 1 || v.Running[0].Pulse == nil {
		t.Fatalf("running = %+v", v.Running)
	}
	p := v.Running[0].Pulse
	if p.LastKind != "tool_call" || p.LastTool != "bash" {
		t.Fatalf("glance = %q/%q, want tool_call/bash", p.LastKind, p.LastTool)
	}
	if n := len([]rune(p.LastText)); n > 161 {
		t.Fatalf("last_text = %d runes, want clipped", n)
	}
	if p.Events != 2 {
		t.Fatalf("events = %d, want 2 (usage still counts)", p.Events)
	}
}

// A verdict changes what a report should say, so it must move the report
// digest even though no count of facts changed (D27/D52).
func TestContentDigestSeesReviewVerdicts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "待复核"})
	if err != nil {
		t.Fatal(err)
	}
	before := ContentDigest(Build(st, 20))
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", Notes: "复现成功"}); err != nil {
		t.Fatal(err)
	}
	if got := ContentDigest(Build(st, 20)); got == before {
		t.Fatal("a review did not change the report digest: a stale candidate can pass as current")
	}
}

// A fact that cited something the ledger cannot open must not look like a fact
// with no citation at all: the decider's next move differs (D44).
func TestBuildSeparatesRefsFromEvidence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{Kind: "vuln", Title: "有文件", EvidenceIDs: []string{"e_1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{Kind: "vuln", Title: "只有出处", Refs: []model.Ref{
		{Scheme: model.RefURL, Locator: "https://target/admin"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{Kind: "note", Title: "什么都没有"}); err != nil {
		t.Fatal(err)
	}

	v := Build(st, 20)
	if v.Facts.Total != 3 || v.Facts.WithEvidence != 1 || v.Facts.WithRefs != 1 {
		t.Fatalf("stats = %+v", v.Facts)
	}
	byTitle := map[string]Item{}
	for _, it := range v.RecentFacts {
		byTitle[it.Title] = it
	}
	if got := byTitle["只有出处"]; got.Evidence != 0 || got.Refs != 1 {
		t.Fatalf("cited fact = %+v", got)
	}
	if got := byTitle["有文件"]; got.Evidence != 1 || got.Refs != 0 {
		t.Fatalf("backed fact = %+v", got)
	}
	if got := byTitle["什么都没有"]; got.Evidence != 0 || got.Refs != 0 {
		t.Fatalf("bare fact = %+v", got)
	}
}

// Playbooks are knowledge, not findings: the board must not count them as facts
// or let their presence change the fact digest (D70).
func TestBoardCountsPlaybooksSeparately(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFact(model.Fact{Kind: "vuln", Title: "一个结论"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "确认的打法", When: "x", Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "候选", When: "y", Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "退役", When: "z", Origin: model.PlaybookFromExecutor, Status: model.PlaybookRetired}); err != nil {
		t.Fatal(err)
	}

	v := Build(st, 20)
	if v.Facts.Total != 1 {
		t.Fatalf("facts.total = %d, want 1", v.Facts.Total)
	}
	if v.Playbooks.Total != 3 || v.Playbooks.Confirmed != 1 || v.Playbooks.Candidates != 1 || v.Playbooks.Retired != 1 {
		t.Fatalf("playbook stats = %+v", v.Playbooks)
	}
	before := ContentDigest(v)
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "又一条候选", When: "w", Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate, FromFactIDs: []string{"f_1"}}); err != nil {
		t.Fatal(err)
	}
	if after := ContentDigest(Build(st, 20)); after != before {
		t.Fatalf("a playbook changed the report digest: %s → %s", before, after)
	}
}
