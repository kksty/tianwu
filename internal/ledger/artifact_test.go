package ledger

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/aop/tool"
	"github.com/kksty/tianwu/internal/artifact"
	"github.com/kksty/tianwu/internal/store"
)

func TestNativeResultsOnlyBecomeEvidenceWhenExplicitlyCited(t *testing.T) {
	root := t.TempDir()
	st, _ := store.Open(filepath.Join(root, "ledger.json"))
	cases := filepath.Join(root, "cases")
	work := filepath.Join(cases, "in_1")
	g := &Ingestor{Store: st, MaxEvidence: 5}
	r, err := artifact.Save(cases, "r_1", "in_1", "event", &tool.Artifact{Tool: "neutron", ResultId: "result", MediaType: "application/json", Data: []byte(`{"match":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	g.OnFinish("r_1", "in_1", cases, work, time.Now())
	if len(st.Snapshot().Facts) != 0 || len(st.Evidence()) != 0 {
		t.Fatal("uncited scanner output promoted")
	}
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), `{"kind":"note","title":"已核对观察","evidence":[{"artifact":"`+r.Ref+`"}]}`+"\n")
	g.OnFinish("r_2", "in_1", cases, work, time.Now())
	if len(st.Evidence()) != 1 || st.Evidence()[0].From != "artifact:"+r.Ref {
		t.Fatalf("citation provenance: %+v", st.Evidence())
	}
}

func TestPrivateLoginInputsCannotBeDeclaredAsEvidence(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "in_1")
	for _, name := range []string{".tw/access/ac_test.json", "../.access/ac_test/material.json", "../.native/data"} {
		if _, state := resolveEvidence(root, work, name); state != evidenceInvalid {
			t.Fatalf("private input accepted as evidence: %s", name)
		}
	}
}
