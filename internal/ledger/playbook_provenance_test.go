package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestDeclaredExperiencePreservesExplicitSourceFactsAndRun(t *testing.T){
	dir:=t.TempDir();st,err:=store.Open(filepath.Join(dir,"ledger.sqlite"));if err!=nil{t.Fatal(err)};defer st.Close()
	f,_:=st.AddFact(model.Fact{Title:"observed source",EntityRef:"target-A"})
	run,_:=st.AddRun(model.Run{IntentID:"in_task"})
	work:=filepath.Join(dir,"cases","in_task");if err=os.MkdirAll(filepath.Join(work,".tw"),0700);err!=nil{t.Fatal(err)}
	raw,_:=json.Marshal(map[string]any{"kind":"playbook","title":"declared experience","summary":"applicable here","doc":"observed method","from_fact_ids":[]string{f.ID},"required_tags":[]string{"context:one"}})
	if err=os.WriteFile(filepath.Join(work,".tw","facts.jsonl"),append(raw,'\n'),0600);err!=nil{t.Fatal(err)}
	g:=Ingestor{Store:st};g.OnFinish(run.ID,"in_task",filepath.Join(dir,"cases"),work,time.Now().Add(-time.Minute))
	books:=st.Playbooks();if len(books)!=1||books[0].SourceRunID!=run.ID||len(books[0].FromFactIDs)!=1||books[0].FromFactVersions[f.ID]!=1{t.Fatalf("provenance lost: %+v",books)}
}
