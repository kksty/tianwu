package settings

// The harness owns the LLM configuration: ~/.cyber/cyber.yaml holds
// llm.providers[] plus llm.active_profile, and both the harness's own web UI,
// the REPL and every local run resolve their model from that document.
//
// The console therefore edits that file instead of keeping a second copy of the
// same knobs. Reading it keeps the two in step when someone changes the model in
// the harness UI or with `aiscan config use`; writing it preserves every other
// section (agent, traffic, extensions, …) and every profile the console does not
// touch.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// HarnessConfigPath is the harness's user-level configuration. TW_HARNESS_CONFIG
// is the deployment override for a control plane that runs under another account
// (a service, a container) than the harness it drives.
func HarnessConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("TW_HARNESS_CONFIG")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cyber", "cyber.yaml")
}

// readHarnessModel returns the active profile of a harness config file and the
// id it was read from. A missing file is an empty configuration, not an error:
// a deployment may configure the harness through CYBER_* only.
func readHarnessModel(path string) (Model, string, error) {
	if path == "" {
		return Model{}, "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Model{}, "", nil
	}
	if err != nil {
		return Model{}, "", err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Model{}, "", fmt.Errorf("解析 %s：%w", path, err)
	}
	doc := documentRoot(&root)
	llm := mapGet(doc, "llm")

	active := strings.TrimSpace(scalar(mapGet(llm, "active_profile")))
	providers := mapGet(llm, "providers")
	var entry *yaml.Node
	if providers != nil && providers.Kind == yaml.SequenceNode {
		if active != "" {
			for _, item := range providers.Content {
				if item.Kind == yaml.MappingNode && strings.TrimSpace(scalar(mapGet(item, "id"))) == active {
					entry = item
					break
				}
			}
		}
		if entry == nil {
			for _, item := range providers.Content {
				if item.Kind == yaml.MappingNode {
					entry = item
					break
				}
			}
		}
	}
	if entry != nil && active == "" {
		active = strings.TrimSpace(scalar(mapGet(entry, "id")))
	}

	m := Model{}
	if entry != nil {
		if m.BaseURL, err = stringField(entry, "base_url"); err != nil {
			return Model{}, "", err
		}
		if m.APIKey, err = stringField(entry, "api_key"); err != nil {
			return Model{}, "", err
		}
		if m.Model, err = stringField(entry, "model"); err != nil {
			return Model{}, "", err
		}
		if m.Provider, err = stringField(entry, "provider"); err != nil {
			return Model{}, "", err
		}
		if m.ContextWindow, err = intField(entry, "context_window"); err != nil {
			return Model{}, "", err
		}
		if m.MaxTokens, err = intField(entry, "max_tokens"); err != nil {
			return Model{}, "", err
		}
	}

	// Flat llm.* fields win over the active profile in the harness's own merge
	// (pkg/config/provider_runtime.go), so the console has to read them last.
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"base_url", &m.BaseURL},
		{"api_key", &m.APIKey},
		{"model", &m.Model},
		{"provider", &m.Provider},
	} {
		if v := strings.TrimSpace(scalar(mapGet(llm, f.key))); v != "" {
			*f.dst = v
		}
	}
	if n, err := intField(llm, "context_window"); err != nil {
		return Model{}, "", err
	} else if n > 0 {
		m.ContextWindow = n
	}
	if n, err := intField(llm, "max_tokens"); err != nil {
		return Model{}, "", err
	} else if n > 0 {
		m.MaxTokens = n
	}
	return m, active, nil
}

