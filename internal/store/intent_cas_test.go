package store

import (
	"github.com/kksty/tianwu/internal/model"
	"testing"
)

func TestStalePreparationCannotResetClaimedIntentOrWriteRevision(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "direction"})
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := s.UpdateIntentIfCurrent(in.ID, in.Status, in.RunID, func(i *model.Intent) { i.Hold = true })
	if err != nil || !ok {
		t.Fatalf("prepare: %v %v", ok, err)
	}
	if _, ok, err := s.ClaimIntent(in.ID, "local"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	rev := s.Revision()
	_, ok, err = s.UpdateIntentIfCurrent(in.ID, in.Status, in.RunID, func(i *model.Intent) { t.Fatal("stale mutation executed") })
	if err != nil || ok || s.Revision() != rev {
		t.Fatalf("stale prepare: %v %v", ok, err)
	}
	current, _ := s.GetIntent(in.ID)
	if current.Status != model.StatusClaimed {
		t.Fatal("stale preparation overwrote claim")
	}
}
