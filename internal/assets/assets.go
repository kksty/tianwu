// Package assets imports the harness's own scan results.
//
// 资产库不另起一套：cyber-harness 的 gogo / neutron 已经把主机与端口扫成结构化结果
// （GOGOResult：ip / port / protocol / status / uri / host / title / midware /
// frameworks / vulns / extracted），我们照它的输出格式收，落成 service 与 host 事实
// 加一条证据（docs/10-assets.md）。
//
// 这里只读它的 JSONL，不 import 它的包：`chainreactors/cyber` 是独立模块 + internal +
// 352 个依赖（D29 实测），而我们要保住"纯标准库、一条命令交叉编译"。
package assets

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/ledger"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// MaxRecords caps one import; a full /16 sweep is not one ledger entry.
const MaxRecords = 2000

// MaxHosts caps derived host facts.
const MaxHosts = 500

// Record is one line of the harness's result stream.
type Record struct {
	IP         string `json:"ip"`
	Port       string `json:"port"`
	Protocol   string `json:"protocol"`
	Status     string `json:"status"`
	URI        string `json:"uri"`
	Host       string `json:"host"`
	Title      string `json:"title"`
	Midware    string `json:"midware"`
	Frameworks []struct {
		Name       string `json:"name"`
		Attributes struct {
			Version string `json:"version"`
		} `json:"attributes"`
	} `json:"frameworks"`
	Vulns []struct {
		Name string `json:"name"`
	} `json:"vulns"`
	Extracted map[string][]string `json:"extracted"`
}

// Version returns a fingerprint name with its version when the harness knows one.
func (r Record) Version() string {
	var parts []string
	for _, f := range r.Frameworks {
		if f.Name == "" {
			continue
		}
		if v := strings.TrimSpace(f.Attributes.Version); v != "" {
			parts = append(parts, f.Name+"/"+v)
			continue
		}
		parts = append(parts, f.Name)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// Key is the host:port identity used to dedupe facts.
func (r Record) Key() string {
	if r.IP == "" || r.Port == "" {
		return ""
	}
	return strings.ToLower(r.IP + ":" + r.Port)
}

// ParseJSONL reads the harness result stream: one JSON object per line. A JSON
// array is accepted too, because hand-cut samples are usually arrays.
func ParseJSONL(path string) ([]Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return nil, fmt.Errorf("文件是空的")
	}
	if strings.HasPrefix(text, "[") {
		var list []Record
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return nil, fmt.Errorf("不是合法的结果数组：%w", err)
		}
		return usable(list), nil
	}

	var out []Record
	var bad int
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if len(out) >= MaxRecords {
			break
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			bad++
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有一条可用结果（%d 行无法解析）", bad)
	}
	return usable(out), nil
}

func usable(in []Record) []Record {
	out := make([]Record, 0, len(in))
	for _, r := range in {
		if r.Key() == "" {
			continue
		}
		if len(out) >= MaxRecords {
			break
		}
		out = append(out, r)
	}
	return out
}

// Result reports what one import did.
type Result struct {
	IntentID   string `json:"intent_id"`
	EvidenceID string `json:"evidence_id"`
	Records    int    `json:"records"`
	Hosts      int    `json:"hosts"`
	Facts      int    `json:"facts"`
}

// Importer writes one scan result stream into the ledger.
type Importer struct {
	Store    *store.Store
	CaseRoot string
}

