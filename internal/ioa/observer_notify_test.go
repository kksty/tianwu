package ioa

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestObserverNotifiesNewIncrementalRecordsButNotBootstrapRecoveryOrDuplicates(t *testing.T) {
	step := 0
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch step {
		case 0:
			fmt.Fprint(w, `[]`)
		case 1:
			fmt.Fprint(w, `[{"id":"m1","meta":{"version":1,"event":"message","id":"e1"}}]`)
		case 2:
			fmt.Fprint(w, `[{"id":"m1","meta":{"version":1,"event":"message","id":"e1"}}]`)
		case 3:
			if r.URL.Query().Get("after") != "" {
				w.WriteHeader(422)
				return
			}
			fmt.Fprint(w, `[{"id":"m2","meta":{"version":1,"event":"message","id":"e2"}}]`)
		}
	})
	defer srv.Close()
	o, err := NewObserver(New(srv.URL, "", "local"), filepath.Join(t.TempDir(), "ioa"))
	if err != nil {
		t.Fatal(err)
	}
	var notified []string
	o.OnRecord = func(r Record) error { notified = append(notified, r.IOAMessageID); return nil }
	for step = 0; step < 4; step++ {
		if _, err := o.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(notified) != 1 || notified[0] != "m1" {
		t.Fatalf("incremental notifications: %v", notified)
	}
	records, err := o.Records()
	if err != nil || len(records) != 2 {
		t.Fatalf("observations lost: %v %v", records, err)
	}
}

func TestObserverRetriesFailedSchedulingWithoutDuplicatingObservation(t *testing.T) {
	step := 0
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if step == 0 {
			fmt.Fprint(w, `[]`)
		} else {
			fmt.Fprint(w, `[{"id":"m1","meta":{"version":1,"event":"message","id":"e1"}}]`)
		}
	})
	defer srv.Close()
	o, err := NewObserver(New(srv.URL, "", "local"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	calls := 0
	o.OnRecord = func(rec Record) error {
		calls++
		if calls == 1 {
			return errors.New("ledger unavailable")
		}
		return nil
	}
	if n, err := o.Step(context.Background()); n != 1 || err == nil {
		t.Fatalf("scheduling failure was lost: n=%d err=%v", n, err)
	}
	if n, err := o.Step(context.Background()); n != 0 || err != nil || calls != 2 {
		t.Fatalf("scheduling was not retried: n=%d err=%v calls=%d", n, err, calls)
	}
	rows, err := o.Records()
	if err != nil || len(rows) != 1 {
		t.Fatalf("retry duplicated observation: %+v %v", rows, err)
	}
	reopened, err := NewObserver(New(srv.URL, "", "local"), o.dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.OnRecord = o.OnRecord
	if _, err := reopened.Step(context.Background()); err != nil || calls != 2 {
		t.Fatalf("acknowledged scheduling replayed: %v calls=%d", err, calls)
	}
}

func TestObserverReplaysLoggedDemandAfterRestartEvenWhenHostIsOffline(t *testing.T) {
	created := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	offline := false
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if offline {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `[{"id":"live","created_at":%q,"meta":{"version":1,"event":"message","id":"live"}}]`, created)
	})
	defer srv.Close()
	dir := t.TempDir()
	o, err := NewObserver(New(srv.URL, "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	// The live row is durable, but the process has not called a consumer yet.
	if _, err := o.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewObserver(New(srv.URL, "", "local"), dir)
	if err != nil {
		t.Fatal(err)
	}
	var delivered []Record
	reopened.OnRecord = func(rec Record) error { delivered = append(delivered, rec); return nil }
	offline = true
	if _, err := reopened.Step(context.Background()); err == nil {
		t.Fatal("offline host reported success")
	}
	if len(delivered) != 1 || delivered[0].Key() != "live" || delivered[0].ObserverID == "" || delivered[0].Seq != 1 {
		t.Fatalf("logged scheduling demand was lost: %+v", delivered)
	}
}

func TestObserverRetriesWhenNotificationCheckpointCannotBeSaved(t *testing.T) {
	created := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":"live","created_at":%q,"meta":{"version":1,"event":"message","id":"live"}}]`, created)
	})
	defer srv.Close()
	o, err := NewObserver(New(srv.URL, "", "local"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the temporary file path makes the receipt write fail.
	if err := os.Mkdir(o.notificationPath()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	o.OnRecord = func(rec Record) error { calls++; return nil }
	if _, err := o.Step(context.Background()); err == nil || calls != 1 {
		t.Fatalf("receipt failure was hidden: %v calls=%d", err, calls)
	}
	if err := os.Remove(o.notificationPath() + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Step(context.Background()); err != nil || calls != 2 {
		t.Fatalf("unreceipted notification was lost: %v calls=%d", err, calls)
	}
}

func TestNewMailDuringFirstPollNotifiesWithoutReplayingHistoricalMail(t *testing.T) {
	created := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	srv := observerHistoryServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":"old","created_at":"2020-01-01T00:00:00Z","meta":{"version":1,"event":"message","id":"old"}},{"id":"new","created_at":%q,"meta":{"version":1,"event":"message","id":"new"}}]`, created)
	})
	defer srv.Close()
	o, err := NewObserver(New(srv.URL, "", "local"), filepath.Join(t.TempDir(), "ioa"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	o.OnRecord = func(rec Record) error { ids = append(ids, rec.IOAMessageID); return nil }
	if _, err := o.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "new" {
		t.Fatalf("bootstrap notifications: %v", ids)
	}
}
