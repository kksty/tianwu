// Package skills ships the harness Skills that Tianwu hands to every executor.
//
// cyber-harness loads a Skill by name or by file path (`-s`), and resolves
// `.cyber/skills/<name>/SKILL.md` relative to the working directory. Tianwu
// embeds its own skills in the binary, writes them to the data dir at startup,
// and passes absolute paths — so the contract travels with the version instead
// of being re-pasted into every prompt (docs/06, D23).
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed assets
var assets embed.FS

// Report is the skill the report run receives. Executors get no mandatory
// skill: their handoff contract lives in the dispatch prompt, where it cannot
// fail to load (D32). Collaborate is different: it is discoverable content
// installed under .cyber/skills and read only when the executor needs it.
const Report = "tianwu-report"

// Collaborate documents the per-run participant API. Harness lists it in the
// system prompt by name/description; the body is read on demand, not pasted into
// every dispatch.
const Collaborate = "collaborate"

// Commander is the skill the commander session receives (D78). It documents the
// control-plane API the commander calls with its own tools; tianwu drops the
// file into the data dir and lets harness expand `/skill:commander` inside the
// session instead of pasting the body into every prompt.
const Commander = "commander"

// Ensure materialises the embedded skills under dir/<name>/SKILL.md and returns
// their absolute paths. It rewrites a file only when the content differs, so
// restarting the control plane does not churn the disk.
func Ensure(dir string) ([]string, error) {
	var out []string
	err := fs.WalkDir(assets, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), "assets/")
		data, err := assets.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(data) {
			if filepath.Base(dst) == "SKILL.md" {
				out = append(out, dst)
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		if filepath.Base(dst) == "SKILL.md" {
			out = append(out, dst)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Install exposes one embedded skill in a harness discovery directory
// (<dir>/<name>/SKILL.md). Harness loads `.cyber/skills` relative to its
// working directory, so a host started in <data> finds a skill dropped under
// <data>/.cyber/skills and a session can expand `/skill:<name>` itself — tianwu
// does not paste the body into the prompt (D78). Rewrites only on change.
func Install(name, dir string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill 名为空")
	}
	data, err := assets.ReadFile("assets/" + name + "/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("内置 skill %s：%w", name, err)
	}
	dst := filepath.Join(dir, name, "SKILL.md")
	if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(data) {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return dst, err
	}
	return dst, os.WriteFile(dst, data, 0o644)
}

// Resolve turns a list of names or paths into absolute SKILL.md paths.
//
// A bare name is looked up under base/<name>/SKILL.md (the extracted copy); a
// path is used as given. An explicit selection must resolve completely: a
// partial list would silently discard part of the operator's run contract.
func Resolve(list []string, base string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		p := item
		if !strings.ContainsAny(item, `/\\`) && !strings.HasSuffix(strings.ToLower(item), ".md") {
			p = filepath.Join(base, item, "SKILL.md")
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if seen[p] {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("读取技能 %s：%w", item, err)
		}
		if !st.Mode().IsRegular() || st.Size() == 0 {
			return nil, fmt.Errorf("技能文件为空或不是普通文件：%s", item)
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// Names lists the embedded skill names, for help text and diagnostics.
func Names() []string {
	var out []string
	entries, err := assets.ReadDir("assets")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
