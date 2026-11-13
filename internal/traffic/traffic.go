// Package traffic brings recorded HTTP traffic into the ledger.
//
// The recording itself is not our job: browsers, Burp, mitmproxy and the
// harness all produce HAR. What the ledger needs is the *account* — which hosts
// spoke, on what ports, with what status distribution — attached to evidence
// that still exists. So this package imports HAR rather than running a proxy
// (docs/09-traffic.md).
package traffic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// MaxEntries caps how much of one recording is summarised. A capture of a
// browsing session can be tens of thousands of entries; the ledger wants a
// shape, not a transcript.
const MaxEntries = 5000

// MaxHosts caps derived service facts, so one noisy capture cannot flood the
// board.
const MaxHosts = 200

// MaxFileSize refuses absurd inputs early.
const MaxFileSize = 128 << 20

// HAR is the subset of HTTP Archive 1.2 the ledger uses.
type HAR struct {
	Log struct {
		Version string `json:"version"`
		Creator struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"creator"`
		Entries []Entry `json:"entries"`
	} `json:"log"`
}

// Entry is one request/response pair.
type Entry struct {
	StartedDateTime string `json:"startedDateTime"`
	Request         struct {
		Method  string `json:"method"`
		URL     string `json:"url"`
		Headers []NameValue
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Content struct {
			MimeType string `json:"mimeType"`
			Size     int64  `json:"size"`
		} `json:"content"`
	} `json:"response"`
	ServerIPAddress string `json:"serverIPAddress"`
}

// NameValue is a header pair; only the name is interesting for host facts.
type NameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Parse reads one HAR file.
func Parse(path string) (HAR, error) {
	var h HAR
	st, err := os.Stat(path)
	if err != nil {
		return h, err
	}
	if st.IsDir() {
		return h, fmt.Errorf("%s 是目录", path)
	}
	if st.Size() > MaxFileSize {
		return h, fmt.Errorf("文件过大（%d 字节），上限 %d", st.Size(), MaxFileSize)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, fmt.Errorf("不是合法的 HAR：%w", err)
	}
	if len(h.Log.Entries) == 0 {
		return h, fmt.Errorf("HAR 里没有请求（log.entries 为空）")
	}
	return h, nil
}

// Host is the per-origin summary derived from a capture.
type Host struct {
	Key       string // host:port
	Requests  int
	Methods   map[string]int
	Statuses  map[int]int
	Paths     []string
	IP        string
	FirstSeen time.Time
}

// Summarise groups entries by host:port. The order is stable so re-importing
// the same capture produces the same fact titles.
func Summarise(h HAR) []Host {
	byKey := map[string]*Host{}
	for i, e := range h.Log.Entries {
		if i >= MaxEntries {
			break
		}
		u, err := url.Parse(strings.TrimSpace(e.Request.URL))
		if err != nil || u.Host == "" {
			continue
		}
		key := strings.ToLower(u.Host)
		host := byKey[key]
		if host == nil {
			host = &Host{Key: key, Methods: map[string]int{}, Statuses: map[int]int{}}
			byKey[key] = host
		}
		host.Requests++
		host.Methods[strings.ToUpper(strings.TrimSpace(e.Request.Method))]++
		if e.Response.Status > 0 {
			host.Statuses[e.Response.Status]++
		}
		if e.ServerIPAddress != "" && host.IP == "" {
			host.IP = e.ServerIPAddress
		}
		if len(host.Paths) < 20 {
			path := u.Path
			if path == "" {
				path = "/"
			}
			host.Paths = append(host.Paths, path)
		}
		if host.FirstSeen.IsZero() {
			if t, err := time.Parse(time.RFC3339, e.StartedDateTime); err == nil {
				host.FirstSeen = t
			}
		}
	}
	out := make([]Host, 0, len(byKey))
	for _, h := range byKey {
		sort.Strings(h.Paths)
		out = append(out, *h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Result reports what one import actually did.
type Result struct {
	IntentID   string `json:"intent_id"`
	EvidenceID string `json:"evidence_id"`
	Entries    int    `json:"entries"`
	Hosts      int    `json:"hosts"`
	Facts      int    `json:"facts"` // service facts created (new hosts only)
}

// Importer writes one capture into the ledger.
type Importer struct {
	Store    *store.Store
	CaseRoot string
}

// Import copies the capture into the case as evidence and derives one service
// fact per host that the ledger does not already know.
//
// The capture is attached to a synthetic intent: facts must be traceable to a
// dispatch (docs/02 invariant 2), and inventing a hidden owner would be worse
// than showing an honest "流量导入" row in the queue.
func (im *Importer) Import(path string) (Result, error) {
	// A relative path is resolved against the case root, so the caller never has
	// to paste a machine-specific absolute path (docs/09-traffic.md).
	path = im.resolve(path)
	h, err := Parse(path)
	if err != nil {
		return Result{}, err
	}

	hosts := Summarise(h)
	if len(hosts) > MaxHosts {
		hosts = hosts[:MaxHosts]
	}

	in, err := im.Store.CreateIntent(model.Intent{
		Title:       "流量导入 " + time.Now().Format("01-02 15:04"),
		Description: fmt.Sprintf("把 %s 的 %d 条请求导入事实意图图", filepath.Base(path), len(h.Log.Entries)),
		Doc:         fmt.Sprintf("来源：%s · HAR %s · 捕获工具 %s %s", path, h.Log.Version, h.Log.Creator.Name, h.Log.Creator.Version),
	})
	if err != nil {
		return Result{}, err
	}
	if _, err := im.Store.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		return Result{}, err
	}

	workDir := filepath.Join(im.CaseRoot, in.ID, "traffic")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Result{}, err
	}
	dst := filepath.Join(workDir, filepath.Base(path))
	if err := copyFile(path, dst); err != nil {
		return Result{}, err
	}
	// The case copy is a convenience; the evidence address is the preserved
	// object, so re-importing or overwriting the capture cannot change what an
	// already-admitted import proves (F2/D150). Re-importing the same bytes
	// reuses the object; different bytes get their own object and row.
	preserved, err := evidence.Preserve(im.CaseRoot, dst)
	if err != nil {
		return Result{}, fmt.Errorf("保全流量捕获失败：%w", err)
	}
	ev, err := im.Store.AddEvidence(model.Evidence{
		IntentID:  in.ID,
		Name:      filepath.Base(dst),
		Mime:      "application/json",
		Size:      preserved.Size,
		URI:       preserved.URI,
		Digest:    preserved.Digest,
		SourceURI: evidence.OriginPath(im.CaseRoot, path),
	})
	if err != nil {
		return Result{}, err
	}

	res := Result{IntentID: in.ID, EvidenceID: ev.ID, Entries: len(h.Log.Entries), Hosts: len(hosts)}

	// One service fact per new host. Existing titles are skipped so importing
	// the same capture twice does not duplicate the board.
	known := map[string]bool{}
	for _, f := range im.Store.ListFacts("", 0) {
		known[strings.ToLower(f.Title)] = true
	}
	for _, host := range hosts {
		if known[strings.ToLower(host.Key)] {
			continue
		}
		if _, err := im.Store.AddFact(model.Fact{
			IntentID:    in.ID,
			Kind:        "service",
			Title:       host.Key,
			Summary:     summariseHost(host),
			Doc:         docForHost(host),
			Confidence:  "observed",
			Origin:      model.OriginDerived,
			EvidenceIDs: []string{ev.ID},
		}); err != nil {
			return res, err
		}
		known[strings.ToLower(host.Key)] = true
		res.Facts++
	}

	if _, err := im.Store.AddFact(model.Fact{
		IntentID:    in.ID,
		Kind:        "note",
		Title:       "流量导入",
		Summary:     fmt.Sprintf("%d 条请求 / %d 个主机", res.Entries, res.Hosts),
		Doc:         docForImport(hosts),
		Confidence:  "observed",
		Origin:      model.OriginDerived,
		EvidenceIDs: []string{ev.ID},
	}); err != nil {
		return res, err
	}
	return res, nil
}

// resolve turns a case-relative path into an absolute one.
func (im *Importer) resolve(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(im.CaseRoot, filepath.FromSlash(path))
}

func summariseHost(h Host) string {
	parts := []string{fmt.Sprintf("%d 个请求", h.Requests)}
	if m := topMethods(h.Methods, 3); m != "" {
		parts = append(parts, "方法 "+m)
	}
	if s := topStatuses(h.Statuses, 3); s != "" {
		parts = append(parts, "状态 "+s)
	}
	return strings.Join(parts, " · ")
}

func docForHost(h Host) string {
	var b strings.Builder
	b.WriteString("捕获到的接口：\n")
	for _, p := range h.Paths {
		b.WriteString("- " + p + "\n")
	}
	if h.IP != "" {
		b.WriteString("\n服务端 IP：" + h.IP + "\n")
	}
	b.WriteString("\n状态码分布：")
	b.WriteString(statusLine(h.Statuses))
	b.WriteString("\n\n注意：这是被动观察到的请求，不代表已测过这些接口。")
	return b.String()
}

func docForImport(hosts []Host) string {
	var b strings.Builder
	b.WriteString("按请求数排序的主机：\n")
	for i, h := range hosts {
		if i >= 30 {
			b.WriteString(fmt.Sprintf("……其余 %d 个\n", len(hosts)-30))
			break
		}
		b.WriteString("- " + h.Key + "：" + summariseHost(h) + "\n")
	}
	return b.String()
}

func statusLine(m map[int]int) string {
	if len(m) == 0 {
		return "（无）"
	}
	type kv struct {
		code int
		n    int
	}
	var list []kv
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].code < list[j].code
	})
	var parts []string
	for i, e := range list {
		if i >= 6 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%d×%d", e.code, e.n))
	}
	return strings.Join(parts, " ")
}

func topMethods(m map[string]int, n int) string {
	var names []string
	for k := range m {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if m[names[i]] != m[names[j]] {
			return m[names[i]] > m[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > n {
		names = names[:n]
	}
	return strings.Join(names, ",")
}

func topStatuses(m map[int]int, n int) string {
	return statusLine(m)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
