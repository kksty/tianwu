package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// recordRel is the endpoint/PID marker of the local host, below the instance's
// data directory. The token is deliberately absent: it is generated per process
// (host.go) and writing it here would put a shell credential on disk.
const recordRel = "host/local.json"

// Record is what the previous process's local host left behind. Its only uses
// are diagnostics ("the last host listened on :18766 and did not stop
// cleanly") and deciding which loopback endpoints are stale when sweeping
// session markers. Nothing in the control plane trusts it beyond that.
type Record struct {
	Version   int       `json:"version"`
	Endpoint  string    `json:"endpoint"`
	PID       int       `json:"pid"`
	Agent     string    `json:"agent,omitempty"`
	DB        string    `json:"db,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Bin       string    `json:"bin,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts,omitempty"`
}

func recordPath(dataDir string) string { return filepath.Join(dataDir, recordRel) }

// ReadRecord reads the previous run's host record. found is false when there is
// no record (a clean stop removed it) or when the record is not trustworthy.
// The error is informational: callers log it and continue.
func ReadRecord(dataDir string) (rec Record, found bool, err error) {
	if strings.TrimSpace(dataDir) == "" {
		return Record{}, false, nil
	}
	data, readErr := os.ReadFile(recordPath(dataDir))
	if errors.Is(readErr, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if readErr != nil {
		return Record{}, false, fmt.Errorf("读宿主记录：%w", readErr)
	}
	if jsonErr := json.Unmarshal(data, &rec); jsonErr != nil {
		return Record{}, false, fmt.Errorf("宿主记录 %s 不是合法 JSON：%w", recordPath(dataDir), jsonErr)
	}
	if !IsLoopbackEndpoint(rec.Endpoint) {
		// A non-loopback endpoint in this file is either a hand edit or a
		// different deployment's record: never act on it (hosts are loopback by
		// design; main.go only warns when one is not).
		return Record{}, false, fmt.Errorf("宿主记录 endpoint %q 不是 loopback，已忽略", rec.Endpoint)
	}
	return rec, true, nil
}

// WriteRecord atomically persists the current host's endpoint/PID. It runs
// after every readiness check, so a crash is followed by a record whose PID is
// dead — which is exactly the "previous process died without stopping" signal.
func WriteRecord(dataDir string, rec Record) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	if rec.Version == 0 {
		rec.Version = 1
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := recordPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建宿主记录目录：%w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveRecord deletes the marker on a clean stop. A missing file is fine.
func RemoveRecord(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	if err := os.Remove(recordPath(dataDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeRecord persists the live host. Best-effort: an unwritable data directory
// must not make the host unready.
func (h *Host) writeRecord() {
	if !h.cfg.Persist {
		return
	}
	h.mu.Lock()
	rec := Record{
		Version:   1,
		Endpoint:  h.addr,
		PID:       h.cmdPIDLocked(),
		Agent:     LocalAgent,
		DB:        h.db,
		Dir:       h.cfg.Dir,
		Bin:       h.cfg.Bin,
		StartedAt: h.started,
		Restarts:  h.restarts,
	}
	h.mu.Unlock()
	if err := WriteRecord(h.cfg.Dir, rec); err != nil {
		h.cfg.Logf("host_event=record_error err=%q", err.Error())
	}
}

func (h *Host) removeRecord() {
	if !h.cfg.Persist {
		return
	}
	if err := RemoveRecord(h.cfg.Dir); err != nil {
		h.cfg.Logf("host_event=record_error err=%q", err.Error())
	}
}

// IsLoopbackEndpoint reports whether a host URL points at this machine. Hosts
// are expected to stay on loopback (their token is a shell); a URL elsewhere
// belongs to a remote node and is never swept by this instance.
func IsLoopbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
