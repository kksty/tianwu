package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The orphan sweep is tested through its two seams (listProcesses,
// terminateProcess): no unit test ever kills a process.

type killCall struct {
	pid    int
	gentle bool
}

func sweepWith(t *testing.T, procs []procInfo, dataDir string, dry bool) (OrphanSweep, []killCall, *testLog) {
	t.Helper()
	if dry {
		t.Setenv("TW_HOST_CLEANUP", "dry")
	} else {
		t.Setenv("TW_HOST_CLEANUP", "")
	}
	var (
		mu    sync.Mutex
		kills []killCall
	)
	oldList, oldTerm := listProcesses, terminateProcess
	listProcesses = func(context.Context) ([]procInfo, error) { return procs, nil }
	terminateProcess = func(pid int, gentle bool) error {
		mu.Lock()
		kills = append(kills, killCall{pid: pid, gentle: gentle})
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		listProcesses, terminateProcess = oldList, oldTerm
	})
	logs := &testLog{}
	swept, err := SweepOrphans(context.Background(), dataDir, logs.Logf)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	return swept, kills, logs
}

func dataDirUnder(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func hostProc(pid, parent int, dataDir string) procInfo {
	return procInfo{
		PID:            pid,
		ParentPID:      parent,
		Name:           "aiscan-full.exe",
		CommandLine:    fmt.Sprintf(`"C:\tools\aiscan-full.exe" web --db %s`, filepath.Join(dataDir, "host", "cyber-web.db")),
		ExecutablePath: `C:\tools\aiscan-full.exe`,
	}
}

func TestOrphanRequiresMatchingDataDir(t *testing.T) {
	data := dataDirUnder(t, "data-a")
	procs := []procInfo{
		hostProc(500, 1, data),                                                     // this instance's leftover
		hostProc(501, 1, dataDirUnder(t, "data-b")),                                // another instance
		{PID: 502, ParentPID: 1, Name: "notepad.exe", CommandLine: "--db " + data}, // wrong binary
	}
	swept, kills, _ := sweepWith(t, procs, data, false)
	if len(kills) != 1 || kills[0].pid != 500 {
		t.Fatalf("kills = %+v, sweep=%+v", kills, swept)
	}
	if swept.Reaped != 1 || swept.Candidates != 1 {
		t.Fatalf("swept = %+v", swept)
	}
}

// The four acceptance instances share one machine; `data-a` is a prefix of
// `data-ab`, so the data-directory match must be on a path boundary.
func TestOrphanNeverKillsOtherInstanceHost(t *testing.T) {
	root := t.TempDir()
	dataA := filepath.Join(root, "data-a")
	dataAB := filepath.Join(root, "data-ab")
	procs := []procInfo{
		hostProc(600, 1, dataAB),
		hostProc(601, 1, dataA),
	}
	_, kills, _ := sweepWith(t, procs, dataA, false)
	if len(kills) != 1 || kills[0].pid != 601 {
		t.Fatalf("kills = %+v, want only data-a's own host", kills)
	}
}

func TestOrphanNeverKillsCurrentHostOrItsChildren(t *testing.T) {
	data := dataDirUnder(t, "data-c")
	if err := WriteRecord(data, Record{Endpoint: "http://127.0.0.1:18766", PID: 4242, Dir: data}); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	procs := []procInfo{
		hostProc(self, 1, data),   // this process itself (paranoia)
		hostProc(700, self, data), // a host this process started
		hostProc(701, 700, data),  // its PTY child
		hostProc(4242, 1, data),   // the PID recorded as the live host
		hostProc(800, 1, data),    // a real leftover from a previous process
	}
	_, kills, _ := sweepWith(t, procs, data, false)
	// self, the host this process started and its PTY child are protected. The
	// PID recorded by a previous process is a real orphan (gentle kill), and so
	// is the unrelated leftover.
	if len(kills) != 2 {
		t.Fatalf("kills = %+v, want the recorded PID and the unrelated leftover", kills)
	}
	got := map[int]bool{kills[0].pid: kills[0].gentle, kills[1].pid: kills[1].gentle}
	if gentle, ok := got[4242]; !ok || !gentle {
		t.Fatalf("recorded PID was not gently reaped: %+v", kills)
	}
	if gentle, ok := got[800]; !ok || gentle {
		t.Fatalf("unrelated leftover should not get the gentle attempt: %+v", kills)
	}
	for _, protected := range []int{self, 700, 701} {
		if _, ok := got[protected]; ok {
			t.Fatalf("killed this process's family: %+v", kills)
		}
	}
}

func TestOrphanDryRunOnlyLogs(t *testing.T) {
	data := dataDirUnder(t, "data-d")
	swept, kills, logs := sweepWith(t, []procInfo{hostProc(900, 1, data)}, data, true)
	if len(kills) != 0 {
		t.Fatalf("dry run killed %+v", kills)
	}
	if swept.Candidates != 1 || swept.Reaped != 0 || !swept.DryRun {
		t.Fatalf("swept = %+v", swept)
	}
	if !logs.has("host_event=orphan_candidate") {
		t.Fatalf("no candidate log:\n%s", logs)
	}
	if logs.has("host_event=orphan_reaped") {
		t.Fatalf("dry run logged a kill:\n%s", logs)
	}
}

// The recorded live host is asked to exit before it is force-killed.
func TestOrphanRecordedPIDGetsGracefulKillFirst(t *testing.T) {
	data := dataDirUnder(t, "data-e")
	if err := WriteRecord(data, Record{Endpoint: "http://127.0.0.1:18766", PID: 4242, Dir: data}); err != nil {
		t.Fatal(err)
	}
	_, kills, _ := sweepWith(t, []procInfo{hostProc(4242, 1, data)}, data, false)
	if len(kills) != 1 || kills[0].pid != 4242 || !kills[0].gentle {
		t.Fatalf("kills = %+v, want a graceful attempt for the recorded PID", kills)
	}
}

func TestParseProcessJSON(t *testing.T) {
	one, err := parseProcessJSON([]byte(`{"ProcessId":11,"ParentProcessId":1,"Name":"aiscan.exe","CommandLine":"x"}`))
	if err != nil || len(one) != 1 || one[0].PID != 11 || one[0].Name != "aiscan.exe" {
		t.Fatalf("single = %+v err=%v", one, err)
	}
	many, err := parseProcessJSON([]byte(`[{"ProcessId":12},{"ProcessId":13}]`))
	if err != nil || len(many) != 2 || many[1].PID != 13 {
		t.Fatalf("array = %+v err=%v", many, err)
	}
	if none, err := parseProcessJSON(nil); err != nil || len(none) != 0 {
		t.Fatalf("empty = %+v err=%v", none, err)
	}
}

func TestHarnessNameAndBoundary(t *testing.T) {
	for _, name := range []string{"aiscan.exe", "AISCAN-FULL.EXE", `C:\tools\aiscan-full.exe`, "aiscan"} {
		if !isHarnessName(name) {
			t.Fatalf("%q should be a harness name", name)
		}
	}
	for _, name := range []string{"notepad.exe", "aiscan-helper.exe", ""} {
		if isHarnessName(name) {
			t.Fatalf("%q is not a harness host", name)
		}
	}
	data := filepath.Join("C:\\", "instances", "data-a")
	if !commandBelongsTo(`--db C:\instances\data-a\host\cyber-web.db`, data) {
		t.Fatal("a command line naming the data dir was rejected")
	}
	if commandBelongsTo(`--db C:\instances\data-ab\host\cyber-web.db`, data) {
		t.Fatal("a sibling instance (data-ab) matched data-a")
	}
	if !strings.Contains(`--config "C:\instances\data-a\host\profiles\x.yaml"`, "data-a") {
		t.Fatal("test fixture broken")
	}
}
