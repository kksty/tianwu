package access

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginMaterialKeepsSecretsOutOfMetadataAndPreservesBytes(t *testing.T) {
	root := t.TempDir()
	raw := json.RawMessage(`{"cookies":[{"name":"sid","value":"secret-session-value"}],"origins":[]}`)
	entry, err := Create(root, Input{Name: "用户 A", Kind: "state", Origin: "https://example.test/login", Account: "A", Material: raw})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := List(root)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list %v %v", rows, err)
	}
	meta, _ := json.Marshal(rows)
	if strings.Contains(string(meta), "secret-session-value") {
		t.Fatal("metadata leaked credential")
	}
	got, err := Material(root, entry.ID)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("material changed %s %v", got, err)
	}
	values := SecretValues(root)
	if len(values) != 1 || values[0] != "secret-session-value" {
		t.Fatalf("secrets %v", values)
	}
	if err := Delete(root, entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Material(root, entry.ID); !os.IsNotExist(err) {
		t.Fatalf("material still readable: %v", err)
	}
}

func TestBrowserBindingAcceptsHeadedAndLocalCDPButRejectsOtherHosts(t *testing.T) {
	for _, endpoint := range []string{"", "http://127.0.0.1:9222", "ws://localhost:9222/devtools/browser/a"} {
		if _, err := Create(t.TempDir(), Input{Name: "browser", Kind: "browser", Origin: "https://example.test", CDPURL: endpoint}); err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"http://evil.test:9222", "file:///etc/passwd", "http://user:pass@127.0.0.1:9222"} {
		if _, err := Create(t.TempDir(), Input{Name: "browser", Kind: "browser", Origin: "https://example.test", CDPURL: endpoint}); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}

func TestAuthenticatedRequestMustMatchDeclaredOrigin(t *testing.T) {
	input := Input{Name: "request", Kind: "request", Origin: "https://example.test", Material: json.RawMessage(`{"method":"GET","url":"https://other.test/me","headers":{}}`)}
	if _, err := Create(t.TempDir(), input); err == nil {
		t.Fatal("accepted another origin")
	}
	input.Material = json.RawMessage(`{"method":"GET","url":"https://example.test/me","headers":{"Authorization":"Bearer example-secret-token"}}`)
	root := t.TempDir()
	if _, err := Create(root, input); err != nil {
		t.Fatal(err)
	}
	if values := strings.Join(SecretValues(root), " "); !strings.Contains(values, "example-secret-token") {
		t.Fatal("token missing from redaction values")
	}
	if _, err := Material(root, "../escape"); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestDeletingBindingDoesNotRecursivelyDeleteUnownedFiles(t *testing.T) {
	root := t.TempDir()
	entry, err := Create(root, Input{Name: "browser", Kind: "browser", Origin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := path(root, entry.ID)
	if err := os.WriteFile(filepath.Join(dir, "unowned.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, entry.ID); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "unowned.txt")); err != nil || string(raw) != "keep" {
		t.Fatal("unowned file removed")
	}
}

func TestCDPOnlyRegistrationAndAliasesShareOneResource(t *testing.T) {
	root := t.TempDir()
	entry, err := Create(root, Input{Kind: "browser", CDPURL: "http://127.0.0.1:9222"})
	if err != nil || entry.Name == "" || entry.Origin != "" || entry.URL != "" {
		t.Fatalf("CDP-only: %+v %v", entry, err)
	}
	for _, endpoint := range []string{"http://localhost:9222/", "ws://127.0.0.1:9222/devtools/browser/abc"} {
		if _, err := Create(root, Input{Kind: "browser", CDPURL: endpoint, Profile: "another-host"}); err == nil {
			t.Fatalf("duplicate browser accepted: %s", endpoint)
		}
	}
	if _, err := Create(root, Input{Kind: "browser", CDPURL: "http://127.0.0.1:9223"}); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentAccountSessionsCanShareTargetButNotSessionIdentity(t *testing.T) {
	root := t.TempDir()
	for _, session := range []string{"account_A", "account_B"} {
		entry, err := Create(root, Input{Kind: "browser", Origin: "https://example.test/login", Session: session, Profile: "worker"})
		if err != nil || entry.Session != session {
			t.Fatalf("session %s: %+v %v", session, entry, err)
		}
	}
	if _, err := Create(root, Input{Kind: "browser", Origin: "https://example.test/other", Session: "account_A", Profile: "worker"}); err == nil {
		t.Fatal("duplicate session registered under another page")
	}
}
