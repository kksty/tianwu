package kb

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrConflict = errors.New("知识已变化，请刷新后重新提名或编辑")

// Candidate is an explicit nomination, never a fact inferred from a transcript.
// Origin stays in the instance; it is not included in distributable entries.
type Candidate struct {
	ID           string    `json:"id"`
	Version      int       `json:"version"`
	Entry        Entry     `json:"entry"`
	Reason       string    `json:"reason"`
	Origin       string    `json:"origin,omitempty"`
	BaseRevision string    `json:"base_revision"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Published    bool      `json:"published"`
}

func cloneEntries(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	for i := range out {
		out[i].Commands = append([]string(nil), out[i].Commands...)
		out[i].Tags = append([]string(nil), out[i].Tags...)
		out[i].SourceRefs = append([]string(nil), out[i].SourceRefs...)
		if out[i].UpdatedAt != nil {
			value := *out[i].UpdatedAt
			out[i].UpdatedAt = &value
		}
	}
	return out
}

func Revision(e Entry) string {
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Revisions binds each edit token to the exact snapshot returned to its reader.
func Revisions(entries []Entry) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.ID] = Revision(e)
	}
	return out
}

func published(c Candidate, e Entry) bool {
	if e.Promotion != promotion(c) {
		return false
	}
	clear := func(e Entry) Entry { e.Promotion = ""; e.CreatedAt = time.Time{}; e.UpdatedAt = nil; return e }
	return Revision(clear(e)) == Revision(clear(c.Entry))
}

func validID(id string) bool {
	if id == "" || len(id) > 120 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validateEntry(e Entry) error {
	if e.Deleted {
		return fmt.Errorf("删除标记不能作为知识候选")
	}
	if !validID(e.ID) {
		return fmt.Errorf("知识 ID 只能包含小写字母、数字、短横线和下划线，最长 120 字符")
	}
	if strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Description) == "" || strings.TrimSpace(e.Detail) == "" {
		return fmt.Errorf("标题、摘要和事实正文不能为空")
	}
	if strings.TrimSpace(e.Source) == "" && len(e.SourceRefs) == 0 {
		return fmt.Errorf("请注明知识来源")
	}
	for _, ref := range e.SourceRefs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("来源引用不能为空")
		}
	}
	return nil
}

func findEntry(entries []Entry, id string) (Entry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Reload replaces the shared snapshot only after every file parses. Callers
// keep the previous working library when an external edit is incomplete.
func (l *Library) Reload() error {
	l.editMu.Lock()
	defer l.editMu.Unlock()
	next, err := open(filepath.Dir(l.dir), true)
	if err != nil {
		return err
	}
	l.replace(next.entries)
	return nil
}

func (l *Library) replace(entries []Entry) {
	l.mu.Lock()
	l.entries = cloneEntries(entries)
	l.mu.Unlock()
}

func (l *Library) candidatePath(id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("无效候选 ID")
	}
	return filepath.Join(filepath.Dir(l.dir), "candidates", id+".json"), nil
}

func (l *Library) Candidates() ([]Candidate, error) {
	l.editMu.Lock()
	defer l.editMu.Unlock()
	out := []Candidate{}
	dir := filepath.Join(filepath.Dir(l.dir), "candidates")
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	entries := l.Entries()
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return nil, err
		}
		var c Candidate
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		e, _ := findEntry(entries, c.Entry.ID)
		c.Published = published(c, e)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func promotion(c Candidate) string { return c.ID + ":" + strconv.Itoa(c.Version) }

func (l *Library) readCandidate(id string) (Candidate, error) {
	p, err := l.candidatePath(id)
	if err != nil {
		return Candidate{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Candidate{}, err
	}
	var c Candidate
	err = json.Unmarshal(b, &c)
	return c, err
}

func (l *Library) SaveCandidate(c Candidate) (Candidate, error) {
	l.editMu.Lock()
	defer l.editMu.Unlock()
	if err := validateEntry(c.Entry); err != nil {
		return Candidate{}, err
	}
	if strings.TrimSpace(c.Reason) == "" {
		return Candidate{}, fmt.Errorf("请说明为什么这条知识可以跨项目使用")
	}
	now := time.Now().UTC()
	if c.ID == "" {
		var b [12]byte
		if _, err := rand.Read(b[:]); err != nil {
			return Candidate{}, err
		}
		c.ID, c.Version, c.CreatedAt = "kbc-"+hex.EncodeToString(b[:]), 1, now
		if e, ok := findEntry(l.snapshotEntries(), c.Entry.ID); ok {
			if c.BaseRevision != Revision(e) {
				return Candidate{}, ErrConflict
			}
		} else if c.BaseRevision != "" {
			return Candidate{}, ErrConflict
		}
	} else {
		old, err := l.readCandidate(c.ID)
		if err != nil {
			return Candidate{}, err
		}
		if c.Version != old.Version {
			return Candidate{}, ErrConflict
		}
		if c.Entry.ID != old.Entry.ID {
			return Candidate{}, fmt.Errorf("已有候选的知识 ID 不能改变，请新建候选")
		}
		c.Version, c.CreatedAt, c.Origin = old.Version+1, old.CreatedAt, old.Origin
		c.BaseRevision = old.BaseRevision
		// Editing a published draft starts a revision against its published entry.
		if e, ok := findEntry(l.Entries(), c.Entry.ID); ok && published(old, e) {
			c.BaseRevision = Revision(e)
		}
	}
	c.UpdatedAt, c.Published = now, false
	c.Entry.Promotion = ""
	p, _ := l.candidatePath(c.ID)
	return c, writeJSONAtomic(p, c)
}

// Publish uses the nominated base revision, so two drafts cannot silently
// overwrite each other. A repeated request is idempotent even after a restart.
func (l *Library) Publish(id string, version int) (Entry, error) {
	l.editMu.Lock()
	defer l.editMu.Unlock()
	c, err := l.readCandidate(id)
	if err != nil {
		return Entry{}, err
	}
	if c.Version != version {
		return Entry{}, ErrConflict
	}
	if err := validateEntry(c.Entry); err != nil {
		return Entry{}, err
	}
	next, err := open(filepath.Dir(l.dir), true)
	if err != nil {
		return Entry{}, err
	}
	old, exists := findEntry(next.entries, c.Entry.ID)
	if exists && published(c, old) {
		l.installEntry(old)
		return old, nil
	}
	base := ""
	if exists {
		base = Revision(old)
	}
	if base != c.BaseRevision {
		return Entry{}, ErrConflict
	}
	e := c.Entry
	now := time.Now().UTC()
	e.Promotion, e.UpdatedAt = promotion(c), &now
	if exists && !old.Deleted {
		e.CreatedAt = old.CreatedAt
	} else {
		e.CreatedAt = now
	}
	target, err := l.entryPath(e.ID)
	if err != nil {
		return Entry{}, err
	}
	if err := writeJSONAtomic(target, e); err != nil {
		return Entry{}, err
	}
	l.installEntry(e)
	return e, nil
}

// Existing local entries may use a filename different from their stable ID.
// Resolve ownership before either publishing or hiding an entry.
func (l *Library) entryPath(id string) (string, error) {
	canonical := filepath.Join(l.dir, id+".json")
	target := canonical
	collision := false
	files, err := os.ReadDir(l.dir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, file := range files {
		if file.IsDir() || !strings.EqualFold(filepath.Ext(file.Name()), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(l.dir, file.Name()))
		if err != nil {
			return "", err
		}
		var local Entry
		if err := json.Unmarshal(b, &local); err != nil {
			return "", err
		}
		if local.ID == "" {
			local.ID = strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
		}
		if local.ID == id {
			target = filepath.Join(l.dir, file.Name())
			break
		}
		if strings.EqualFold(file.Name(), id+".json") {
			collision = true
		}
	}
	if target == canonical && collision {
		return "", fmt.Errorf("%w：目标文件已属于另一条知识", ErrConflict)
	}
	return target, nil
}

// Called under editMu. Publishing is not Reload: unrelated disk changes stay
// outside the shared snapshot until an explicit successful reload.
func (l *Library) installEntry(e Entry) {
	entries := l.snapshotEntries()
	replaced := false
	for i := range entries {
		if entries[i].ID == e.ID {
			entries[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	l.replace(entries)
}

func writeJSONAtomic(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".kb-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Export writes repository-ready assets, never instance nominations or origin.
// It deliberately does not write into an arbitrary server filesystem path.
func (l *Library) Export(w io.Writer, ids []string) error {
	entries := []Entry{}
	removed := []string{}
	for _, e := range l.snapshotEntries() {
		if e.Deleted {
			if !validID(e.ID) {
				return fmt.Errorf("无效删除 ID：%s", e.ID)
			}
			if len(ids) == 0 {
				removed = append(removed, e.ID)
			}
			continue
		}
		entries = append(entries, e)
	}
	selected := make(map[string]bool)
	for _, id := range ids {
		if _, ok := findEntry(entries, id); !ok {
			return fmt.Errorf("知识不存在：%s", id)
		}
		selected[id] = true
	}
	z := zip.NewWriter(w)
	exported := []string{}
	for _, e := range entries {
		if len(selected) > 0 && !selected[e.ID] {
			continue
		}
		if !validID(e.ID) {
			return fmt.Errorf("无效知识 ID：%s", e.ID)
		}
		if err := validateEntry(e); err != nil {
			return fmt.Errorf("知识 %s 不能导出：%w", e.ID, err)
		}
		e.Promotion = ""
		exported = append(exported, e.ID)
		b, err := json.MarshalIndent(e, "", "  ")
		if err != nil {
			return err
		}
		f, err := z.Create("internal/kb/assets/entries/" + e.ID + ".json")
		if err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	manifest, err := json.MarshalIndent(ExportManifest{Format: 1, Entries: exported, DeletedIDs: removed}, "", "  ")
	if err != nil {
		return err
	}
	f, err := z.Create("kb-export-manifest.json")
	if err != nil {
		return err
	}
	if _, err := f.Write(manifest); err != nil {
		return err
	}
	f, err = z.Create("KB_IMPORT.md")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, "# 世界知识导入\n\n将包保存在仓库之外，使用仓库脚本导入：\n\n```powershell\npwsh -File .\\scripts\\import-kb.ps1 -Archive <包的绝对路径>\n```\n\n全库包的 kb-export-manifest.json 记录删除的 ID。确认要从内置库移除这些条目时，加 -ApplyDeletions。脚本只修改 internal/kb/assets/entries 下的 JSON，不操作 Git、不构建或启动服务。\n\n随后检查差异、测试，再手动 git add internal/kb/assets/entries、commit 和 push；重新构建 tw 后成为内置。\n"); err != nil {
		return err
	}
	return z.Close()
}
