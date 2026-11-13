package bundle

import (
	"encoding/json"
	"github.com/kksty/tianwu/internal/store"
	"os"
	"path/filepath"
)

// ExportLedger serializes one committed logical snapshot. Copying a live
// SQLite main file without its WAL would silently omit committed evidence.
func ExportLedger(caseRoot string, s *store.Store, out string) (Manifest, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return Manifest{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(out), "ledger-export-*.json")
	if err != nil {
		return Manifest{}, err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(s.Snapshot())
	closeErr := f.Close()
	if err != nil {
		return Manifest{}, err
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	return Export(caseRoot, f.Name(), out)
}
