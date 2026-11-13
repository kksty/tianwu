package artifact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/aop/tool"
)

func TestNativeResultIdentityAndCitationPreserveOriginalBytes(t *testing.T) {
	root := t.TempDir()
	value := &tool.Artifact{Tool: "neutron", Kind: "vuln", Target: "https://test", Data: []byte(`{"matched":true}`), MediaType: "application/json", ResultId: "result/../../opaque"}
	record, err := Save(root, "r_1", "in_1", "ev_1", value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(root, "r_1", "in_1", "ev_replay", value); err != nil {
		t.Fatal(err)
	}
	rows, err := List(root, "r_1", "neutron", 50)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list %v %v", rows, err)
	}
	work := filepath.Join(root, "in_2")
	rel, err := Materialize(root, work, record.Ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(work, rel))
	if err != nil || string(raw) != string(value.Data) {
		t.Fatalf("citation changed bytes %s %v", raw, err)
	}
	value.Data = []byte("different")
	if _, err := Save(root, "r_1", "in_1", "ev_2", value); err == nil {
		t.Fatal("overwrote same identity with different bytes")
	}
	if _, _, err := Read(t.TempDir(), record.Ref); !os.IsNotExist(err) {
		t.Fatal("another project could resolve result")
	}
}

func TestNativeResultMissingIdentityDoesNotInventAResult(t *testing.T) {
	if _, err := Save(t.TempDir(), "r_1", "in_1", "", &tool.Artifact{}); err == nil {
		t.Fatal("accepted identityless result")
	}
}
