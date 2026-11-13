package ws

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEchoRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage([]byte("echo:" + string(msg))); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, want := range []string{"hello", strings.Repeat("x", 70_000)} {
		if err := c.WriteMessage([]byte(want)); err != nil {
			t.Fatal(err)
		}
		got, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "echo:"+want {
			t.Fatalf("echo mismatch: %d bytes back", len(got))
		}
	}
}

func TestUpgradeRejectsPlainGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Upgrade(w, r); err == nil {
			t.Fatal("plain GET must not upgrade")
		}
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestPingIsAnsweredTransparently(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		// Server sends a ping, then a text message: the client must skip the
		// ping without surfacing it.
		_ = c.writeFrame(opPing, []byte("are you there"))
		_ = c.WriteMessage([]byte("after-ping"))
		_, _ = c.ReadMessage() // wait for the pong (or the close)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(msg) != "after-ping" {
		t.Fatalf("got %q", msg)
	}
}

func TestServerRejectsMaskedClientFrame(t *testing.T) {
	// A server that receives a masked frame is being spoken to by a client that
	// got its role backwards; the connection must fail loudly, not decode junk.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := c.ReadMessage(); err == nil {
			t.Fatal("masked frame from a client was accepted")
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Hand-write an unmasked frame: that is exactly the server's rule being
	// broken from the client side.
	c.mu.Lock()
	_, _ = c.conn.Write([]byte{0x81, 0x02, 'h', 'i'})
	c.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
}

// A peer that stops reading must not block Write forever (and, through the
// write mutex, must not block Close either).
func TestWriteTimesOutWhenPeerDoesNotRead(t *testing.T) {
	old := writeTimeout
	writeTimeout = 80 * time.Millisecond
	defer func() { writeTimeout = old }()

	a, b := net.Pipe()
	defer b.Close()
	c := &Conn{conn: a}

	start := time.Now()
	err := c.WriteBinary(make([]byte, 1<<20))
	if err == nil {
		t.Fatal("a write to a stalled peer must fail, not block")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("write blocked for %s", d)
	}
}

func TestCloseDoesNotWaitForAStalledWrite(t *testing.T) {
	old := writeTimeout
	writeTimeout = 150 * time.Millisecond
	defer func() { writeTimeout = old }()

	a, b := net.Pipe()
	defer b.Close()
	c := &Conn{conn: a}
	go func() { _ = c.WriteBinary(make([]byte, 1<<20)) }()
	time.Sleep(20 * time.Millisecond) // let the write take the mutex

	start := time.Now()
	_ = c.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close waited %s for a stalled write", d)
	}
}

// A host that accepts the TCP connection and never answers the upgrade must not
// hold the caller past the handshake deadline (DialContext only covers connect).
func TestDialHandshakeIsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(2 * time.Second) // accept, never respond
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Dial(ctx, "ws://"+ln.Addr().String()+"/x", nil); err == nil {
		t.Fatal("handshake against a mute host must fail")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("handshake blocked for %s", d)
	}
}
