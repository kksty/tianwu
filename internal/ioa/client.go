// Package ioa sends a message into a live harness session through the IOA
// server that aiscan web mounts beside AOP.
//
// Tianwu does not need a harness fork for "say": aiscan web already embeds the
// IOA server at <host>/ioa, and the harness IOA extension turns a targeted
// message into Runtime.Deliver -> session inbox -> the same run. This package
// is only the HTTP client for that already-existing path.
package ioa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to one host's IOA endpoint. It resolves the space, sender and
// target agent once and reuses them; a host restart invalidates the cached ids
// and the next send re-resolves them.
type Client struct {
	BaseURL string
	Token   string
	Agent   string
	HTTP    *http.Client

	mu     sync.Mutex
	space  string
	sender string
	target string
}

// New returns a client for a harness host. BaseURL is the AOP endpoint
// (http://127.0.0.1:18766); the IOA server lives at its /ioa path.
func New(baseURL, token, agent string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		Agent:   strings.TrimSpace(agent),
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

type space struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Nodes []node `json:"nodes"`
}

type node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type sendMessage struct {
	Content map[string]any `json:"content"`
	Refs    *refs          `json:"refs,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type refs struct {
	Nodes []string `json:"nodes,omitempty"`
}

type message struct {
	ID string `json:"id"`
}

// ErrCursorGone is specific to the all=true history-read contract: every
// other query value is constructed locally and valid, so its 422 response
// means that the supplied after id is absent from the selected space.
var ErrCursorGone = errors.New("IOA 消息游标已失效")

type responseError struct {
	path   string
	status int
	detail string
}

func (e *responseError) Error() string {
	return fmt.Sprintf("IOA %s 失败（%d %s）：%s", e.path, e.status, http.StatusText(e.status), e.detail)
}

// Message is one persisted IOA record as the read API returns it. The observer
// only needs identity, sender, content and meta; transport fields it does not
// understand stay in Content/Meta.
type Message struct {
	ID        string         `json:"id"`
	SpaceID   string         `json:"space_id,omitempty"`
	Sender    string         `json:"sender"`
	CreatedAt string         `json:"created_at"`
	Content   map[string]any `json:"content"`
	Refs      *refs          `json:"refs,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// Read returns persisted messages after the given IOA message id. It is the
// read side of the observer: cursor-based, oldest first, no delivery side
// effects. limit <= 0 leaves the server default.
func (c *Client) Read(ctx context.Context, after string, limit int) ([]Message, error) {
	space, sender, _, err := c.ids(ctx)
	if err != nil {
		return nil, err
	}
	path := "/spaces/" + url.PathEscape(space) + "/messages"
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	// The observer wants the space history, not only messages addressed to one
	// node; harness history reads use the same flag.
	q.Set("all", "true")
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out []Message
	if err := c.getHeaders(ctx, path, &out, map[string]string{"X-Node-ID": sender}); err != nil {
		// A restarted host may have new space/node ids; resolve again next call.
		c.invalidate()
		var response *responseError
		if after != "" && errors.As(err, &response) && response.status == http.StatusUnprocessableEntity {
			return nil, fmt.Errorf("%w：%w", ErrCursorGone, err)
		}
		return nil, err
	}
	return out, nil
}

// Send injects text into sessionID. interrupt=true asks the harness to stop the
// current model request and skip tool calls that have not started; false lets
// the current turn finish and drains the message before the next model call.
//
// A nil error means the IOA server accepted the message, not that the model has
// already read it: IOA has no end-to-end acknowledgement for this path. The
// caller should record the message id and let the run's next event show it.
//
// The space/sender/target ids are cached per host. A host restart can change
// them, so a "gone" response clears the cache and the request is retried once
// after re-resolving; without that, every later say kept POSTing to an id that
// no longer existed until tw itself was restarted.
func (c *Client) Send(ctx context.Context, sessionID, text string, interrupt bool) (string, error) {
	return c.SendEvent(ctx, sessionID, text, interrupt, "")
}

// SendEvent is Send with a stable application event id. The id travels in the
// v1 envelope and is what the harness dedupes on when a retry reuses it; an
// empty id keeps the old behaviour (the transport id is the only identity).
func (c *Client) SendEvent(ctx context.Context, sessionID, text string, interrupt bool, eventID string) (string, error) {
	meta := map[string]any{
		"version":   1,
		"event":     "message",
		"target":    map[string]any{"session_id": sessionID},
		"interrupt": interrupt,
	}
	if eventID != "" {
		meta["id"] = eventID
	}
	return c.send(ctx, sessionID, meta, text)
}

// SendResult sends one v1 task.result record. It requires a stable event id:
// the protocol uses it for admission idempotency across transport retries.
// chain is the task-result chain (bounded by the harness); it is empty for a
// direct result.
func (c *Client) SendResult(ctx context.Context, sessionID, text, eventID string, chain []string) (string, error) {
	if eventID == "" {
		return "", errors.New("task.result 需要稳定的 event id")
	}
	return c.send(ctx, sessionID, map[string]any{
		"version": 1,
		"event":   "task.result",
		"id":      eventID,
		"target":  map[string]any{"session_id": sessionID},
		"chain":   chain,
	}, text)
}

func (c *Client) send(ctx context.Context, sessionID string, meta map[string]any, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("插话内容为空")
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("没有目标 AOP session")
	}
	if c == nil || c.BaseURL == "" {
		return "", errors.New("宿主没有 IOA 地址")
	}
	body := sendMessage{
		Content: map[string]any{"text": text},
		Meta:    meta,
	}

	for attempt := 0; attempt < 2; attempt++ {
		spaceID, sender, target, err := c.ids(ctx)
		if err != nil {
			return "", err
		}
		body.Refs = &refs{Nodes: []string{target}}
		data, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		endpoint := c.apiURL("/spaces/" + url.PathEscape(spaceID) + "/messages")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Node-ID", sender)
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		resp, err := c.http().Do(req)
		if err != nil {
			// The connection may have died with the host. Re-resolve next time.
			c.invalidate()
			return "", fmt.Errorf("IOA 发送失败：%w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// 404/410 mean the cached space or node id is gone (host restarted
			// with a new DB). Retry once with fresh ids; the message was not
			// accepted, so a retry cannot duplicate it.
			c.invalidate()
			if (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) && attempt == 0 {
				continue
			}
			return "", fmt.Errorf("IOA 发送失败（%s）：%s", resp.Status, clip(string(raw), 300))
		}
		var out message
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", fmt.Errorf("IOA 回复无法解析：%w", err)
		}
		if out.ID == "" {
			return "", errors.New("IOA 接受了请求但没有返回 message id")
		}
		return out.ID, nil
	}
	return "", errors.New("IOA 发送失败：缓存的 space/node id 已失效，重试后仍不可用")
}

