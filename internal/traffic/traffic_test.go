package traffic

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

const fixture = `{"log":{"version":"1.2","creator":{"name":"mitmproxy","version":"11.0"},"entries":[
{"startedDateTime":"2026-10-31T12:00:00Z","request":{"method":"GET","url":"http://10.0.0.5:8080/admin"},"response":{"status":200,"content":{"mimeType":"text/html","size":12}},"serverIPAddress":"10.0.0.5"},
{"startedDateTime":"2026-10-31T12:00:01Z","request":{"method":"POST","url":"http://10.0.0.5:8080/login"},"response":{"status":403}},
{"startedDateTime":"2026-10-31T12:00:02Z","request":{"method":"GET","url":"https://corp.local/"},"response":{"status":200}}
]}}`

func writeHAR(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "capture.har")
	if err := os.WriteFile(p, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty.json": `{}`,
		"bad.json":   `not json`,
		"noent.json": `{"log":{"entries":[]}}`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(p); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
	if _, err := Parse(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("a missing file must be rejected")
	}
}

func TestSummariseGroupsByHost(t *testing.T) {
	h, err := Parse(writeHAR(t))
	if err != nil {
		t.Fatal(err)
	}
	hosts := Summarise(h)
	if len(hosts) != 2 {
		t.Fatalf("hosts = %d, want 2: %+v", len(hosts), hosts)
	}
	if hosts[0].Key != "10.0.0.5:8080" || hosts[0].Requests != 2 {
		t.Fatalf("first host = %+v", hosts[0])
	}
	if hosts[0].Methods["GET"] != 1 || hosts[0].Methods["POST"] != 1 {
		t.Fatalf("methods = %+v", hosts[0].Methods)
	}
	if hosts[0].Statuses[200] != 1 || hosts[0].Statuses[403] != 1 {
		t.Fatalf("statuses = %+v", hosts[0].Statuses)
	}
	if hosts[0].IP != "10.0.0.5" {
		t.Fatalf("ip = %q", hosts[0].IP)
	}
	if hosts[1].Key != "corp.local" {
		t.Fatalf("second host = %q", hosts[1].Key)
	}
}

func TestImportWritesEvidenceAndFacts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	im := &Importer{Store: st, CaseRoot: caseRoot}

	res, err := im.Import(writeHAR(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Entries != 3 || res.Hosts != 2 || res.Facts != 2 {
		t.Fatalf("result = %+v", res)
	}

	in, ok := st.GetIntent(res.IntentID)
	if !ok || in.Title == "" {
		t.Fatalf("import must be traceable to an intent: %+v", in)
	}
	if in.Status != model.StatusDone {
		t.Fatalf("import intent status = %s, want done", in.Status)
	}
	ev, ok := st.EvidenceByID(res.EvidenceID)
	if !ok {
		t.Fatal("evidence row missing")
	}
	if _, err := os.Stat(filepath.Join(caseRoot, filepath.FromSlash(ev.URI))); err != nil {
		t.Fatalf("evidence file missing on disk: %v", err)
	}

	facts := st.ListFacts("", 0)
	var services, notes int
	for _, f := range facts {
		switch f.Kind {
		case "service":
			services++
			if len(f.EvidenceIDs) != 1 || f.EvidenceIDs[0] != res.EvidenceID {
				t.Fatalf("service fact must point at the capture: %+v", f)
			}
			if f.Origin != model.OriginDerived {
				t.Fatalf("traffic facts are derived, got %q", f.Origin)
			}
		case "note":
			notes++
		}
	}
	if services != 2 || notes != 1 {
		t.Fatalf("facts: services=%d notes=%d", services, notes)
	}
}

func TestImportDoesNotDuplicateHosts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	im := &Importer{Store: st, CaseRoot: t.TempDir()}
	har := writeHAR(t)

	if _, err := im.Import(har); err != nil {
		t.Fatal(err)
	}
	second, err := im.Import(har)
	if err != nil {
		t.Fatal(err)
	}
	if second.Facts != 0 {
		t.Fatalf("re-import must not duplicate hosts, created %d", second.Facts)
	}

	var services int
	for _, f := range st.ListFacts("", 0) {
		if f.Kind == "service" {
			services++
		}
	}
	if services != 2 {
		t.Fatalf("service facts = %d, want 2", services)
	}
}

func TestImportResolvesCaseRelativePath(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseRoot, "captures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseRoot, "captures", "small.har"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	im := &Importer{Store: st, CaseRoot: caseRoot}
	res, err := im.Import("captures/small.har")
	if err != nil {
		t.Fatalf("relative path must resolve against the case root: %v", err)
	}
	if res.Entries != 3 || res.Hosts != 2 {
		t.Fatalf("result = %+v", res)
	}
}
