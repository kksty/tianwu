package ioa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func observerHistoryServer(t *testing.T, history http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("observer attempted a write: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/ioa/spaces":
			fmt.Fprint(w, `[{"id":"sp-1","name":"default"}]`)
		case "/ioa/nodes":
			fmt.Fprint(w, `[{"id":"web-id","name":"cyber.web"},{"id":"agent-id","name":"local"}]`)
		case "/ioa/spaces/sp-1/messages":
			if r.URL.Query().Get("all") != "true" {
				t.Error("observer did not request space history")
			}
			history(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestObserverRecoversAnExpiredCursorWithoutLosingOrDuplicatingHistory(t *testing.T) {
	dir := t.TempDir()
	seed := []Record{
		{Seq: 1, IOAMessageID: "old-transport", Envelope: &Envelope{Version: 1, Event: EventMessage, ID: "shared"}},
		{Seq: 2, IOAMessageID: "plain", Envelope: &Envelope{Version: 1, Event: EventMessage}},
		{Seq: 3, IOAMessageID: "rejected", Error: "unsupported version"},
	}
	var log strings.Builder
	for _, rec := range seed {
		raw, _ := json.Marshal(rec)
		log.Write(raw)
		log.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cursor"), []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := []Message{
		{ID: "new-transport", Meta: map[string]any{"version": 1, "event": "message", "id": "shared"}},
		{ID: "plain", Meta: map[string]any{"version": 1, "event": "message"}},
		{ID: "rejected", Meta: map[string]any{"version": 99, "event": "message"}},
	}
	for i := range 255 {
		history = append(history, Message{ID: fmt.Sprint("m", i), Content: map[string]any{"text": strings.Repeat("x", 4500)}, Meta: map[string]any{"version": 1, "event": "message", "id": fmt.Sprint("e", i)}})
	}
	history = append(history, Message{ID: "foreign-tail", Meta: map[string]any{"other": true}})
	var snapshots, requests atomic.Int32
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch after := r.URL.Query().Get("after"); after {
		case "gone":
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"detail":"expired cursor; wording is not a machine contract"}`)
		case "":
			snapshots.Add(1)
			rows := history
			// Match IOA's tail-window semantics, including the failure mode.
			if value := r.URL.Query().Get("limit"); value != "" {
				limit, _ := strconv.Atoi(value)
				if limit > 0 && len(rows) > limit {
					rows = rows[len(rows)-limit:]
				}
			}
			json.NewEncoder(w).Encode(rows)
		case "foreign-tail":
			if r.URL.Query().Get("limit") != "200" {
				t.Error("incremental page lost its bound")
			}
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected cursor %s", after)
			w.WriteHeader(500)
		}
	})
	defer srv.Close()
	obs, err := NewObserver(New(srv.URL, "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	added, err := obs.Step(context.Background())
	if err != nil || added != 255 {
		t.Fatalf("recovery=%d %v", added, err)
	}
	records, err := obs.Records()
	if err != nil || len(records) != 258 {
		t.Fatalf("records=%d %v", len(records), err)
	}
	for i, rec := range records {
		if rec.Seq != int64(i+1) {
			t.Fatalf("sequence=%d at %d", rec.Seq, i)
		}
	}
	if obs.Cursor() != "foreign-tail" {
		t.Fatalf("cursor=%s", obs.Cursor())
	}
	reopened, err := NewObserver(New(srv.URL, "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.Step(context.Background()); err != nil || n != 0 {
		t.Fatalf("restart=%d %v", n, err)
	}
	if snapshots.Load() != 1 || requests.Load() != 3 {
		t.Fatalf("snapshots=%d requests=%d", snapshots.Load(), requests.Load())
	}
}

func TestObserverInitialSnapshotDoesNotSkipOlderMessages(t *testing.T) {
	var count atomic.Int32
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Query().Get("after") != "" || r.URL.Query().Get("limit") != "" {
			t.Error("initial snapshot would be a tail window")
		}
		fmt.Fprint(w, `[{"id":"first","meta":{"version":1,"event":"message","id":"e1"}}]`)
	})
	defer srv.Close()
	obs, err := NewObserver(New(srv.URL, "", "local"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n, err := obs.Step(context.Background()); err != nil || n != 1 || count.Load() != 1 {
		t.Fatalf("initial=%d %v", n, err)
	}
}

func TestObserverPersistsEmptyHistoryAfterCursorExpires(t *testing.T) {
	var expired atomic.Int32
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "" {
			expired.Add(1)
			w.WriteHeader(422)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cursor"), []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		obs, err := NewObserver(New(srv.URL, "", "local"), dir)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := obs.Step(context.Background()); err != nil || n != 0 || obs.Cursor() != "" {
			t.Fatalf("empty recovery=%d %v cursor=%s", n, err, obs.Cursor())
		}
	}
	if expired.Load() != 1 {
		t.Fatal("empty history kept the rejected cursor")
	}
}

func TestObserverKeepsCursorWhenHistoryReadOrRecoveryFails(t *testing.T) {
	for _, status := range []int{401, 403, 404, 422, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var resets atomic.Int32
			srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("after") == "" {
					resets.Add(1)
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(status)
			})
			defer srv.Close()
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "cursor"), []byte("keep"), 0o600)
			obs, err := NewObserver(New(srv.URL, "", "local"), dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = obs.Step(context.Background()); err == nil {
				t.Fatal("failed read hidden")
			}
			persisted, _ := os.ReadFile(filepath.Join(dir, "cursor"))
			if obs.Cursor() != "keep" || string(persisted) != "keep" {
				t.Fatal("failed read discarded cursor")
			}
			want := int32(0)
			if status == 422 {
				want = 1
			}
			if resets.Load() != want {
				t.Fatalf("snapshot attempts=%d want=%d", resets.Load(), want)
			}
		})
	}
}

func TestCursorRecoveryRejectsIncompleteSnapshots(t *testing.T) {
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "" {
			w.WriteHeader(422)
			return
		}
		fmt.Fprint(w, `[{"id":"partial","meta":{"version":1,"event":"message"}}`)
	})
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cursor"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	obs, err := NewObserver(New(srv.URL, "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = obs.Step(context.Background()); err == nil {
		t.Fatal("partial history accepted")
	}
	if obs.Cursor() != "keep" {
		t.Fatal("partial snapshot discarded cursor")
	}
	if records, err := obs.Records(); err != nil || len(records) != 0 {
		t.Fatalf("partial observations=%v %v", records, err)
	}
}

func TestHistoryValidationWithoutCursorIsNotCursorExpiration(t *testing.T) {
	var requests atomic.Int32
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(422) })
	defer srv.Close()
	_, err := New(srv.URL, "", "local").Read(context.Background(), "", 0)
	if err == nil || errors.Is(err, ErrCursorGone) || requests.Load() != 1 {
		t.Fatalf("validation classified as expiration: %v", err)
	}
}
