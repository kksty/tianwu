// Package access owns operator-provided login material. It is project state,
// not evidence, and never makes an authentication or vulnerability claim.
package access

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/store"
)

const MaxMaterial = 2 << 20

type Entry struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"` // browser|state|request
	Origin    string        `json:"origin"`
	URL       string        `json:"url"`
	Account   string        `json:"account,omitempty"`
	Role      string        `json:"role,omitempty"`
	Tenant    string        `json:"tenant,omitempty"`
	Note      string        `json:"note,omitempty"`
	Session   string        `json:"session,omitempty"`
	Profile   string        `json:"profile,omitempty"`
	IntentID  string        `json:"intent_id,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	Lease     *BrowserLease `json:"lease,omitempty"`
}

type Input struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Origin    string          `json:"origin"`
	Account   string          `json:"account"`
	Role      string          `json:"role"`
	Tenant    string          `json:"tenant"`
	Note      string          `json:"note"`
	CDPURL    string          `json:"cdp_url"`
	Profile   string          `json:"profile"`
	Session   string          `json:"session"`
	IntentID  string          `json:"intent_id"`
	RequestID string          `json:"request_id"`
	Material  json.RawMessage `json:"material"`
}

type BrowserBinding struct {
	CDPURL          string `json:"cdp_url"`
	Session         string `json:"session"`
	Profile         string `json:"profile"`
	ExistingSession bool   `json:"existing_session,omitempty"`
	Connected       bool   `json:"connected,omitempty"`
}

// SaveBrowserBinding updates only connection metadata, preserving the resource
// and its lease. Callers serialize this with native operations.
func SaveBrowserBinding(cases, id string, binding BrowserBinding) error {
	dir, err := path(cases, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".binding-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "material.json"))
}

func root(cases string) string { return filepath.Join(cases, ".access") }
func path(cases, id string) (string, error) {
	if !strings.HasPrefix(id, "ac_") || strings.ContainsAny(id, "/\\:") || len(id) > 80 {
		return "", fmt.Errorf("无效的登录材料 id")
	}
	return filepath.Join(root(cases), id), nil
}

func Create(cases string, in Input) (Entry, error) {
	if len(in.Material) > MaxMaterial {
		return Entry{}, fmt.Errorf("登录材料超过 2 MiB")
	}
	in.Origin = strings.TrimSpace(in.Origin)
	in.CDPURL = strings.TrimSpace(in.CDPURL)
	u, err := url.Parse(in.Origin)
	cdpOnly := in.Kind == "browser" && in.CDPURL != "" && in.Origin == ""
	if !cdpOnly && (err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil) {
		return Entry{}, fmt.Errorf("适用站点必须是无内嵌凭据的 HTTP(S) 地址")
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = u.Host
		if cdpOnly {
			if cdp, err := url.Parse(in.CDPURL); err == nil {
				in.Name = "浏览器 · " + cdp.Host
			}
		}
	}
	if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 100 || len([]rune(in.Note)) > 2000 {
		return Entry{}, fmt.Errorf("名称应为 1–100 字（留空自动生成），备注不超过 2000 字")
	}
	for _, value := range []string{in.Name, in.Account, in.Role, in.Tenant, in.Profile, in.CDPURL, in.Origin, in.Session, in.IntentID} {
		if len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
			return Entry{}, fmt.Errorf("登录材料标签或地址包含换行或过长")
		}
	}
	for _, value := range []string{in.Account, in.Role, in.Tenant, in.Profile} {
		if len([]rune(value)) > 160 {
			return Entry{}, fmt.Errorf("账号、角色、租户和模型宿主标注不能超过 160 字")
		}
	}
	rows, err := List(cases)
	if err != nil {
		return Entry{}, err
	}
	if len(rows) >= 32 {
		return Entry{}, fmt.Errorf("每项目最多 32 份登录材料，请删除不再使用的材料")
	}
	entry := Entry{ID: store.NewID("ac"), Name: strings.TrimSpace(in.Name), Kind: in.Kind,
		Origin: u.Scheme + "://" + u.Host, URL: in.Origin, Account: in.Account, Role: in.Role, Tenant: in.Tenant, Note: in.Note, Profile: in.Profile, IntentID: in.IntentID, CreatedAt: time.Now()}
	if cdpOnly {
		entry.Origin = ""
	}
	material := in.Material
	switch in.Kind {
	case "browser":
		cdp, err := url.Parse(in.CDPURL)
		if in.CDPURL != "" && (err != nil || cdp.User != nil || cdp.Host == "" ||
			(cdp.Scheme != "http" && cdp.Scheme != "https" && cdp.Scheme != "ws" && cdp.Scheme != "wss") ||
			(cdp.Hostname() != "127.0.0.1" && cdp.Hostname() != "localhost" && cdp.Hostname() != "::1")) {
			return Entry{}, fmt.Errorf("单机浏览器接管需要本机 loopback CDP 地址")
		}
		entry.Session = "tw_" + entry.ID
		if in.Session != "" {
			if len(in.Session) > 160 || strings.ContainsAny(in.Session, " \t/\\") {
				return Entry{}, fmt.Errorf("浏览器 session 无效")
			}
			entry.Session = in.Session
		}
		for _, row := range rows {
			if row.Kind != "browser" {
				continue
			}
			raw, err := Material(cases, row.ID)
			if err != nil {
				return Entry{}, err
			}
			var binding struct {
				CDPURL string `json:"cdp_url"`
			}
			if err := json.Unmarshal(raw, &binding); err != nil {
				return Entry{}, err
			}
			if (in.Session != "" && row.Profile == in.Profile && row.Session == in.Session) || (in.CDPURL != "" && sameCDP(in.CDPURL, binding.CDPURL)) {
				return Entry{}, fmt.Errorf("该浏览器已登记为「%s」，请复用原现场，不要重复登记", row.Name)
			}
		}
		material, _ = json.Marshal(BrowserBinding{CDPURL: in.CDPURL, Session: entry.Session, Profile: entry.Profile, ExistingSession: in.Session != ""})
	case "state":
		var state struct {
			Cookies []json.RawMessage `json:"cookies"`
			Origins []json.RawMessage `json:"origins"`
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(material, &object) != nil || object == nil || json.Unmarshal(material, &state) != nil || (object["cookies"] == nil && object["origins"] == nil) {
			return Entry{}, fmt.Errorf("状态文件必须是含 cookies 或 origins 的 JSON 对象")
		}
	case "request":
		var request struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body,omitempty"`
		}
		if json.Unmarshal(material, &request) != nil || request.Method == "" {
			return Entry{}, fmt.Errorf("请求材料需要 method、url、headers，可选 body")
		}
		target, err := url.Parse(request.URL)
		if err != nil || target.User != nil || target.Scheme+"://"+target.Host != entry.Origin || target.Host == "" {
			return Entry{}, fmt.Errorf("请求 URL 必须属于适用站点")
		}
	default:
		return Entry{}, fmt.Errorf("kind 只能是 browser、state 或 request")
	}
	dir, _ := path(cases, entry.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Entry{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "material.json"), material, 0o600); err != nil {
		return Entry{}, err
	}
	raw, _ := json.MarshalIndent(entry, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "entry.json"), raw, 0o600); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Loopback aliases and HTTP/WebSocket URLs for the same debugging port share
