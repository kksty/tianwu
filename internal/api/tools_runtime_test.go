package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeCatalogueUsesHostDeclarationsWithoutRunningCommands(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cyber.rpc.agent.AgentService/ListAgents" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected host operation: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		_, _ = w.Write([]byte(`{"agents":[{"hello":{"nodeId":"local"},"status":{"provider":"openai","model":"fixture","configError":"key secret-fixture invalid"},"commands":[{"name":"/skill:commander"},{"name":"!scan","usage":"!scan <target>"}]}]}`))
	}))
	defer host.Close()
	s := New(nil, termExec{url: host.URL}, t.TempDir(), t.TempDir())
	s.Secrets = func() []string { return []string{"secret-fixture"} }
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tools/runtime", nil))
	var got runtimeToolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || got.State != "connected" || got.Model != "fixture" || got.ModelError != "key *** invalid" || len(got.Commands) != 2 || got.Commands[0].Name != "!scan" || got.Commands[0].Usage != "!scan <target>" {
		t.Fatalf("runtime catalogue = %s", rec.Body.String())
	}
}

func TestUnavailableHostIsNotAnEmptySuccessfulCatalogue(t *testing.T) {
	for _, body := range []string{`{"agents":[]}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer host.Close()
			s := New(nil, termExec{url: host.URL}, t.TempDir(), t.TempDir())
			rec := httptest.NewRecorder()
			s.listRuntimeTools(rec, httptest.NewRequest(http.MethodGet, "/api/tools/runtime", nil))
			var got runtimeToolsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.State != "unavailable" || got.Error == "" || got.Commands == nil {
				t.Fatalf("unavailable = %s", rec.Body.String())
			}
		})
	}
}
