// Package artifact preserves scanner-native results without promoting them to
// findings. Only an executor's explicit citation materializes evidence.
package artifact

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

	"github.com/kksty/tianwu/internal/aop/tool"
)

const MaxBytes = 16 << 20

type Record struct {
	Ref       string    `json:"ref"`
	RunID     string    `json:"run_id"`
	IntentID  string    `json:"intent_id"`
	EventID   string    `json:"event_id"`
	ResultID  string    `json:"result_id"`
	Tool      string    `json:"tool"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	MediaType string    `json:"media_type"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// key hashes an opaque identifier for a bounded filename; it is not an evidence
// integrity hash and does not reinterpret the harness's result identity.
func key(ref string) string   { sum := sha256.Sum256([]byte(ref)); return hex.EncodeToString(sum[:]) }
func dir(cases string) string { return filepath.Join(cases, ".native") }

func Save(cases, runID, intentID, eventID string, value *tool.Artifact) (Record, error) {
	if value == nil || len(value.Data) > MaxBytes {
		return Record{}, fmt.Errorf("原生结果为空或超过 16 MiB")
	}
	resultID := value.ResultId
	if resultID == "" {
		resultID = eventID
	}
	if resultID == "" || runID == "" {
		return Record{}, fmt.Errorf("原生结果缺少稳定身份")
	}
	r := Record{Ref: runID + "/" + resultID, RunID: runID, IntentID: intentID, EventID: eventID, ResultID: resultID,
		Tool: value.Tool, Kind: value.Kind, Target: value.Target, MediaType: value.MediaType, Size: len(value.Data), CreatedAt: time.Now()}
	if value.Timestamp != nil {
		r.CreatedAt = value.Timestamp.AsTime()
	}
	if old, raw, err := Read(cases, r.Ref); err == nil {
		if string(raw) != string(value.Data) || old.Tool != r.Tool || old.Target != r.Target || old.Kind != r.Kind || old.MediaType != r.MediaType {
			return Record{}, fmt.Errorf("原生结果身份冲突：%s", r.Ref)
		}
		return old, nil
	} else if !os.IsNotExist(err) {
		return Record{}, err
	}
	if err := os.MkdirAll(dir(cases), 0700); err != nil {
		return Record{}, err
	}
	base := filepath.Join(dir(cases), key(r.Ref))
	if err := os.WriteFile(base+".data", value.Data, 0600); err != nil {
		return Record{}, err
	}
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(base+".json", raw, 0600); err != nil {
		return Record{}, err
	}
	return r, nil
}

func Read(cases, ref string) (Record, []byte, error) {
	if ref == "" || len(ref) > 4096 {
		return Record{}, nil, fmt.Errorf("无效的原生结果引用")
	}
	base := filepath.Join(dir(cases), key(ref))
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		return Record{}, nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, nil, err
	}
	if r.Ref != ref {
		return r, nil, fmt.Errorf("原生结果身份不匹配")
	}
	data, err := os.ReadFile(base + ".data")
	if err == nil && len(data) != r.Size {
		err = fmt.Errorf("原生结果字节数与记录不一致")
	}
	return r, data, err
}

func List(cases, runID, query string, limit int) ([]Record, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	files, err := os.ReadDir(dir(cases))
	if os.IsNotExist(err) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows := []Record{}
	query = strings.ToLower(query)
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir(cases), f.Name()))
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if runID != "" && r.RunID != runID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(r.Target+" "+r.Tool+" "+r.Kind+" "+r.ResultID), query) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// Materialize copies only explicitly cited bytes into the current case.
// Selected evidence and its provenance then follow the usual ledger
// path. Missing native data remains a reference rather than fabricated proof.
//
// The file written here is a working path, not the evidence address: when the
// declaration cites it, ledger admission preserves the bytes into the
// content-addressed store and records the digest (F2/D150). Materialize itself
// records no evidence row, so a later write of this path cannot change what an
// earlier declaration proves.
func Materialize(cases, workDir, ref string) (string, error) {
	r, data, err := Read(cases, ref)
	if err != nil {
		return "", err
	}
	ext := ".bin"
	if strings.Contains(r.MediaType, "json") && json.Valid(data) {
		ext = ".json"
	}
	rel := filepath.Join("native-evidence", key(ref)+ext)
	p := filepath.Join(workDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
