package aop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAgentsReadsTheWorkingDirectory pins the one field the executor cannot
// guess: where an agent's tools resolve relative paths (D43).
func TestAgentsReadsTheWorkingDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cyber.rpc.agent.AgentService/ListAgents" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agents":[
		  {"hello":{"nodeId":"local","name":"local","runtime":{"hostname":"h","os":"windows","workingDir":"C:\\data"}},
		   "status":{"provider":"openai","model":"m","bound":true,"configError":"provider unavailable"},
		   "commands":[{"name":"!scan","description":"Scan targets","usage":"!scan <target>"},{"name":"/skill:commander"}]},
		  {"hello":{"nodeId":"gpu","runtime":{"workingDir":"/srv/data"}}}
		]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	agents, err := c.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("agents = %+v", agents)
	}
	if agents[0].WorkingDir != `C:\data` || agents[0].Model != "m" || !agents[0].Bound {
		t.Fatalf("agent[0] = %+v", agents[0])
	}
	if agents[0].ConfigError != "provider unavailable" || len(agents[0].Commands) != 2 || agents[0].Commands[0].Usage != "!scan <target>" {
		t.Fatalf("host catalogue/health was lost: %+v", agents[0])
	}
	if a, ok := Find(agents, "gpu"); !ok || a.WorkingDir != "/srv/data" {
		t.Fatalf("Find(gpu) = %+v %v", a, ok)
	}
	if _, ok := Find(agents, "missing"); ok {
		t.Fatal("Find returned an agent that is not registered")
	}
	// Two agents and no pinning is a guess; the caller must not get one.
	if _, ok := Find(agents, ""); ok {
		t.Fatal("Find with no id must only answer when there is exactly one agent")
	}
	if a, ok := Find(agents[:1], ""); !ok || a.ID != "local" {
		t.Fatalf("Find(\"\", one agent) = %+v %v", a, ok)
	}
}

func TestAgentsRejectsBadTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "wrong").Agents(context.Background()); err == nil {
		t.Fatal("a 401 must surface as an error")
	}
}
