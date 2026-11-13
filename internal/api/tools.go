package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ToolStat is one row of the tool-call leaderboard. Calls are counted from the
// run logs (kind=tool_call), not from facts: a call that produced nothing still
// cost a turn, and that is exactly what the ranking is for.
type ToolStat struct {
	Tool   string     `json:"tool"`
	Calls  int        `json:"calls"`
	Runs   int        `json:"runs"`
	LastAt *time.Time `json:"last_at,omitempty"`
}

// ArsenalTool is one binary found in an arsenal bin directory, with the
// version/managed_by fields from that directory's manifest.json when present.
// Dirs lists every arsenal root that holds the same binary: an operator may
// have installed it both instance-wide and for the local user, and the page
// should say so once, not twice.
type ArsenalTool struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Size        int64    `json:"size,omitempty"`
	Source      string   `json:"source,omitempty"`
	ManagedBy   string   `json:"managed_by,omitempty"`
	InstalledAt string   `json:"installed_at,omitempty"`
	Dirs        []string `json:"dirs,omitempty"`
}

type toolsResponse struct {
	Leaderboard []ToolStat    `json:"leaderboard"`
	RunsScanned int           `json:"runs_scanned"`
	Arsenal     []ArsenalTool `json:"arsenal"`
	ArsenalDirs []string      `json:"arsenal_dirs,omitempty"`
}

// toolFile is one run log's contribution, cached until the file changes.
type toolFile struct {
	size  int64
	mod   time.Time
	calls map[string]int
	last  map[string]time.Time
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	rows, scanned := s.toolLeaderboard()
	arsenal, dirs := s.arsenalTools()
	writeJSON(w, http.StatusOK, toolsResponse{
		Leaderboard: rows,
		RunsScanned: scanned,
		Arsenal:     arsenal,
		ArsenalDirs: dirs,
	})
}

// toolLeaderboard aggregates tool_call events over every run log of the
// project. A run log is re-read only when its size or mtime changed, so the
// page stays cheap after the first load even with large JSONL files.
func (s *Server) toolLeaderboard() ([]ToolStat, int) {
	dir := filepath.Join(s.DataDir, "runs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}

	type tally struct {
		calls int
		runs  int
		last  time.Time
	}
	byTool := map[string]*tally{}
	scanned := 0

	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		scanned++

		s.mu.Lock()
		cached, ok := s.toolCache[path]
		s.mu.Unlock()
		if !ok || cached.size != st.Size() || !cached.mod.Equal(st.ModTime()) {
			cached = readToolCalls(path, st)
			s.mu.Lock()
			s.toolCache[path] = cached
			s.mu.Unlock()
		}

		for tool, n := range cached.calls {
			t := byTool[tool]
			if t == nil {
				t = &tally{}
				byTool[tool] = t
			}
			t.calls += n
			t.runs++
			if at := cached.last[tool]; at.After(t.last) {
				t.last = at
			}
		}
	}

	out := make([]ToolStat, 0, len(byTool))
	for tool, t := range byTool {
		row := ToolStat{Tool: tool, Calls: t.calls, Runs: t.runs}
		if !t.last.IsZero() {
			at := t.last
			row.LastAt = &at
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Tool < out[j].Tool
	})
	return out, scanned
}

// readToolCalls reads one run log and keeps only what the leaderboard needs.
func readToolCalls(path string, st os.FileInfo) toolFile {
	out := toolFile{
		size:  st.Size(),
		mod:   st.ModTime(),
		calls: map[string]int{},
		last:  map[string]time.Time{},
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 1<<20)
	for {
		line, readErr := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			var ev struct {
				Kind string    `json:"kind"`
				Tool string    `json:"tool"`
				At   time.Time `json:"at"`
			}
			if json.Unmarshal([]byte(trimmed), &ev) == nil && ev.Kind == "tool_call" && ev.Tool != "" {
				out.calls[ev.Tool]++
				if ev.At.After(out.last[ev.Tool]) {
					out.last[ev.Tool] = ev.At
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return out
}

// arsenalTools lists what arsenal installed on this machine. The instance-level
// directory (<data>/arsenal, docs/02) and the user-level one the local agent
// actually resolves (~/.cyber/arsenal) are both reported: an operator who drops
// a binary in one of them should see it, and the page says which one.
func (s *Server) arsenalTools() ([]ArsenalTool, []string) {
	roots := s.ArsenalDirs
	if len(roots) == 0 {
		roots = defaultArsenalDirs(s.DataDir)
	}

	seen := map[string]bool{}
	byName := map[string][]ArsenalTool{}
	var dirs []string
	for _, root := range roots {
		bin := filepath.Join(root, "bin")
		st, err := os.Stat(bin)
		if err != nil || !st.IsDir() || seen[bin] {
			continue
		}
		seen[bin] = true
		dirs = append(dirs, bin)

		manifest := readArsenalManifest(filepath.Join(root, "manifest.json"))
		ents, err := os.ReadDir(bin)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() || strings.HasPrefix(name, ".") {
				continue
			}
			name = strings.TrimSuffix(strings.TrimSuffix(name, ".exe"), ".EXE")
			info, err := ent.Info()
			if err != nil {
				continue
			}
			row := ArsenalTool{Name: name, Size: info.Size(), Dirs: []string{bin}}
			if m, ok := manifest[name]; ok {
				row.Version = m.Version
				row.Source = m.Source
				row.ManagedBy = m.ManagedBy
				row.InstalledAt = m.InstalledAt
			}
			byName[name] = append(byName[name], row)
		}
	}

	out := make([]ArsenalTool, 0, len(byName))
	for _, rows := range byName {
		pick := rows[0]
		var where []string
		for _, row := range rows {
			where = append(where, row.Dirs[0])
			switch {
			case pick.Version == "" && row.Version != "":
				pick = row // prefer the copy the manifest describes
			case row.Version == pick.Version && row.Size > pick.Size:
				pick = row // a stale 1-byte placeholder must not be the headline
			}
		}
		// Show the directory of the copy the page is describing first.
		for i, dir := range where {
			if dir == pick.Dirs[0] {
				where[0], where[i] = where[i], where[0]
				break
			}
		}
		pick.Dirs = where
		out = append(out, pick)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, dirs
}

type arsenalManifestEntry struct {
	Version     string `json:"version"`
	InstalledAt string `json:"installed_at"`
	Source      string `json:"source"`
	ManagedBy   string `json:"managed_by"`
}

func readArsenalManifest(path string) map[string]arsenalManifestEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]arsenalManifestEntry
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

// defaultArsenalDirs: DataDir is <data>/projects/<id>, so the instance tools
// directory is two levels up; the second entry is what a local agent resolves
// when the case has no project .cyber of its own.
func defaultArsenalDirs(dataDir string) []string {
	instance := filepath.Dir(filepath.Dir(dataDir))
	out := []string{filepath.Join(instance, "arsenal")}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".cyber", "arsenal"))
	}
	return out
}
