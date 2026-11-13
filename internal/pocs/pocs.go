// Package pocs owns the POC template directory shared with neutron.
//
// neutron 的模板 schema 就是 nuclei 的（见 docs/12-extend.md 的源码验证），所以
// 导入 nuclei 模板**不需要转换**：把文件放进目录，跑 neutron 时用 `-t <dir>` 指过去。
// 这里只做三件事：校验最小字段、去重、落盘。
package pocs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Poc is one imported template, as the console sees it.
type Poc struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Severity string `json:"severity,omitempty"`
	Tags     string `json:"tags,omitempty"`
	File     string `json:"file"`
	Size     int64  `json:"size"`
}

// Store is a directory of YAML templates.
//
// A full community library is thousands of files, and the list is read on every
// console open as well as on every dispatch (the brief carries the directory
// only when the library is non-empty). Parsing all of them per read made the
// POC page wait on the filesystem and dominated dispatch latency, so List keeps
// the parsed result and recomputes it only when the directory signature changes.
type Store struct {
	Dir string

	mu    sync.Mutex
	cache []Poc
	sig   string
}

func New(dir string) *Store { return &Store{Dir: dir} }

// List reads the directory. A file that lost its id is still listed, by name,
// so a hand-edited template does not silently disappear from the console.
//
// The directory is stat-ed on every call (cheap) and its contents are parsed
// only when a name/size/mtime changed; Import invalidates the cache explicitly
// so an overwrite in the same timestamp tick is still seen.
func (s *Store) List() []Poc {
	if s == nil || s.Dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		s.invalidate()
		return nil
	}
	type fileEntry struct {
		name string
		size int64
		mod  int64
	}
	metas := make([]fileEntry, 0, len(entries))
	var sig strings.Builder
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		m := fileEntry{name: e.Name(), size: info.Size(), mod: info.ModTime().UnixNano()}
		metas = append(metas, m)
		fmt.Fprintf(&sig, "%s\x00%d\x00%d\n", m.name, m.size, m.mod)
	}
	key := sig.String()

	s.mu.Lock()
	if key == s.sig {
		out := make([]Poc, len(s.cache))
		copy(out, s.cache)
		s.mu.Unlock()
		return out
	}
	s.mu.Unlock()

	out := make([]Poc, 0, len(metas))
	for _, m := range metas {
		poc, err := parseFile(filepath.Join(s.Dir, m.name))
		if err != nil {
			continue
		}
		if poc.ID == "" {
			poc.ID = strings.TrimSuffix(m.name, filepath.Ext(m.name))
		}
		poc.File = m.name
		poc.Size = m.size
		out = append(out, poc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	s.mu.Lock()
	s.sig = key
	s.cache = out
	clone := make([]Poc, len(out))
	copy(clone, out)
	s.mu.Unlock()
	return clone
}

// invalidate drops the cached parse result. It runs after an import (same-id
// overwrites can land inside one filesystem timestamp tick) and when the
// directory itself became unreadable.
func (s *Store) invalidate() {
	s.mu.Lock()
	s.sig = ""
	s.cache = nil
	s.mu.Unlock()
}

// Page filters an already-listed library and returns one page of it.
//
// q matches id, name, tags and file name, case-insensitively. offset skips
// matched rows; limit<=0 means "no limit". matched is the number of rows the
// filter keeps before offset/limit, so the console can show "matched / total"
// without a second walk of the directory.
func Page(all []Poc, q string, offset, limit int) (page []Poc, matched int) {
	if offset < 0 {
		offset = 0
	}
	needle := strings.ToLower(strings.TrimSpace(q))
	page = make([]Poc, 0, len(all))
	for _, p := range all {
		if needle != "" && !matches(p, needle) {
			continue
		}
		matched++
		if matched <= offset {
			continue
		}
		if limit > 0 && len(page) >= limit {
			continue
		}
		page = append(page, p)
	}
	return page, matched
}

func matches(p Poc, needle string) bool {
	return strings.Contains(strings.ToLower(p.ID), needle) ||
		strings.Contains(strings.ToLower(p.Name), needle) ||
		strings.Contains(strings.ToLower(p.Tags), needle) ||
		strings.Contains(strings.ToLower(p.File), needle)
}

// ImportResult reports what one import did. Skipped entries carry a reason:
// silently dropping a template is how a POC library rots.
type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	IDs      []string `json:"ids,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// Import copies one template file or a directory tree of them.
func (s *Store) Import(path string) (ImportResult, error) {
	if s == nil || s.Dir == "" {
		return ImportResult{}, fmt.Errorf("POC 目录未配置")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return ImportResult{}, err
	}
	// A same-id overwrite can keep the same size and land inside one filesystem
	// timestamp tick, where the signature check in List would not notice it.
	defer s.invalidate()
	info, err := os.Stat(path)
	if err != nil {
		return ImportResult{}, err
	}

	var res ImportResult
	if !info.IsDir() {
		s.importOne(path, &res)
		return res, nil
	}
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isYAML(d.Name()) {
			return nil
		}
		s.importOne(p, &res)
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

func (s *Store) importOne(path string, res *ImportResult) {
	poc, err := parseFile(path)
	if err != nil || poc.ID == "" {
		res.Skipped++
		reason := "缺少 id 字段"
		if err != nil {
			reason = err.Error()
		}
		res.Errors = append(res.Errors, filepath.Base(path)+"："+reason)
		return
	}
	id := sanitizeID(poc.ID)
	if id == "" {
		res.Skipped++
		res.Errors = append(res.Errors, filepath.Base(path)+"：id 含非法字符")
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		res.Skipped++
		res.Errors = append(res.Errors, filepath.Base(path)+"："+err.Error())
		return
	}
	// 同一 id 覆盖：模板更新就是这样进来的。
	dst := filepath.Join(s.Dir, id+".yaml")
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		res.Skipped++
		res.Errors = append(res.Errors, filepath.Base(path)+"："+err.Error())
		return
	}
	res.Imported++
	res.IDs = append(res.IDs, id)
}

func isYAML(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			return ""
		}
	}
	return b.String()
}

// parseFile reads the handful of fields the console needs, without a YAML
// dependency. Only top-level `id` and keys inside the `info` block matter.
func parseFile(path string) (Poc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Poc{}, err
	}
	var poc Poc
	inInfo := false
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		if !indented {
			inInfo = trimmed == "info:"
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch {
		case !indented && key == "id" && poc.ID == "":
			poc.ID = value
		case inInfo && indented && key == "name" && poc.Name == "":
			poc.Name = value
		case inInfo && indented && key == "severity" && poc.Severity == "":
			poc.Severity = value
		case inInfo && indented && key == "tags" && poc.Tags == "":
			poc.Tags = value
		}
	}
	return poc, nil
}
