package settings

import "testing"

func TestSuccessCannotReleaseAnInflightRotation(t *testing.T) {
	r := credentialRegistry{}
	first := r.begin("p", 0, 3)
	if !first.Rotate {
		t.Fatal("missing first owner")
	}
	if r.succeed("p", 0) {
		t.Fatal("success released another owner's rotation")
	}
	if second := r.begin("p", 0, 3); second.Rotate {
		t.Fatal("two rotation owners")
	}
	if _, ok := r.complete("p", first.Claim); !ok {
		t.Fatal("original owner lost claim")
	}
	if r.succeed("p", 0) {
		t.Fatal("old generation reset current episode")
	}
}

func TestStaleRotationCannotCompleteOrAbortItsReplacement(t *testing.T) {
	r := credentialRegistry{}
	first := r.begin("p", 0, 3)
	if !r.abort("p", first.Claim) {
		t.Fatal("owner could not abort")
	}
	second := r.begin("p", 0, 3)
	if !second.Rotate || first.Claim == second.Claim {
		t.Fatal("replacement must have a new claim")
	}
	if _, ok := r.complete("p", first.Claim); ok {
		t.Fatal("old owner completed replacement")
	}
	if r.abort("p", first.Claim) {
		t.Fatal("old owner aborted replacement")
	}
	if generation, ok := r.complete("p", second.Claim); !ok || generation != 1 {
		t.Fatal("replacement could not complete")
	}
}
