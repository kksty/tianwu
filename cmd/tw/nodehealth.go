package main

// Node health for the 态势 page.
//
// A configured node is not necessarily reachable, and the ledger cannot tell
// the difference: "bound" only means a human allowed dispatch. This prober asks
// the question directly — a host answers ListAgents, an audit node is healthy
// when its binary exists — and caches the answer briefly so a 3-second page
// poll does not hammer every host.

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/aop"
	"github.com/kksty/tianwu/internal/fleet"
	"github.com/kksty/tianwu/internal/node"
)

const (
	nodeHealthTTL    = 10 * time.Second
	nodeProbeTimeout = 1500 * time.Millisecond
)

type nodeProber struct {
	reg *node.Registry

	mu    sync.Mutex
	cache map[string]fleet.Health
	at    time.Time
}

func newNodeProber(reg *node.Registry) *nodeProber {
	return &nodeProber{reg: reg, cache: map[string]fleet.Health{}}
}

// Health returns one health record per configured node. Probes run in
// parallel and each is bounded by nodeProbeTimeout, so one dead host cannot
// hold the whole page for long.
func (p *nodeProber) Health(parent context.Context) map[string]fleet.Health {
	ids := p.reg.IDs()

	p.mu.Lock()
	if !p.at.IsZero() && time.Since(p.at) < nodeHealthTTL && len(p.cache) == len(ids) {
		out := cloneHealth(p.cache)
		p.mu.Unlock()
		return out
	}
	p.mu.Unlock()

	out := make(map[string]fleet.Health, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		spec, ok := p.reg.Spec(id)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(id string, spec node.Spec) {
			defer wg.Done()
			health := probeNode(parent, spec)
			mu.Lock()
			out[id] = health
			mu.Unlock()
		}(id, spec)
	}
	wg.Wait()

	// A page refresh cancels its request; do not freeze "context canceled" into
	// the cache as if the host were down.
	if parent.Err() != nil {
		return out
	}

	p.mu.Lock()
	p.cache, p.at = out, time.Now()
	p.mu.Unlock()
	return out
}

func cloneHealth(in map[string]fleet.Health) map[string]fleet.Health {
	out := make(map[string]fleet.Health, len(in))
	for id, h := range in {
		out[id] = h
	}
	return out
}

func probeNode(parent context.Context, spec node.Spec) fleet.Health {
	start := time.Now()
	h := fleet.Health{CheckedAt: &start}

	if spec.AOP != "" {
		ctx, cancel := context.WithTimeout(parent, nodeProbeTimeout)
		defer cancel()
		agents, err := aop.New(spec.AOP, spec.Token).Agents(ctx)
		h.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			h.Error = err.Error()
			return h
		}
		h.Online = true
		h.Agents = len(agents)
		return h
	}

	// An audit node is a local process started on demand: its health is "the
	// binary is there", not "a host is answering". Every execution node is a
	// host (D64) and is probed above.
	if spec.Bin == "" {
		h.Error = "没配 bin"
		return h
	}
	if st, err := os.Stat(spec.Bin); err != nil || st.IsDir() {
		h.Error = "找不到 harness 二进制：" + spec.Bin
		return h
	}
	h.Online = true
	h.LatencyMS = time.Since(start).Milliseconds()
	return h
}