// writeHarnessModel updates the active profile in place. Everything the console
// does not own (other profiles, agent/traffic/extensions sections, proxy,
// timeout, images) is left exactly as it was.
func writeHarnessModel(path string, m Model) error {
	if path == "" {
		return errors.New("没有 harness 配置路径：设置 TW_HARNESS_CONFIG")
	}
	data, err := os.ReadFile(path)
	var root yaml.Node
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("解析 %s：%w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		root = yaml.Node{Kind: yaml.DocumentNode}
	default:
		return err
	}
	doc := documentRoot(&root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		doc = documentRoot(&root)
	}

	llm := ensureMapping(doc, "llm")
	providers := ensureSequence(llm, "providers")

	active := strings.TrimSpace(scalar(mapGet(llm, "active_profile")))
	entry := findProfile(providers, active)
	if entry == nil {
		entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		providers.Content = append(providers.Content, entry)
	}
	id := strings.TrimSpace(scalar(mapGet(entry, "id")))
	if id == "" {
		id = active
		if id == "" {
			id = "default"
		}
		setScalar(entry, "id", id)
	}
	if active == "" {
		setScalar(llm, "active_profile", id)
	}

	setScalar(entry, "provider", m.Provider)
	setScalar(entry, "base_url", m.BaseURL)
	setScalar(entry, "model", m.Model)
	// An empty key means "keep the stored one" (ApplyView already applied that
	// rule); never write "" over a working credential.
	if strings.TrimSpace(m.APIKey) != "" {
		setScalar(entry, "api_key", m.APIKey)
	}
	setIntOrDelete(entry, "context_window", m.ContextWindow)
	setIntOrDelete(entry, "max_tokens", m.MaxTokens)

	// If the file also carries flat llm.* overrides, mirror the edit there:
	// otherwise the flat value shadows the profile in the harness merge.
	for _, key := range []string{"provider", "base_url", "model"} {
		if mapGet(llm, key) != nil {
			setScalar(llm, key, fieldString(m, key))
		}
	}
	if mapGet(llm, "api_key") != nil && strings.TrimSpace(m.APIKey) != "" {
		setScalar(llm, "api_key", m.APIKey)
	}
	if mapGet(llm, "context_window") != nil {
		setIntOrDelete(llm, "context_window", m.ContextWindow)
	}
	if mapGet(llm, "max_tokens") != nil {
		setIntOrDelete(llm, "max_tokens", m.MaxTokens)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(&root); err != nil {
		return fmt.Errorf("编码 %s：%w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("编码 %s：%w", path, err)
	}
	return writeFileAtomic(path, buf.Bytes())
}

func fieldString(m Model, key string) string {
	switch key {
	case "provider":
		return m.Provider
	case "base_url":
		return m.BaseURL
	case "model":
		return m.Model
	case "api_key":
		return m.APIKey
	}
	return ""
}

// writeFileAtomic stages beside the target and renames over it: another process
// (the harness web UI) may be reading the file at the same moment, and it must
// never see half a document. The staged file keeps the target's owner on Unix
// (see preserveOwner) so a root-run control plane editing another account's
// config does not lock that account out of its own harness.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var existing os.FileInfo
	if info, statErr := os.Stat(path); statErr == nil {
		existing = info
	}
	tmp, err := os.CreateTemp(dir, ".cyber-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if existing != nil {
		preserveOwner(name, existing)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ---- a tiny YAML-node walker (comments and unknown keys survive a round trip) ----

func documentRoot(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		return root.Content[0]
	}
	return root
}

func mapGet(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func mapSet(node *yaml.Node, key string, value *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func mapDelete(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func ensureMapping(parent *yaml.Node, key string) *yaml.Node {
	if v := mapGet(parent, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapSet(parent, key, v)
	return v
}

func ensureSequence(parent *yaml.Node, key string) *yaml.Node {
	if v := mapGet(parent, key); v != nil && v.Kind == yaml.SequenceNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapSet(parent, key, v)
	return v
}

func findProfile(providers *yaml.Node, id string) *yaml.Node {
	if providers == nil || providers.Kind != yaml.SequenceNode {
		return nil
	}
	if id != "" {
		for _, item := range providers.Content {
			if item.Kind == yaml.MappingNode && strings.TrimSpace(scalar(mapGet(item, "id"))) == id {
				return item
			}
		}
	}
	return nil
}

func setScalar(node *yaml.Node, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		mapDelete(node, key)
		return
	}
	mapSet(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setIntOrDelete(node *yaml.Node, key string, value int) {
	if value <= 0 {
		mapDelete(node, key)
		return
	}
	mapSet(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)})
}

func scalar(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func stringField(node *yaml.Node, key string) (string, error) {
	v := mapGet(node, key)
	if v == nil {
		return "", nil
	}
	if v.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("harness 配置的 %s 不是字符串", key)
	}
	return strings.TrimSpace(v.Value), nil
}

func intField(node *yaml.Node, key string) (int, error) {
	v := mapGet(node, key)
	if v == nil || strings.TrimSpace(v.Value) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v.Value))
	if err != nil {
		return 0, fmt.Errorf("harness 配置的 %s 不是整数：%q", key, v.Value)
	}
	return n, nil
}
