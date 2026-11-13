package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownClosesListenerEvenWhenCleanupBlocks(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler()}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	blocked, done := make(chan struct{}), make(chan struct{})
	defer close(blocked)
	go func() { shutdownConsole(srv, 30*time.Millisecond, func() { <-blocked }); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup trapped shutdown")
	}
	select {
	case err := <-served:
		if err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener stayed open")
	}
}
