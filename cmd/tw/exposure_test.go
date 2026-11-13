package main

import "testing"

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		"127.0.0.1":      true,
		"0.0.0.0:8080":   false,
		":8080":          false,
		"10.0.0.5:8080":  false,
		"192.168.1.9:80": false,
	} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestIsPlaintextRemote(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"http://10.0.0.5:18766":    true,
		"http://hub.example":       true,
		"10.0.0.5:18766":           true,  // no scheme: still plaintext
		"https://hub.example":      false, // TLS
		"http://127.0.0.1:8080":    false, // token never leaves the host
		"http://localhost:8080":    false,
		"http://[::1]:8080":        false,
		"wss://hub.example/aop":    false,
		"":                         false,
		"not a url with spaces ::": false,
	} {
		if got := isPlaintextRemote(endpoint); got != want {
			t.Errorf("isPlaintextRemote(%q) = %v, want %v", endpoint, got, want)
		}
	}
}
