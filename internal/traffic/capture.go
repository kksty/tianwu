package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/aop/operation"
	native "github.com/kksty/tianwu/internal/aop/traffic"
	"google.golang.org/protobuf/proto"
)

// Capture preserves an observed exchange. Attribution comes from the event's
// accepted run and typed operation reference, never from URL/time guessing.
type Capture struct {
	Ref           string         `json:"ref"`
	RunID         string         `json:"run_id"`
	IntentID      string         `json:"intent_id"`
	SessionID     string         `json:"session_id,omitempty"`
	EventID       string         `json:"event_id"`
	FlowID        string         `json:"flow_id"`
	Method        string         `json:"method"`
	URL           string         `json:"url"`
	Status        int32          `json:"status"`
	At            time.Time      `json:"at"`
	Complete      bool           `json:"complete"`
	Error         string         `json:"error,omitempty"`
	RequestBytes  int            `json:"request_bytes"`
	ResponseBytes int            `json:"response_bytes"`
	PayloadSHA256 string         `json:"payload_sha256,omitempty"`
	Operation     *operation.Ref `json:"operation,omitempty"`
}

func captureBase(cases, ref string) string {
	h := sha256.Sum256([]byte(ref))
	return filepath.Join(cases, ".http", hex.EncodeToString(h[:]))
}

func SaveCapture(cases, runID, intentID, sessionID, eventID string, flow *native.Flow, ref *operation.Ref) (Capture, error) {
	if flow == nil || flow.Id == "" || runID == "" {
		return Capture{}, fmt.Errorf("HTTP 观察缺少稳定身份")
	}
	r := Capture{Ref: runID + "/" + flow.Id, RunID: runID, IntentID: intentID, SessionID: sessionID, EventID: eventID,
		FlowID: flow.Id, Method: flow.GetRequest().GetMethod(), URL: flow.GetRequest().GetUrl(), Status: flow.GetResponse().GetStatusCode(),
		Complete: flow.Complete, Error: flow.Error, RequestBytes: len(flow.GetRequest().GetBody()), ResponseBytes: len(flow.GetResponse().GetBody()), Operation: ref}
	if flow.Timestamp != nil {
		r.At = flow.Timestamp.AsTime()
	}
	if old, value, err := ReadCapture(cases, r.Ref); err == nil {
		if !proto.Equal(value, flow) || old.IntentID != intentID || old.SessionID != sessionID || !proto.Equal(old.Operation, ref) {
			return Capture{}, fmt.Errorf("HTTP 观察身份冲突：%s", r.Ref)
		}
		return old, nil
	} else if !os.IsNotExist(err) {
		return Capture{}, err
	}
	base := captureBase(cases, r.Ref)
	// A committed index with a missing body is damaged data, not permission to
	// replace the first observation under the same stable identity.
	if _, err := os.Stat(base + ".json"); err == nil {
		return Capture{}, fmt.Errorf("HTTP 原件丢失，保留已有观察：%s", r.Ref)
	} else if !os.IsNotExist(err) {
		return Capture{}, err
	}
	if err := os.MkdirAll(filepath.Dir(base), 0700); err != nil {
		return Capture{}, err
	}
	b, err := proto.Marshal(flow)
	if err != nil {
		return Capture{}, err
	}
	sum := sha256.Sum256(b)
	r.PayloadSHA256 = hex.EncodeToString(sum[:])
	if err := writeCaptureFile(base+".pb", b); err != nil {
		return Capture{}, err
	}
	meta, err := json.Marshal(r)
	if err != nil {
		return Capture{}, err
	}
	if err := writeCaptureFile(base+".json", meta); err != nil {
		return Capture{}, err
	}
	return r, nil
}

func writeCaptureFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".http-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func ReadCapture(cases, ref string) (Capture, *native.Flow, error) {
	if ref == "" || len(ref) > 4096 {
		return Capture{}, nil, fmt.Errorf("无效流量引用")
	}
	base := captureBase(cases, ref)
	b, err := os.ReadFile(base + ".json")
	if err != nil {
		return Capture{}, nil, err
	}
	var r Capture
	if err := json.Unmarshal(b, &r); err != nil {
		return r, nil, err
	}
	if r.Ref != ref {
		return r, nil, fmt.Errorf("流量身份不匹配")
	}
	b, err = os.ReadFile(base + ".pb")
	if err != nil {
		return r, nil, err
	}
	if r.PayloadSHA256 != "" {
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != r.PayloadSHA256 {
			return r, nil, fmt.Errorf("HTTP 原件与已保存校验值不一致")
		}
	}
	f := new(native.Flow)
	if err := proto.Unmarshal(b, f); err != nil {
		return r, nil, err
	}
	if f.Id != r.FlowID || r.Ref != r.RunID+"/"+f.Id || f.GetRequest().GetUrl() != r.URL || f.GetRequest().GetMethod() != r.Method || f.GetResponse().GetStatusCode() != r.Status || len(f.GetRequest().GetBody()) != r.RequestBytes || len(f.GetResponse().GetBody()) != r.ResponseBytes {
		return r, nil, fmt.Errorf("HTTP 原件与索引身份或摘要不一致")
	}
	return r, f, nil
}

func ListCaptures(cases, query, run string, offset, limit int) ([]Capture, int, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows := []Capture{}
	files, err := os.ReadDir(filepath.Join(cases, ".http"))
	if os.IsNotExist(err) {
		return rows, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(cases, ".http", file.Name()))
		if err != nil {
			return nil, 0, err
		}
		var r Capture
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, 0, err
		}
		if run != "" && r.RunID != run {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(r.Method+" "+r.URL+" "+fmt.Sprint(r.Status)+" "+r.Error+" "+r.IntentID), query) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].At.Equal(rows[j].At) {
			return rows[i].Ref < rows[j].Ref
		}
		return rows[i].At.After(rows[j].At)
	})
	total := len(rows)
	if offset >= total {
		return []Capture{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return rows[offset:end], total, nil
}
