package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/ioa"
)

var errNoIOAObserver = errors.New("IOA observer 未启动")

// Every already-started model host has its own native IOA origin. Observing a
// profile must never materialize a host merely to inspect its messages.
func (h *hub) observeNativeHost(profile string, endpoint, token, agent string) {
	h.ioaMu.Lock()
	defer h.ioaMu.Unlock()
	if h.ioaSources == nil {
		h.ioaSources = make(map[string]*ioa.Observer)
	}
	// Retired hosts can still own live runs. They keep independent observers
	// and cursors; a replacement must not steal the old host's log ownership.
	digest := sha256.Sum256([]byte(profile + "\x00" + endpoint + "\x00" + token))
	key := fmt.Sprintf("%x", digest[:16])
	if h.ioaSources[key] != nil {
		return
	}
	dir := filepath.Join(h.cfg.DataDir, "ioa", "hosts", key)
	o, err := ioa.NewObserver(ioa.New(endpoint, token, agent), dir)
	if err != nil {
		log.Printf("ioa profile observer: %v", err)
		return
	}
	h.ioaSources[key] = o
	o.OnRecord = h.onNativeIOA
	go func() {
		o.Run(h.ctx, 5*time.Second, func(err error) { log.Printf("ioa profile observer: %v", err) })
	}()
}

func (h *hub) nativeIOARecords() ([]ioa.Record, error) {
	h.ioaMu.Lock()
	observers := make([]*ioa.Observer, 0, len(h.ioaSources)+1)
	dirs := make(map[string]bool)
	if h.cfg.IOA != nil {
		observers = append(observers, h.cfg.IOA)
	}
	for key, source := range h.ioaSources {
		observers = append(observers, source)
		dirs[filepath.Join(h.cfg.DataDir, "ioa", "hosts", key)] = true
	}
	h.ioaMu.Unlock()
	records := make([]ioa.Record, 0)
	seen := make(map[string]bool)
	for _, observer := range observers {
		rows, err := observer.Records()
		if err != nil {
			return nil, err
		}
		for _, rec := range rows {
			if !seen[rec.Key()] {
				records = append(records, rec)
				seen[rec.Key()] = true
			}
		}
	}
	archived := false
	if h.cfg.DataDir != "" {
		root := filepath.Join(h.cfg.DataDir, "ioa", "hosts")
		entries, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			archived = true
			dir := filepath.Join(root, entry.Name())
			if dirs[dir] {
				continue
			}
			rows, err := ioa.ReadRecords(dir)
			if err != nil {
				return nil, err
			}
			for _, rec := range rows {
				if !seen[rec.Key()] {
					records = append(records, rec)
					seen[rec.Key()] = true
				}
			}
		}
	}
	if len(observers) == 0 && !archived {
		return nil, errNoIOAObserver
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, ae := time.Parse(time.RFC3339Nano, records[i].CreatedAt)
		b, be := time.Parse(time.RFC3339Nano, records[j].CreatedAt)
		if ae != nil {
			a = records[i].ObservedAt
		}
		if be != nil {
			b = records[j].ObservedAt
		}
		if a.Equal(b) {
			return records[i].Key() < records[j].Key()
		}
		return a.Before(b)
	})
	return records, nil
}

// Directed native mail stays in the harness mailbox. Only the control plane
// may schedule its next commander turn, so pause and tracked usage also apply
// to peer-triggered work. No text is interpreted or copied into facts/mail.
func (h *hub) onNativeIOA(rec ioa.Record) error {
	if rec.Error != "" || rec.Envelope == nil || rec.Envelope.Event != ioa.EventMessage || rec.Envelope.Target.SessionID == "" {
		return nil
	}
	h.mu.RLock()
	kits := make([]*kit, 0, len(h.kits))
	for _, k := range h.kits {
		kits = append(kits, k)
	}
	h.mu.RUnlock()
	var failures []error
	for _, k := range kits {
		if k.srv == nil || k.dec == nil {
			continue
		}
		if sess, ok := aopexec.ReadSession(k.srv.CommanderDir()); ok && sess.Session == rec.Envelope.Target.SessionID {
			if err := k.proj.Store.NoteCommanderNativeMailAt(rec.Key(), rec.ObserverID, rec.Seq); err != nil {
				failures = append(failures, fmt.Errorf("native commander scheduling: %w", err))
				continue
			}
			k.dec.Wake("message")
		}
	}
	return errors.Join(failures...)
}

// sessionLabels names the harness sessions one project can see: the commander,
// each intent's current session and every run's session. A session id is opaque
// on the wire, so an id outside this map can only be shown as unlabelled — and
// must not be guessed to be a person.
func sessionLabels(k *kit) map[string]string {
	sessions := make(map[string]string)
	if k == nil || k.srv == nil {
		return sessions
	}
	addMarker := func(dir, title string) {
		if sess, ok := aopexec.ReadSession(dir); ok {
			sessions[sess.Session] = title
		}
	}
	addMarker(k.srv.CommanderDir(), "指挥官")
	for _, in := range k.srv.Store.ListIntents() {
		addMarker(filepath.Join(k.srv.CaseRoot, in.ID), in.Title)
	}
	for _, run := range k.srv.Store.Runs() {
		if run.SessionID == "" {
			continue
		}
		label := run.IntentID
		if run.Purpose != "" {
			label = run.Purpose
			if run.Purpose == "commander" {
				label = "指挥官"
			}
		} else if in, ok := k.srv.Store.GetIntent(run.IntentID); ok {
			label = in.Title
		}
		sessions[run.SessionID] = label
	}
	return sessions
}

// A native IOA observation is not a ledger delivery. Project attribution uses
// actual harness identities, never message text, and does not send or ack mail.
func (h *hub) serveProjectIOA(w http.ResponseWriter, r *http.Request, k *kit) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	records, err := h.nativeIOARecords()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errNoIOAObserver) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	sessions := sessionLabels(k)
	filtered := make([]ioa.Record, 0)
	for _, rec := range records {
		if rec.Envelope == nil {
			continue
		}
		_, source := sessions[rec.Envelope.Source.SessionID]
		_, target := sessions[rec.Envelope.Target.SessionID]
		if source || target {
			filtered = append(filtered, rec)
		}
	}
	if len(filtered) > 500 {
		filtered = filtered[len(filtered)-500:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": filtered, "sessions": sessions})
}
