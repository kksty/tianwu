package main

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

func TestHubCapacityCountsProfilesAcrossProjects(t *testing.T) {
	h := &hub{kits: map[string]*kit{}}
	for _, id := range []string{"a", "b"} {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "ledger.json"))
		if err != nil {
			t.Fatal(err)
		}
		srv := api.New(st, nil, dir, filepath.Join(dir, "cases"))
		h.kits[id] = &kit{srv: srv}
		for _, r := range []model.Run{
			{IntentID: "worker", ResolvedProfile: "shared"},
			{Purpose: "commander", ResolvedProfile: "shared"},
			{IntentID: "other", ResolvedProfile: "other"},
		} {
			if _, err := st.AddRun(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := h.globalRunning(); n != 4 {
		t.Fatalf("workers=%d", n)
	}
	if n := h.globalProfileRunning("shared"); n != 2 {
		t.Fatalf("shared profile=%d", n)
	}
	if n := h.globalProfileRunning("absent"); n != 0 {
		t.Fatalf("absent profile=%d", n)
	}
}