// invalidate drops the cached ids after a failed send, so the next attempt
// resolves them again from the (possibly restarted) host.
func (c *Client) invalidate() {
	c.mu.Lock()
	c.space, c.sender, c.target = "", "", ""
	c.mu.Unlock()
}

// ids returns the resolved space/sender/target, resolving them on first use.
func (c *Client) ids(ctx context.Context) (space, sender, target string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.space == "" || c.sender == "" || c.target == "" {
		if err := c.resolve(ctx); err != nil {
			return "", "", "", err
		}
	}
	return c.space, c.sender, c.target, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) apiURL(path string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/ioa") {
		return base + path
	}
	return base + "/ioa" + path
}

// resolve chooses the space, sender node and target agent node. The target is
// the agent's node id (not its display name): IOA's refs.nodes compares ids.
func (c *Client) resolve(ctx context.Context) error {
	var spaces []space
	if err := c.get(ctx, "/spaces", &spaces); err != nil {
		return err
	}
	if len(spaces) == 0 {
		return errors.New("IOA 没有 space")
	}
	var nodes []node
	if err := c.get(ctx, "/nodes", &nodes); err != nil {
		return err
	}
	if len(nodes) == 0 {
		return errors.New("IOA 没有已注册节点")
	}

	target := c.pickTarget(nodes)
	if target == "" {
		return fmt.Errorf("IOA 里找不到 agent %q 对应的节点", c.Agent)
	}
	sender := c.pickSender(nodes, target)
	if sender == "" {
		return errors.New("IOA 里没有可作为发送方的节点（需要 cyber.web 或另一个非 agent 节点）")
	}

	chosen := ""
	for _, s := range spaces {
		if s.Name == "default" {
			chosen = s.ID
			break
		}
	}
	if chosen == "" {
		for _, s := range spaces {
			for _, n := range s.Nodes {
				if n.ID == target {
					chosen = s.ID
					break
				}
			}
			if chosen != "" {
				break
			}
		}
	}
	if chosen == "" {
		chosen = spaces[0].ID
	}
	c.space, c.sender, c.target = chosen, sender, target
	return nil
}

func (c *Client) pickTarget(nodes []node) string {
	if c.Agent != "" {
		for _, n := range nodes {
			if n.Name == c.Agent {
				return n.ID
			}
		}
		return ""
	}
	for _, n := range nodes {
		if n.Name == "local" {
			return n.ID
		}
	}
	return ""
}

func (c *Client) pickSender(nodes []node, target string) string {
	for _, n := range nodes {
		if n.Name == "cyber.web" && n.ID != target {
			return n.ID
		}
	}
	for _, n := range nodes {
		if n.ID != target {
			return n.ID
		}
	}
	return ""
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.getHeaders(ctx, path, out, nil)
}

func (c *Client) getHeaders(ctx context.Context, path string, out any, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(path), nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("IOA 请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &responseError{path: path, status: resp.StatusCode, detail: clip(string(raw), 200)}
	}
	// A resynchronization reads all retained history. Decode from the response
	// stream instead of truncating a successful snapshot at an arbitrary byte
	// boundary; the request context still bounds its lifetime.
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("IOA %s 回复无法解析：%w", path, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("回复包含多个 JSON 值")
		}
		return fmt.Errorf("IOA %s 回复无法解析：%w", path, err)
	}
	return nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