// Import attaches the result file as evidence and derives one service fact per
// host:port plus one host fact per address.
func (im *Importer) Import(path string) (Result, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(im.CaseRoot, filepath.FromSlash(path))
	}
	records, err := ParseJSONL(path)
	if err != nil {
		return Result{}, err
	}

	att, err := ledger.AttachImport(im.Store, im.CaseRoot, path, "scan",
		"资产导入 "+time.Now().Format("01-02 15:04"),
		fmt.Sprintf("把 %s 的 %d 条扫描结果导入事实意图图", filepath.Base(path), len(records)))
	if err != nil {
		return Result{}, err
	}

	res := Result{IntentID: att.IntentID, EvidenceID: att.EvidenceID, Records: len(records)}

	known := map[string]bool{}
	for _, f := range im.Store.ListFacts("", 0) {
		known[strings.ToLower(f.Title)] = true
	}

	byHost := map[string][]Record{}
	var order []string
	for _, r := range records {
		if _, ok := byHost[r.IP]; !ok {
			order = append(order, r.IP)
		}
		byHost[r.IP] = append(byHost[r.IP], r)
	}
	sort.Strings(order)
	if len(order) > MaxHosts {
		order = order[:MaxHosts]
	}
	res.Hosts = len(order)

	for _, ip := range order {
		ports := byHost[ip]
		sort.SliceStable(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
		for _, r := range ports {
			key := r.Key()
			if known[key] {
				continue
			}
			if _, err := im.Store.AddFact(model.Fact{
				IntentID:    att.IntentID,
				Kind:        "service",
				Title:       key,
				Summary:     summarise(r),
				Doc:         docFor(r),
				Confidence:  "observed",
				Origin:      model.OriginDerived,
				EvidenceIDs: []string{att.EvidenceID},
			}); err != nil {
				return res, err
			}
			known[key] = true
			res.Facts++
		}

		if known[strings.ToLower(ip)] {
			continue
		}
		if _, err := im.Store.AddFact(model.Fact{
			IntentID:    att.IntentID,
			Kind:        "host",
			Title:       ip,
			Summary:     fmt.Sprintf("%d 个端口开放", len(ports)),
			Doc:         docForHost(ip, ports),
			Confidence:  "observed",
			Origin:      model.OriginDerived,
			EvidenceIDs: []string{att.EvidenceID},
		}); err != nil {
			return res, err
		}
		known[strings.ToLower(ip)] = true
		res.Facts++
	}

	if _, err := im.Store.AddFact(model.Fact{
		IntentID:    att.IntentID,
		Kind:        "note",
		Title:       "资产导入",
		Summary:     fmt.Sprintf("%d 条结果 / %d 个主机", res.Records, res.Hosts),
		Doc:         docForImport(order, byHost),
		Confidence:  "observed",
		Origin:      model.OriginDerived,
		EvidenceIDs: []string{att.EvidenceID},
	}); err != nil {
		return res, err
	}
	return res, nil
}

func summarise(r Record) string {
	var parts []string
	if r.Protocol != "" {
		parts = append(parts, r.Protocol)
	}
	if r.Status != "" {
		parts = append(parts, r.Status) //nolint:staticcheck // status is the harness's own field
	}
	if fw := r.Version(); fw != "" {
		parts = append(parts, fw)
	}
	if r.Title != "" {
		parts = append(parts, r.Title)
	}
	if len(r.Vulns) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个漏洞指纹", len(r.Vulns)))
	}
	return strings.Join(parts, " · ")
}

func docFor(r Record) string {
	var b strings.Builder
	if r.URI != "" {
		b.WriteString("URI：" + r.URI + "\n")
	}
	if r.Host != "" {
		b.WriteString("Host 头：" + r.Host + "\n")
	}
	if r.Title != "" {
		b.WriteString("标题：" + r.Title + "\n")
	}
	if r.Midware != "" {
		b.WriteString("中间件：" + r.Midware + "\n")
	}
	if fw := r.Version(); fw != "" {
		b.WriteString("指纹：" + fw + "\n")
	}
	if len(r.Vulns) > 0 {
		names := make([]string, 0, len(r.Vulns))
		for _, v := range r.Vulns {
			if v.Name != "" {
				names = append(names, v.Name)
			}
		}
		sort.Strings(names)
		b.WriteString("漏洞指纹：" + strings.Join(names, ", ") + "\n")
	}
	if len(r.Extracted) > 0 {
		var keys []string
		for k := range r.Extracted {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n提取到的字段：\n")
		for _, k := range keys {
			vals := r.Extracted[k]
			if len(vals) > 5 {
				vals = vals[:5]
			}
			b.WriteString(fmt.Sprintf("- %s：%s\n", k, strings.Join(vals, ", ")))
		}
	}
	b.WriteString("\n这是扫描器看到的结果，不代表已经验证过这些服务的安全问题（D30）。")
	return b.String()
}

func docForHost(ip string, ports []Record) string {
	var b strings.Builder
	b.WriteString(ip + " 上开放的端口：\n")
	for _, r := range ports {
		line := "- " + r.Port
		if r.Protocol != "" {
			line += " " + r.Protocol
		}
		if r.Title != "" {
			line += " · " + r.Title
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func docForImport(order []string, byHost map[string][]Record) string {
	var b strings.Builder
	for i, ip := range order {
		if i >= 30 {
			b.WriteString(fmt.Sprintf("……其余 %d 个主机\n", len(order)-30))
			break
		}
		b.WriteString(fmt.Sprintf("- %s：%d 个端口\n", ip, len(byHost[ip])))
	}
	return b.String()
}
