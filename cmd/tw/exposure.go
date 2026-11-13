package main

import (
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/kksty/tianwu/internal/node"
)

// warnExposure reports the two facts that decide how exposed this instance is:
// where the console listens, and whether any AOP endpoint is plaintext. It only
// logs. On a trusted LAN the token is still the gate, and refusing to start would
// break the 0.0.0.0 deployments distributed nodes need (D60).
func warnExposure(addr string, nodes *node.Registry) {
	if !isLoopbackAddr(addr) {
		log.Printf("警告：控制台监听在 %s（非 loopback）且没有 TLS；控制台令牌 = 节点 shell 权限，只应放在可信网段，对外请套 TLS 反代", addr)
	}
	if nodes == nil {
		return
	}
	for _, n := range nodes.Infos() {
		if n.Transport != "aop" {
			continue
		}
		if isPlaintextRemote(n.Endpoint) {
			log.Printf("警告：节点 %s 的 AOP 端点是明文 %s；节点注册 token 会以 Authorization 头明文经过网络", n.ID, n.Endpoint)
		}
	}
}

// isLoopbackAddr answers "does this listen address restrict the socket to this
// host?". An empty host means every interface, which is not loopback.
func isLoopbackAddr(addr string) bool {
	addr = strings.TrimSpace(addr)
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isPlaintextRemote reports an AOP endpoint whose token crosses the network in
// clear text. Loopback endpoints are exempt: the token never leaves the host.
func isPlaintextRemote(endpoint string) bool {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// A bare "host:port" is accepted by some older nodes.json files; parse it
		// as a host so it is warned about rather than silently skipped.
		if alt, altErr := url.Parse("//" + raw); altErr == nil {
			u, err = alt, nil
		}
	}
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "wss") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}
