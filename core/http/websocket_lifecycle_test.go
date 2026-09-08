package http

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	transport "github.com/coder/websocket"
)

func webSocketLifecycleWait(t *testing.T, done <-chan struct{}, event string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("missing event:", event)
	}
}

func webSocketLifecycleEmpty(h *WebSocket) <-chan struct{} {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	return h.state.empty
}

func TestWebSocketUnauthenticatedHandshakeNeverAccepts(t *testing.T) {
	var grants, messages atomic.Int32
	o := webSocketOptions(t)
	o.Authorize = func(*http.Request) error {
		grants.Add(1)
		return auth.ErrUnauthenticated
	}
	o.AuthorizeMessage = func(*http.Request, string, WebSocketEnvelope) error {
		messages.Add(1)
		return nil
	}
	h, server := webSocketOpen(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, response, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &transport.DialOptions{
		Subprotocols: []string{WebSocketProtocol},
		HTTPHeader:   http.Header{"Origin": {"https://app.example.test"}},
	})
	if c != nil {
		defer c.CloseNow()
		t.Fatal("unauthenticated connection accepted")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal("missing HTTP 401", response, err)
	}
	defer response.Body.Close()
	if response.Header.Get("Upgrade") != "" || response.Header.Get("Sec-Websocket-Accept") != "" || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("unsafe refusal headers", response.Header)
	}
	if grants.Load() != 1 || messages.Load() != 0 {
		t.Fatal("unexpected callbacks", grants.Load(), messages.Load())
	}
	webSocketLifecycleWait(t, webSocketLifecycleEmpty(h), "refused handshake releases permit")
}

// Retain the HTTP reader: a prompt server ping may share a TCP read with 101.
func webSocketLifecycleRaw(t *testing.T, address string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(address, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: socket.example.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: %s\r\nOrigin: https://app.example.test\r\n\r\n", WebSocketProtocol)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, httptest.NewRequest(http.MethodGet, address, nil))
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatal("handshake", response, err)
	}
	return c, reader
}

func webSocketLifecycleFrame(t *testing.T, reader *bufio.Reader) (byte, []byte) {
	t.Helper()
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		t.Fatal("server frame", err)
	}
	if header[0]&0xf0 != 0x80 || header[1]&0x80 != 0 {
		t.Fatal("unexpected fragmented, compressed, or masked server frame", header)
	}
	n := uint64(header[1] & 0x7f)
	if n == 126 {
		var size [2]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			t.Fatal(err)
		}
		n = uint64(binary.BigEndian.Uint16(size[:]))
	} else if n == 127 {
		var size [8]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			t.Fatal(err)
		}
		n = binary.BigEndian.Uint64(size[:])
	}
	if n > 4096 {
		t.Fatal("unexpected lifecycle fixture frame size", n)
	}
	payload := make([]byte, int(n))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	return header[0] & 0xf, payload
}

func TestWebSocketServerPingRequiresMatchingPong(t *testing.T) {
	for _, mode := range []string{"matching", "missing", "mismatched"} {
		t.Run(mode, func(t *testing.T) {
			o := webSocketOptions(t)
			o.PingInterval = 30 * time.Millisecond
			o.PongTimeout = 300 * time.Millisecond
			// Longer than the raw peer's outer deadline, so read-idle expiry
			// cannot accidentally satisfy the missing-pong close assertion.
			o.IdleTimeout = 10 * time.Second
			h, server := webSocketOpen(t, o)
			c, reader := webSocketLifecycleRaw(t, server.URL)
			empty := webSocketLifecycleEmpty(h)
			opcode, first := webSocketLifecycleFrame(t, reader)
			if opcode != 9 || len(first) == 0 {
				t.Fatal("server did not initiate ping", opcode, first)
			}
			if mode != "matching" {
				if mode == "mismatched" {
					webSocketMasked(t, c, 10, true, append([]byte("wrong:"), first...))
				}
				opcode, closePayload := webSocketLifecycleFrame(t, reader)
				if opcode != 8 || len(closePayload) < 2 || binary.BigEndian.Uint16(closePayload) != 1001 || string(closePayload[2:]) != "going away" {
					t.Fatal("unanswered server ping did not close safely", opcode, closePayload)
				}
				webSocketMasked(t, c, 8, true, closePayload)
				webSocketLifecycleWait(t, empty, "pong timeout releases connection")
				return
			}
			webSocketMasked(t, c, 10, true, first)
			// The writer cannot initiate its next ping until the matching pong
			// has been consumed by the independently running server reader.
			opcode, second := webSocketLifecycleFrame(t, reader)
			if opcode != 9 || string(first) == string(second) {
				t.Fatal("matching pong did not complete first ping", opcode, second)
			}
			webSocketMasked(t, c, 10, true, second)
			webSocketMasked(t, c, 1, true, []byte(`{"type":"echo","version":1,"payload":{"text":"still live"}}`))
			for range 8 {
				opcode, payload := webSocketLifecycleFrame(t, reader)
				if opcode == 9 {
					webSocketMasked(t, c, 10, true, payload)
					continue
				}
				if opcode != 1 || !strings.Contains(string(payload), `"text":"still live"`) {
					t.Fatal("connection unusable after matching pongs", opcode, payload)
				}
				return
			}
			t.Fatal("pings prevented the queued reply")
		})
	}
}

// A channel-backed transport stall proves that the real socket writer was
// entered. Only Close can unblock it; no elapsed-time/scheduler inference is used.
type webSocketLifecycleBlockedConn struct {
	net.Conn
	armed               atomic.Bool
	entered             chan []byte
	exited, closed      chan struct{}
	writeOnce, exitOnce sync.Once
	closeOnce           sync.Once
	closeErr            error
}

