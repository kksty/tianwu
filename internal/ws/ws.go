// Package ws is a small RFC 6455 websocket implementation: text messages only,
// no extensions, no compression, no subprotocols.
//
// It exists because both ends of the wire are ours and the standard library is
// enough (D29): the console's terminal/REPL bridge upgrades a browser socket,
// and the AOP client dials the harness application socket. The framing that
// matters — masking, fragmentation, ping/pong, close — is implemented here and
// pinned by the tests.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// MaxMessage is the largest message either side will assemble (8 MiB). A frame
// bigger than this closes the connection instead of growing the heap.
const MaxMessage = 8 << 20

// Write and handshake bounds. A peer that stops reading (suspended browser tab,
// half-open TCP) would otherwise block Write while holding the write mutex,
// which also blocks Close: the connection and its owner leak. Deadlines turn
// both into an error the caller can act on.
//
// Variables, not constants, so tests can shorten them.
var (
	writeTimeout     = 30 * time.Second
	handshakeTimeout = 15 * time.Second
)

// opcodes
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Conn is one websocket connection. It is safe for one reader and one writer at
// a time; writes are serialised internally.
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	client bool // client connections must mask their frames
	mu     sync.Mutex
	closed bool
}

// Upgrade turns an HTTP request into a websocket connection. The caller has
// already authenticated the request: Upgrade does not look at the token.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !headerHasToken(r.Header, "Connection", "upgrade") || !headerHasToken(r.Header, "Upgrade", "websocket") {
		return nil, errors.New("不是 websocket 升级请求")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return nil, errors.New("只支持 websocket 版本 13")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("缺少 Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("这个 ResponseWriter 不支持 hijack")
	}
	netConn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := netConn.Write([]byte(resp)); err != nil {
		_ = netConn.Close()
		return nil, err
	}
	return &Conn{conn: netConn, br: bufrw.Reader}, nil
}

// Dial opens a client connection to ws:// or wss://host/path.
func Dial(ctx context.Context, rawURL string, header http.Header) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("不支持的 scheme：%s", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if u.Scheme == "wss" {
		td := tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: u.Hostname()}}
		conn, err = td.DialContext(ctx, "tcp", host)
	} else {
		conn, err = d.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}

	// Bound the handshake itself: DialContext only covers the TCP connect. A
	// host that accepts and then stalls would otherwise hang the caller (and a
	// console handler) until the OS TCP timeout.
	deadline := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	req := &http.Request{
		Method:     http.MethodGet,
		URL:        &url.URL{Scheme: "http", Host: u.Host, Path: u.Path, RawQuery: u.RawQuery},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       u.Host,
		Header:     http.Header{},
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	_ = path
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}

	br := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("握手失败：HTTP %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != acceptKey(key) {
		_ = conn.Close()
		return nil, errors.New("握手失败：Accept 不匹配")
	}
	// Handshake is done; deadlines from here are per-write.
	_ = conn.SetDeadline(time.Time{})
	return &Conn{conn: conn, br: br, client: true}, nil
}

// ReadMessage returns the next complete text or binary message.
func (c *Conn) ReadMessage() ([]byte, error) {
	_, payload, err := c.ReadMessageType()
	return payload, err
}

// ReadMessageType is ReadMessage with the opcode: text frames are control
// (resize, close) and binary frames are terminal bytes.
func (c *Conn) ReadMessageType() (byte, []byte, error) {
	var buf []byte
	var messageOpcode byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch opcode {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, nil)
			return 0, nil, io.EOF
		case opText, opBinary:
			if buf != nil {
				return 0, nil, errors.New("上一条消息还没拼完就来了新消息")
			}
			messageOpcode = opcode
			buf = payload
		case opContinuation:
			if buf == nil {
				return 0, nil, errors.New("孤立的续帧")
			}
			buf = append(buf, payload...)
		default:
			return 0, nil, fmt.Errorf("不认识的 opcode %d", opcode)
		}
		if len(buf) > MaxMessage {
			return 0, nil, errors.New("消息超过上限")
		}
		if fin {
			return messageOpcode, buf, nil
		}
	}
}

// WriteMessage sends one text message.
func (c *Conn) WriteMessage(p []byte) error { return c.writeFrame(opText, p) }

// OpText is the text-frame opcode, exported for callers that read frames and
// need to tell control text from terminal bytes.
const OpText = opText

// WriteBinary sends one binary message. AOP frames are protobuf, so they need
// the binary opcode.
func (c *Conn) WriteBinary(p []byte) error { return c.writeFrame(opBinary, p) }

// Close closes the underlying connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.conn.SetDeadline(time.Now())
	return c.conn.Close()
}

// SetReadDeadline bounds how long ReadMessage may block (zero clears it).
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.br, head[:]); err != nil {
		return
	}
	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if length < 0 || length > MaxMessage {
		return false, 0, nil, errors.New("帧长度不合法")
	}
	if masked == c.client {
		// A client must mask; a server must not. Both are protocol errors.
		return false, 0, nil, errors.New("掩码方向不对")
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("连接已关闭")
	}
	// The write deadline is what makes Close() able to win the mutex: without
	// it a peer that stopped reading blocks this Write (and the lock) forever.
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	var head []byte
	head = append(head, 0x80|opcode)
	n := len(payload)
	maskBit := byte(0)
	if c.client {
		maskBit = 0x80
	}
	switch {
	case n < 126:
		head = append(head, maskBit|byte(n))
	case n <= 0xFFFF:
		head = append(head, maskBit|126, byte(n>>8), byte(n))
	default:
		head = append(head, maskBit|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}

	if !c.client {
		_, err := c.conn.Write(append(head, payload...))
		return err
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	head = append(head, mask[:]...)
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := c.conn.Write(append(head, masked...))
	return err
}

func acceptKey(key string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, key+guid)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
