package board

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Redispatch is the re-send metric: only starts beyond a first attempt count,
// so a fresh direction and a never-retried failed one both stay at zero.
func TestRedispatchCountsExtraAttempts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := st.CreateIntent(model.Intent{Title: "首轮"})
	retried, _ := st.CreateIntent(model.Intent{Title: "重发过两次"})
	if _, err := st.UpdateIntent(first.ID, func(i *model.Intent) { i.Attempts = 1 }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(retried.ID, func(i *model.Intent) { i.Attempts = 3 }); err != nil {
		t.Fatal(err)
	}
	v := Build(st, 20)
	if v.Redispatch.Total != 2 || v.Redispatch.Directions != 1 {
		t.Fatalf("redispatch = %+v, want total=2 directions=1", v.Redispatch)
	}
}