// one browser. Registering them twice would bypass resource ownership.
func sameCDP(a, b string) bool {
	if b == "" {
		return false
	}
	x, ex := url.Parse(a)
	y, ey := url.Parse(b)
	if ex != nil || ey != nil {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" || u.Scheme == "wss" {
			return "443"
		}
		return "80"
	}
	return port(x) == port(y)
}

func List(cases string) ([]Entry, error) {
	dirs, err := os.ReadDir(root(cases))
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows := []Entry{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		entry, err := Get(cases, d.Name())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			} // unpublished or explicitly retired entry
			return nil, err
		}
		rows = append(rows, entry)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows, nil
}

func Get(cases, id string) (Entry, error) {
	dir, err := path(cases, id)
	if err != nil {
		return Entry{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "entry.json"))
	if err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return Entry{}, err
	}
	if entry.ID != id {
		return Entry{}, fmt.Errorf("登录材料身份不匹配")
	}
	lease, err := readLease(dir)
	if err != nil {
		return Entry{}, err
	}
	entry.Lease = lease
	return entry, nil
}

func Material(cases, id string) ([]byte, error) {
	if _, err := Get(cases, id); err != nil {
		return nil, err
	}
	dir, _ := path(cases, id)
	return os.ReadFile(filepath.Join(dir, "material.json"))
}

// Delete only removes the exact two owned files; no recursive delete follows a
// caller-provided path or a directory inserted by an executor.
func Delete(cases, id string) error {
	entry, err := Get(cases, id)
	if err != nil {
		return err
	}
	if entry.Lease != nil {
		return fmt.Errorf("浏览器现场仍被占用，请先归还或恢复")
	}
	dir, _ := path(cases, id)
	// Remove only files owned by this resource. Retire metadata last, so a
	// failed delete remains visible and can be retried.
	for _, name := range []string{"material.json", "control-session.json", "state-export.json"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(filepath.Join(dir, "entry.json")); err != nil {
		return err
	}
	// An executor may have left unrelated files; keep them rather than deleting
	// recursively or reviving a resource whose metadata has been retired.
	_ = os.Remove(dir)
	return nil
}
