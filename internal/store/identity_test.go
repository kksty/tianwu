package store

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestLedgerIdentityHas128BitRandomPayload(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID("f")
		raw, err := hex.DecodeString(strings.TrimPrefix(id, "f_"))
		if err != nil || len(raw) != 16 || seen[id] {
			t.Fatalf("weak or duplicate identity: %s", id)
		}
		seen[id] = true
	}
}
