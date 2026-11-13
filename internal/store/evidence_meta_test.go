package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// AddEvidence makes the impossible metadata states unrecordable: a preserved
// object's address IS its digest, so a row pointing at one must carry that same
// digest. Legacy rows (plain working-path URI, no digest) stay accepted — their
// limitation is explicit, not repaired here (F2/D150).
func TestAddEvidenceRejectsIncoherentPreservationMetadata(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	objectURI := ".evidence/objects/" + digestA

	bad := []struct {
		name string
		ev   model.Evidence
	}{
		{"preserved URI without a digest", model.Evidence{URI: objectURI}},
		{"digest disagrees with the object name", model.Evidence{URI: objectURI, Digest: digestB}},
		{"malformed digest", model.Evidence{URI: "in_1/a.txt", Digest: "not-a-sha256"}},
		{"negative size", model.Evidence{URI: "in_1/a.txt", Size: -1}},
	}
	for _, c := range bad {
		if _, err := s.AddEvidence(c.ev); err == nil {
			t.Fatalf("%s: AddEvidence accepted an incoherent row", c.name)
		}
	}

	// A coherent preserved row is accepted and its digest normalized.
	got, err := s.AddEvidence(model.Evidence{URI: objectURI, Digest: strings.ToUpper(digestA)})
	if err != nil {
		t.Fatalf("coherent preserved row refused: %v", err)
	}
	if got.Digest != digestA {
		t.Fatalf("digest = %q, want the normalized %q", got.Digest, digestA)
	}

	// A legacy row keeps working; no digest was ever recorded for it.
	if _, err := s.AddEvidence(model.Evidence{URI: "in_1/legacy.txt", Name: "legacy.txt"}); err != nil {
		t.Fatalf("legacy row refused: %v", err)
	}
}
