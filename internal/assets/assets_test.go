package assets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/store"
)

const fixture = `{"ip":"10.0.0.5","port":"80","protocol":"http","status":"200","uri":"/","host":"www.example.local","title":"Example","midware":"nginx","frameworks":[{"name":"nginx","attributes":{"version":"1.18.0"}}]}
{"ip":"10.0.0.5","port":"22","protocol":"ssh","status":"open"}
{"ip":"10.0.0.6","port":"3306","protocol":"mysql","status":"open","vulns":[{"name":"weak-password"}]}
not json at all
{"ip":"","port":"443"}
`

func writeJSONL(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "scan.jsonl")
	if err := os.WriteFile(p, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseJSONLSkipsNoise(t *testing.T) {
	records, err := ParseJSONL(writeJSONL(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3: %+v", len(records), records)
	}
	if records[0].Key() != "10.0.0.5:80" {
		t.Fatalf("key = %q", records[0].Key())
	}
	if got := records[0].Version(); got != "nginx/1.18.0" {
		t.Fatalf("version = %q", got)
	}
}

func TestParseJSONLAcceptsArray(t *testing.T) {
	p := filepath.Join(t.TempDir(), "arr.json")
	body := `[{"ip":"1.1.1.1","port":"443","protocol":"https"}]`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := ParseJSONL(p)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v err = %v", records, err)
	}
}

func TestParseJSONLRejectsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(p, []byte("\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseJSONL(p); err == nil {
		t.Fatal("an empty file must be rejected")
	}
}

func TestImportDerivesHostAndServiceFacts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	im := &Importer{Store: st, CaseRoot: caseRoot}

	res, err := im.Import(writeJSONL(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Records != 3 || res.Hosts != 2 {
		t.Fatalf("result = %+v", res)
	}
	// 3 service + 2 host = 5, plus the note.
	if res.Facts != 5 {
		t.Fatalf("facts = %d, want 5", res.Facts)
	}

	byTitle := map[string]string{}
	for _, f := range st.ListFacts("", 0) {
		byTitle[f.Title] = f.Kind
	}
	for _, want := range []string{"10.0.0.5:80", "10.0.0.5:22", "10.0.0.6:3306", "10.0.0.5", "10.0.0.6", "资产导入"} {
		if _, ok := byTitle[want]; !ok {
			t.Fatalf("missing fact %q, got %v", want, byTitle)
		}
	}
	if byTitle["10.0.0.5:80"] != "service" || byTitle["10.0.0.5"] != "host" {
		t.Fatalf("kinds = %v", byTitle)
	}
}

func TestImportIsIdempotentForHosts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	im := &Importer{Store: st, CaseRoot: t.TempDir()}
	path := writeJSONL(t)

	if _, err := im.Import(path); err != nil {
		t.Fatal(err)
	}
	second, err := im.Import(path)
	if err != nil {
		t.Fatal(err)
	}
	if second.Facts != 0 {
		t.Fatalf("re-import must not duplicate hosts, created %d", second.Facts)
	}
}

func TestImportResolvesCaseRelativePath(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseRoot, "scans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "scans", "s.jsonl"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	im := &Importer{Store: st, CaseRoot: caseRoot}
	res, err := im.Import("scans/s.jsonl")
	if err != nil {
		t.Fatalf("relative path must resolve against the case root: %v", err)
	}
	if res.Hosts != 2 {
		t.Fatalf("result = %+v", res)
	}
}
