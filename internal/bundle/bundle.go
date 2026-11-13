// Package bundle moves a whole case between machines.
//
// The bundle is a zip with a manifest that carries a sha256 per entry: a
// transfer that silently drops or corrupts evidence is worse than no transfer,
// because the report keeps pointing at proof that is no longer there
// (docs/08, D28).
//
// The hash is for搬运完整性 only. It is not written back into the ledger and
// it proves nothing about the evidence itself, which is D6's boundary.
package bundle

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
)

// Version is the bundle format version. Bump only when an old reader would
// misread a new bundle.
const Version = 1

// ManifestFile is always the last entry of the archive.
const ManifestFile = "manifest.json"

// File is one entry of the bundle.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Counts is the summary a human reads before importing.
type Counts struct {
	Facts    int `json:"facts"`
	Evidence int `json:"evidence"`
	Reports  int `json:"reports"`
}

// Manifest describes the archive. Missing lists evidence whose original was
// already gone at export time: the import still succeeds, but that evidence is
// unreadable, and saying so beats letting the report fail months later.
type Manifest struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Source     string    `json:"source"`
	Counts     Counts    `json:"counts"`
	Files      []File    `json:"files"`
	Missing    []string  `json:"missing,omitempty"`
}

// skipDirs never travel: .tw is the handoff channel, .verify is a judgement,
// .cyber is harness runtime state.
var skipDirs = map[string]bool{".tw": true, ".verify": true, ".cyber": true, ".access": true, ".native": true}

// ledgerFacts is the sliver of the ledger export needs: counts, and the evidence
// rows whose original bytes must still be checkable.
type ledgerFacts struct {
	Facts    []json.RawMessage `json:"facts"`
	Evidence []struct {
		ID     string `json:"id"`
		URI    string `json:"uri"`
		Digest string `json:"digest"`
	} `json:"evidence"`
}

// Export writes one archive to out and returns its manifest.
func Export(caseRoot, ledgerPath, out string) (Manifest, error) {
	m := Manifest{Version: Version, ExportedAt: time.Now(), Source: filepath.Base(caseRoot)}

	var entries []struct{ name, abs string }
	if ledgerPath != "" {
		if st, err := os.Stat(ledgerPath); err == nil && !st.IsDir() {
			entries = append(entries, struct{ name, abs string }{"ledger.json", ledgerPath})
			if b, err := os.ReadFile(ledgerPath); err == nil {
				var lf ledgerFacts
				if json.Unmarshal(b, &lf) == nil {
					m.Counts.Facts = len(lf.Facts)
					m.Counts.Evidence = len(lf.Evidence)
					for _, e := range lf.Evidence {
						// A row with a digest must still hash to it before we
						// copy the case: a bundle that carries altered bytes
						// would launder tampering into the recipient's ledger.
						// A legacy row (no digest) keeps the old existence-only
						// check; there is nothing to compare its bytes against.
						err := evidence.Verify(caseRoot, model.Evidence{ID: e.ID, URI: e.URI, Digest: e.Digest})
						switch {
						case err == nil:
						case errors.Is(err, evidence.ErrDigestMismatch):
							return m, fmt.Errorf("%w：证据 %s（%s）的字节与登记摘要不符，bundle 不能导出", evidence.ErrDigestMismatch, e.ID, e.URI)
						case errors.Is(err, evidence.ErrMissing), errors.Is(err, evidence.ErrOutside):
							m.Missing = append(m.Missing, e.URI)
						default:
							return m, err
						}
					}
				}
			}
		}
	}

	walkErr := filepath.WalkDir(caseRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(caseRoot, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := filepath.ToSlash(filepath.Join(caseDirName, rel))
		entries = append(entries, struct{ name, abs string }{name, p})
		// One report = one directory carrying report.json.
		if strings.HasSuffix(rel, "report.json") && strings.Contains(rel, "reports") {
			m.Counts.Reports++
		}
		return nil
	})
	if walkErr != nil {
		return m, walkErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return m, err
	}
	f, err := os.Create(out)
	if err != nil {
		return m, err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	for _, e := range entries {
		rec, err := addFile(zw, e.name, e.abs)
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, rec)
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	w, err := zw.Create(ManifestFile)
	if err != nil {
		return m, err
	}
	if _, err := w.Write(mb); err != nil {
		return m, err
	}
	if err := zw.Close(); err != nil {
		return m, err
	}
	return m, nil
}

func addFile(zw *zip.Writer, name, abs string) (File, error) {
	src, err := os.Open(abs)
	if err != nil {
		return File{}, err
	}
	defer src.Close()
	w, err := zw.Create(name)
	if err != nil {
		return File{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), src)
	if err != nil {
		return File{}, err
	}
	return File{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// Import verifies the whole archive first, then extracts it. A single bad
// checksum rejects the transfer, so a half-written case can never appear.
func Import(zipPath, destDir string) (Manifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("打开 bundle 失败：%w", err)
	}
	defer zr.Close()

	m, err := readManifest(zr)
	if err != nil {
		return Manifest{}, err
	}
	if m.Version != Version {
		return Manifest{}, fmt.Errorf("bundle 版本 %d 不受支持（当前 %d）", m.Version, Version)
	}

	want := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		want[f.Path] = f
	}

	// Pass 1: verify everything before writing anything.
	seen := make(map[string]bool, len(m.Files))
	for _, zf := range zr.File {
		if zf.Name == ManifestFile {
			continue
		}
		if _, err := safeName(zf.Name); err != nil {
			return Manifest{}, err
		}
		w, ok := want[zf.Name]
		if !ok {
			return Manifest{}, fmt.Errorf("bundle 里有清单外的条目：%s", zf.Name)
		}
		if err := verifyEntry(zf, w); err != nil {
			return Manifest{}, err
		}
		seen[zf.Name] = true
	}
	for name := range want {
		if !seen[name] {
			return Manifest{}, fmt.Errorf("清单里的文件不在包里：%s", name)
		}
	}

	// Pass 2: extract into a sibling temp dir, then move into place.
	tmp := destDir + ".partial"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Manifest{}, err
	}
	for _, zf := range zr.File {
		if zf.Name == ManifestFile {
			continue
		}
		rel, err := safeName(zf.Name)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return Manifest{}, err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = os.RemoveAll(tmp)
			return Manifest{}, err
		}
		if err := extractEntry(zf, dst); err != nil {
			_ = os.RemoveAll(tmp)
			return Manifest{}, err
		}
	}
	// Pass 3: the zip manifest proves the transfer, not the ledger's own
	// invariant. Re-check preserved digests against the extracted bytes before
	// publishing the destination: a bundle can be internally consistent and
	// still carry evidence the ledger says is something else.
	if err := verifyImportedEvidence(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return Manifest{}, err
	}
	_ = os.RemoveAll(destDir)
	if err := os.Rename(tmp, destDir); err != nil {
		_ = os.RemoveAll(tmp)
		return Manifest{}, err
	}
	return m, nil
}

