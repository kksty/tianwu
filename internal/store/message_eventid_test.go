package store

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func TestMessageEventIDIsStableAndDeduped(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := s.AddMessage(testMessage("pt_a", "pt_b", "k1", "hello"))
	if err != nil || !created {
		t.Fatalf("add: %v created=%v", err, created)
	}
	if first.EventID == "" || first.EventID != first.ID {
		t.Fatalf("event id = %q, message id = %q", first.EventID, first.ID)
	}

	// A retry reusing the stable event id returns the original row even if the
	// caller changed the text/transport (it is the same application event).
	retry := testMessage("pt_a", "pt_b", "", "hello again")
	retry.EventID = first.EventID
	got, created, err := s.AddMessage(retry)
	if err != nil || created || got.ID != first.ID {
		t.Fatalf("dedupe = %+v created=%v err=%v", got, created, err)
	}

	// A different sender with the same event id is a different event.
	other := testMessage("pt_c", "pt_b", "", "other")
	other.EventID = first.EventID
	if _, created, err := s.AddMessage(other); err != nil || !created {
		t.Fatalf("cross-sender dedupe: created=%v err=%v", created, err)
	}
}

func TestBeginMessageDeliveryPinsLegacyEventID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	msg, _, err := s.AddMessage(testMessage("pt_a", "pt_b", "k", "old row"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a row written before EventID existed.
	if _, err := s.UpdateMessage(msg.ID, func(m *model.Message) { m.EventID = "" }); err != nil {
		t.Fatal(err)
	}
	claimed, acquired, err := s.BeginMessageDelivery(msg.ID)
	if err != nil || !acquired {
		t.Fatalf("claim: %v acquired=%v", err, acquired)
	}
	if claimed.EventID != msg.ID {
		t.Fatalf("event id = %q, want message id %q", claimed.EventID, msg.ID)
	}
}

func TestResultMessageCarriesChainHop(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	msg := testMessage("pt_child", "pt_parent", "k-result", "done")
	msg.Kind = model.MessageResult
	msg.FromRunID = "r_child"
	got, _, err := s.AddMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chain) != 1 || got.Chain[0] != "r_child" {
		t.Fatalf("chain = %v", got.Chain)
	}
}
