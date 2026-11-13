package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// The overview's guard panel reads board.guard: the watchdog must publish its
// cadence and idle threshold there instead of letting the page hardcode them.
func TestBoardPublishesGuardCadence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, t.TempDir(), t.TempDir())
	s.IdleTimeout = 30 * time.Minute
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/board")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got struct {
		Guard struct {
			IntervalSeconds    int        `json:"interval_seconds"`
			IdleTimeoutSeconds int        `json:"idle_timeout_seconds"`
			LastTickAt         *time.Time `json:"last_tick_at"`
		} `json:"guard"`
		Decider struct {
			Enabled bool `json:"enabled"`
		} `json:"decider"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Guard.IntervalSeconds != int(watchInterval/time.Second) {
		t.Fatalf("interval = %d, want %d", got.Guard.IntervalSeconds, int(watchInterval/time.Second))
	}
	if got.Guard.IdleTimeoutSeconds != 30*60 {
		t.Fatalf("idle timeout = %d, want 1800", got.Guard.IdleTimeoutSeconds)
	}
	if got.Guard.LastTickAt != nil {
		t.Fatalf("last tick before Watch ran = %v, want nil", got.Guard.LastTickAt)
	}
	if got.Decider.Enabled {
		t.Fatal("decider without a model must report disabled")
	}
}

// The briefing strip compares board.digest with decider.last_digest to say
// whether the next heartbeat will wake the commander. That comparison is only
// honest if the API publishes the value the decider hashes — including a change
// no count can see, like rewriting a fact's doc (D79).
func TestBoardPublishesWakeDigest(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "note", Title: "一条观察"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func() (digest, last string) {
		res, err := http.Get(srv.URL + "/api/board")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got struct {
			Digest  string `json:"digest"`
			Decider struct {
				LastDigest string `json:"last_digest"`
			} `json:"decider"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got.Digest, got.Decider.LastDigest
	}

	before, _ := get()
	if before == "" {
		t.Fatal("the board published no digest, so the page cannot compare it")
	}
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "重放：curl -s http://10.0.0.5:8080/" }); err != nil {
		t.Fatal(err)
	}
	after, _ := get()
	if after == before {
		t.Fatalf("a doc edit did not reach the published digest: %s", before)
	}
}