// caseDirName is the prefix Export gives every case file inside the archive, so
// the extracted case root is <dest>/case.
const caseDirName = "case"

// verifyImportedEvidence checks the restored ledger's digests against the
// extracted bytes. Missing originals are tolerated: Export records them in
// Manifest.Missing and the import keeps the ledger as it was. A digest conflict
// is a refusal, because accepting it would make the recipient's ledger point at
// bytes that are not the admitted proof (F2/D150).
func verifyImportedEvidence(tmp string) error {
	b, err := os.ReadFile(filepath.Join(tmp, "ledger.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var lf ledgerFacts
	if err := json.Unmarshal(b, &lf); err != nil {
		return fmt.Errorf("ledger.json 不是合法 JSON")
	}
	caseRoot := filepath.Join(tmp, caseDirName)
	for _, e := range lf.Evidence {
		if strings.TrimSpace(e.Digest) == "" {
			// Legacy row: only existence was ever recorded for it.
			continue
		}
		err := evidence.Verify(caseRoot, model.Evidence{ID: e.ID, URI: e.URI, Digest: e.Digest})
		switch {
		case err == nil:
		case errors.Is(err, evidence.ErrMissing), errors.Is(err, evidence.ErrOutside):
			// The manifest already carries this as Missing; the ledger keeps
			// the row and the operator sees the gap.
		case errors.Is(err, evidence.ErrDigestMismatch):
			return fmt.Errorf("%w：bundle 里证据 %s（%s）的字节与登记摘要不符，拒绝导入", evidence.ErrDigestMismatch, e.ID, e.URI)
		default:
			return err
		}
	}
	return nil
}

func readManifest(zr *zip.ReadCloser) (Manifest, error) {
	for _, zf := range zr.File {
		if zf.Name != ManifestFile {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return Manifest{}, err
		}
		defer rc.Close()
		var m Manifest
		if err := json.NewDecoder(rc).Decode(&m); err != nil {
			return Manifest{}, fmt.Errorf("%s 不是合法 JSON", ManifestFile)
		}
		return m, nil
	}
	return Manifest{}, fmt.Errorf("bundle 里没有 %s", ManifestFile)
}

func verifyEntry(zf *zip.File, want File) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return err
	}
	if n != want.Size {
		return fmt.Errorf("%s 大小不符：清单 %d，实际 %d", zf.Name, want.Size, n)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("%s 校验和不符：清单 %s，实际 %s", zf.Name, want.SHA256, got)
	}
	return nil
}

func extractEntry(zf *zip.File, dst string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// safeName rejects entries that could escape the destination. Zip entries are
// attacker-controlled input the moment a bundle arrives from another host.
func safeName(name string) (string, error) {
	n := filepath.ToSlash(strings.TrimSpace(name))
	if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, "..") ||
		filepath.IsAbs(filepath.FromSlash(n)) || strings.Contains(n, ":") {
		return "", fmt.Errorf("bundle 条目路径不合法：%q", name)
	}
	return n, nil
}