func (c *webSocketLifecycleBlockedConn) Write(p []byte) (int, error) {
	if !c.armed.Load() {
		return c.Conn.Write(p)
	}
	c.writeOnce.Do(func() { c.entered <- append([]byte(nil), p...) })
	<-c.closed
	c.exitOnce.Do(func() { close(c.exited) })
	return 0, net.ErrClosed
}

func (c *webSocketLifecycleBlockedConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		close(c.closed)
	})
	return c.closeErr
}

type webSocketLifecycleListener struct {
	net.Listener
	accepted chan *webSocketLifecycleBlockedConn
}

func (l *webSocketLifecycleListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &webSocketLifecycleBlockedConn{Conn: c, entered: make(chan []byte, 1), exited: make(chan struct{}), closed: make(chan struct{})}
	l.accepted <- wrapped
	return wrapped, nil
}

func TestWebSocketWriteDeadlineClosesObservedBlockedTransport(t *testing.T) {
	o := webSocketOptions(t)
	o.WriteTimeout = 100 * time.Millisecond
	var sends atomic.Int32
	writerContext := make(chan context.Context, 1)
	o.AuthorizeMessage = func(r *http.Request, action string, _ WebSocketEnvelope) error {
		if action == WebSocketSend && sends.Add(1) == 2 {
			writerContext <- r.Context()
		}
		return nil
	}
	h, err := NewWebSocket(o)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	listener := &webSocketLifecycleListener{Listener: server.Listener, accepted: make(chan *webSocketLifecycleBlockedConn, 1)}
	server.Listener = listener
	server.Start()
	var raw *webSocketLifecycleBlockedConn
	t.Cleanup(func() {
		if raw != nil {
			_ = raw.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
		server.Close()
	})
	c := webSocketDial(t, server.URL)
	select {
	case raw = <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("no accepted transport")
	}
	empty := webSocketLifecycleEmpty(h)
	raw.armed.Store(true) // 101 has completed; only subsequent socket writes stall.
	webSocketSend(t, c, `{"type":"echo","version":1,"payload":{"text":"blocked reply"}}`)
	select {
	case frame := <-raw.entered:
		if len(frame) < 2 || frame[0] != 0x81 || frame[1]&0x80 != 0 || !strings.Contains(string(frame), "blocked reply") {
			t.Fatal("witness was not the actual outgoing text frame", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket Write was never entered")
	}
	var ctx context.Context
	select {
	case ctx = <-writerContext:
	case <-time.After(time.Second):
		t.Fatal("writer grant did not execute")
	}
	webSocketLifecycleWait(t, raw.closed, "write deadline closes raw transport")
	webSocketLifecycleWait(t, raw.exited, "blocked Write returns after close")
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("closure was not caused by the writer deadline", ctx.Err())
	}
	data, err := webSocketReceive(t, c)
	if err == nil || len(data) != 0 || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("client did not observe the server-side transport closure", data, err)
	}
	webSocketLifecycleWait(t, empty, "all blocked-write pumps release the permit")
}

func TestWebSocketPeerDisconnectCancelsActiveHandler(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan error, 1)
	o := webSocketOptions(t)
	o.MaxConnections = 1
	o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(ctx context.Context, _ webSocketInput) (webSocketOutput, error) {
		close(entered)
		<-ctx.Done()
		canceled <- ctx.Err()
		return webSocketOutput{}, ctx.Err()
	})}
	h, server := webSocketOpen(t, o)
	c := webSocketDial(t, server.URL)
	empty := webSocketLifecycleEmpty(h)
	webSocketSend(t, c, `{"type":"echo","version":1,"payload":{}}`)
	webSocketLifecycleWait(t, entered, "handler entered before peer disconnect")
	if err := c.CloseNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-canceled:
		if err != context.Canceled {
			t.Fatal("peer disconnect did not cancel handler", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler context survived peer disconnect")
	}
	webSocketLifecycleWait(t, empty, "peer disconnect joins handler and pumps")
	// Shutdown has not been called: the released sole permit is reusable.
	_ = webSocketDial(t, server.URL)
}

func TestWebSocketDefaultMessageLimitExactBoundary(t *testing.T) {
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Length int `json:"length"`
	}
	const limit = 1 << 20
	const prefix = `{"type":"boundary","version":1,"payload":{"text":"`
	const suffix = `"}}`
	for _, size := range []int{limit, limit + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var effects atomic.Int32
			d, err := WebSocketMessage("boundary", 1, func(context.Context, input) error { return nil }, func(_ context.Context, value input) (output, error) {
				effects.Add(1)
				return output{Length: len(value.Text)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			o := webSocketOptions(t)
			o.Messages = []WebSocketMessageDefinition{d}
			if o.MaxMessageBytes != 0 {
				t.Fatal("fixture accidentally configured an explicit limit")
			}
			h, server := webSocketOpen(t, o)
			if h.config.options.MaxMessageBytes != limit {
				t.Fatal("incorrect default", h.config.options.MaxMessageBytes)
			}
			c := webSocketDial(t, server.URL)
			value := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
			if len(value) != size {
				t.Fatal("incorrect boundary fixture")
			}
			webSocketSend(t, c, value)
			data, err := webSocketReceive(t, c)
			if size > limit {
				if transport.CloseStatus(err) != 1009 || len(data) != 0 || effects.Load() != 0 {
					t.Fatal("default overflow executed or emitted", err, effects.Load())
				}
				return
			}
			var reply struct {
				Payload output `json:"payload"`
			}
			if err != nil || json.Unmarshal(data, &reply) != nil || reply.Payload.Length != size-len(prefix)-len(suffix) || effects.Load() != 1 {
				t.Fatal("exact default limit rejected or altered", string(data), err, effects.Load())
			}
		})
	}
}
