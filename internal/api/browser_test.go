package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/access"
)

func TestUnconfirmedBrowserCommandCannotBeRetriedOrReplayed(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	entry, err := access.Create(s.CaseRoot, access.Input{Name: "现场", Kind: "browser", Origin: "https://example.test/login"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.CaseRoot, ".access", entry.ID, "control-session.json")
	raw, _ := json.Marshal(browserControl{Endpoint: "http://127.0.0.1:18766", Session: "control-session", Pending: true})
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = s.runBrowserCommand(context.Background(), entry, "!playwright open")
	if err == nil || !strings.Contains(err.Error(), "不能安全重发") {
		t.Fatalf("uncertain command was retried: %v", err)
	}
}
