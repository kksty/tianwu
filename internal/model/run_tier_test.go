package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Run.Tier is frozen at dispatch next to the profile it belongs to, so the
// ledger keeps saying which tier a run actually spent after the profile is
// retiered or deleted. Rows written before the field existed have no tier:
// the key must be absent from the JSON instead of an empty string the console
// and the board would have to special-case.
func TestRunTierIsPresentOnlyWhenKnown(t *testing.T) {
	old, err := json.Marshal(Run{ID: "r_old", IntentID: "in_1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(old), `"tier"`) {
		t.Fatalf("a run with no tier serialized one: %s", old)
	}

	fresh, err := json.Marshal(Run{ID: "r_new", IntentID: "in_1", ResolvedProfile: "cheap", Tier: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	var back Run
	if err := json.Unmarshal(fresh, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tier != "worker" || back.ResolvedProfile != "cheap" {
		t.Fatalf("tier did not survive the ledger round trip: %+v", back)
	}
}
