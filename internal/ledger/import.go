package ledger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Imported is one artifact attached to a synthetic intent.
type Imported struct {
	IntentID   string
	EvidenceID string
	// Path is where the copy lives, relative to the case root.
	Path string
}

// AttachImport copies one file into the case as evidence and records it behind
// a synthetic "导入" intent.
//
// Facts must be traceable to an intent (docs/02 invariant 2). Inventing a
// hidden owner would be worse than an honest queue row that says "this came
// from an import" — it is also what makes "why is this in the ledger" answerable
// six months later.
func AttachImport(st *store.Store, caseRoot, srcPath, sub, title, description string) (Imported, error) {
	srcPath = strings.TrimSpace(srcPath)
	if srcPath == "" {
		return Imported{}, fmt.Errorf("没有文件路径")
	}
	if !filepath.IsAbs(srcPath) {
		srcPath = filepath.Join(caseRoot, filepath.FromSlash(srcPath))
	}
	st0, err := os.Stat(srcPath)
	if err != nil {
		return Imported{}, err
	}
	if st0.IsDir() {
		return Imported{}, fmt.Errorf("%s 是目录", srcPath)
	}

	in, err := st.CreateIntent(model.Intent{
		Title:       title,
		Description: description,
		Doc:         fmt.Sprintf("来源文件：%s（%d 字节）", filepath.Base(srcPath), st0.Size()),
	})
	if err != nil {
		return Imported{}, err
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		return Imported{}, err
	}

	dir := filepath.Join(caseRoot, in.ID, filepath.FromSlash(sub))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Imported{}, err
	}
	dst := filepath.Join(dir, filepath.Base(srcPath))
	if err := copyFile(srcPath, dst); err != nil {
		return Imported{}, err
	}
	rel, err := filepath.Rel(caseRoot, dst)
	if err != nil {
		return Imported{}, err
	}
	// The working copy is a convenience; the ledger's address is the preserved
	// object. If preservation fails the import is refused: a row pointing at a
	// copy someone can later overwrite would not be proof (F2/D150).
	preserved, err := evidence.Preserve(caseRoot, dst)
	if err != nil {
		return Imported{}, fmt.Errorf("保全导入文件失败：%w", err)
	}
	ev, err := st.AddEvidence(model.Evidence{
		IntentID:  in.ID,
		Name:      filepath.Base(dst),
		Mime:      mimeByExt(dst),
		Size:      preserved.Size,
		URI:       preserved.URI,
		Digest:    preserved.Digest,
		SourceURI: evidence.OriginPath(caseRoot, srcPath),
		CreatedAt: time.Now(),
	})
	if err != nil {
		return Imported{}, err
	}
	return Imported{IntentID: in.ID, EvidenceID: ev.ID, Path: filepath.ToSlash(rel)}, nil
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
