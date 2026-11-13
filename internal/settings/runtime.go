package settings

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RuntimeConfig materialises the default host/audit configuration. The saved
// document also carries Tianwu admission and scheduling metadata, which the
// harness's strict parser must never see, even on an inactive profile.
// A content-addressed path makes Host.Update notice changes to file-only
// settings while a no-op save or admission toggle leaves live sessions alone.
func (s *Store) RuntimeConfig(dir string) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	doc := root.Content[0]
	if s.harness != "" {
		var err error
		root, doc, err = s.readDocument()
		if err != nil {
			return "", err
		}
		// The harness resolves misc.data_dir relative to its configuration
		// file. Moving that file must not move the operator's runtime data.
		if misc := mapGet(doc, "misc"); misc != nil {
			if dataDir := scalar(mapGet(misc, "data_dir")); dataDir != "" && !filepath.IsAbs(dataDir) {
				sourceDir, err := filepath.Abs(filepath.Dir(s.harness))
				if err != nil {
					return "", err
				}
				setScalar(misc, "data_dir", filepath.Join(sourceDir, dataDir))
			}
		}
	}
	if providers := mapGet(mapGet(doc, "llm"), "providers"); providers != nil {
		if providers.Kind != yaml.SequenceNode {
			return "", fmt.Errorf("harness 的 llm.providers 必须是列表")
		}
		for _, entry := range providers.Content {
			p, err := decodeProfile(entry)
			if err != nil {
				return "", err
			}
			if key := s.currentKey(p); key != "" {
				setScalar(entry, "api_key", key)
			}
			stripProfileMetadata(entry)
		}
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("编码宿主运行配置失败")
	}
	digest := sha256.Sum256(data)
	path, err := filepath.Abs(filepath.Join(dir, fmt.Sprintf("runtime-%x.yaml", digest[:12])))
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func stripProfileMetadata(entry *yaml.Node) {
	for _, key := range []string{"pricing", "tier", "api_keys", "max_concurrency", "fallback", "paused"} {
		mapDelete(entry, key)
	}
}
