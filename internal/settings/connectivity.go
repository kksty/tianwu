package settings

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/modelclient"
)

// Credentials are deliberately absent from the public binding revision. A
// private probe-cache key must nevertheless change when the selected key does.
func profileProbeKey(p Profile, selectedKey string) string {
	key := sha256.Sum256([]byte(selectedKey))
	return fmt.Sprintf("%s|%s|%x", p.Revision(), p.BaseURL, key)
}

// CheckProfile is explicit, one-shot inference. Listing profiles never calls it.
// Its short-lived verdict shares the admission cache with directory checks, so
// a successful real request takes precedence over an unavailable model list.
func (s *Store) CheckProfile(ctx context.Context, id string) ModelProbe {
	p, err := s.SavedProfile(id)
	if err != nil {
		return ModelProbe{Kind: "connectivity", Error: err.Error()}
	}
	if p.Paused {
		return ModelProbe{Kind: "connectivity", Error: "模型配置已暂停：先恢复使用再检测"}
	}
	key := s.currentKey(p)
	cacheKey := profileProbeKey(p, key)
	c, err := modelclient.NewWithProvider(p.BaseURL, key, p.Model, p.Provider)
	if err != nil {
		return ModelProbe{Kind: "connectivity", Error: err.Error()}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if p.Proxy != "" {
		proxy, err := url.Parse(p.Proxy)
		if err != nil {
			return ModelProbe{Kind: "connectivity", Error: "代理地址无效"}
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	defer transport.CloseIdleConnections()
	c.HTTP = &http.Client{Transport: transport, Timeout: time.Minute}
	// One short response; no tools, project context, sessions or automatic retry.
	c.MaxTokens = 256
	if p.MaxTokens > 0 && p.MaxTokens < c.MaxTokens {
		c.MaxTokens = p.MaxTokens
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	started := time.Now()
	res, err := c.Check(ctx)
	probe := ModelProbe{Kind: "connectivity", Probed: true, At: time.Now(), MS: time.Since(started).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		for _, secret := range s.SecretValues() {
			if secret != "" {
				probe.Error = strings.ReplaceAll(probe.Error, secret, "***")
			}
		}
	} else {
		probe.Available = true
		probe.ReturnedModel = res.ReturnedModel
	}
	s.probeMu.Lock()
	if s.probeCache == nil {
		s.probeCache = map[string]probeEntry{}
	}
	s.probeCache[cacheKey] = probeEntry{probe: probe, expires: probe.At.Add(probeTTL)}
	s.probeMu.Unlock()
	return probe
}
