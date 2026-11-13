package main

import "testing"

// A second tw on the same console address must fail while reserving the socket,
// before the orphan sweep can kill the running instance's host (D112).
func TestConsoleListenerRejectsASecondInstance(t *testing.T) {
	first, err := listenConsole("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := listenConsole(first.Addr().String())
	if err == nil {
		_ = second.Close()
		t.Fatalf("a second instance could bind the console address %s", first.Addr())
	}
}
