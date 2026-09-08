package http

import (
	"bufio"
	"context"
	"encoding/binary"
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

func webSocketOptions(t testing.TB) WebSocketOptions {
	return WebSocketOptions{Origins: []string{"https://app.example.test"}, Authorize: func(*http.Request) error { return nil }, AuthorizeMessage: func(*http.Request, string, WebSocketEnvelope) error { return nil }, Messages: []WebSocketMessageDefinition{webSocketDefinition(t, nil)}, CloseTimeout: 20 * time.Millisecond}
}

type webSocketBrokenWriter struct {
	header          http.Header
	headers, writes int
	mode            string
}

func (w *webSocketBrokenWriter) Header() http.Header { return w.header }
func (w *webSocketBrokenWriter) WriteHeader(int) {
	w.headers++
	if w.mode == "header panic" {
		panic("private writer")
	}
}
func (w *webSocketBrokenWriter) Write(value []byte) (int, error) {
	w.writes++
	switch w.mode {
	case "write panic":
		panic("private writer")
	case "short":
		return len(value) - 1, nil
	default:
		return 0, errors.New("private transfer")
	}
}
func TestWebSocketEarlyErrorTransferNeverAppendsAnotherResponse(t *testing.T) {
	h, err := NewWebSocket(webSocketOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"header panic", "write panic", "short", "error"} {
		t.Run(mode, func(t *testing.T) {
			w := &webSocketBrokenWriter{header: make(http.Header), mode: mode}
			func() {
				defer func() {
					if recover() != http.ErrAbortHandler {
						t.Error("missing abort")
					}
				}()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/socket/", nil))
			}()
			if w.headers != 1 || w.writes > 1 {
				t.Fatal("appended response", w.headers, w.writes)
			}
		})
	}
}

func TestWebSocketInboundBackpressureCancelsCurrentHandler(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	o := webSocketOptions(t)
	o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(ctx context.Context, _ webSocketInput) (webSocketOutput, error) {
		close(entered)
		<-release
		return webSocketOutput{}, ctx.Err()
	})}
	_, server := webSocketOpen(t, o)
	c := webSocketDial(t, server.URL)
	raw := `{"type":"echo","version":1,"payload":{}}`
	webSocketSend(t, c, raw)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not entered")
	}
	webSocketSend(t, c, raw)
	webSocketSend(t, c, raw)
	_, err := webSocketReceive(t, c)
	if transport.CloseStatus(err) != 1013 {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
}

func TestWebSocketOutboundBackpressureAndLateSessionRevocation(t *testing.T) {
	t.Run("outbound", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		defer once.Do(func() { close(release) })
		var sends, effects atomic.Int32
		second, third := make(chan struct{}), make(chan struct{})
		o := webSocketOptions(t)
		o.OutboundQueue = 1
		o.AuthorizeMessage = func(_ *http.Request, action string, _ WebSocketEnvelope) error {
			if action == WebSocketSend && sends.Add(1) == 2 {
				close(entered)
				<-release
			}
			return nil
		}
		o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(context.Context, webSocketInput) (webSocketOutput, error) {
			n := effects.Add(1)
			if n == 2 {
				close(second)
			}
			if n == 3 {
				close(third)
			}
			return webSocketOutput{}, nil
		})}
		_, server := webSocketOpen(t, o)
		c := webSocketDial(t, server.URL)
		raw := `{"type":"echo","version":1,"payload":{}}`
		webSocketSend(t, c, raw)
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("writer not entered")
		}
		webSocketSend(t, c, raw)
		select {
		case <-second:
		case <-time.After(time.Second):
			t.Fatal("second effect missing")
		}
		webSocketSend(t, c, raw)
		select {
		case <-third:
		case <-time.After(time.Second):
			t.Fatal("third effect missing")
		}
		_, err := webSocketReceive(t, c)
		if transport.CloseStatus(err) != 1013 {
			t.Fatal(err)
		}
		once.Do(func() { close(release) })
	})
	t.Run("current session", func(t *testing.T) {
		var allowed atomic.Bool
		allowed.Store(true)
		var effects atomic.Int32
		o := webSocketOptions(t)
		o.Authorize = func(*http.Request) error {
			if !allowed.Load() {
				return auth.ErrUnauthenticated
			}
			return nil
		}
		o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(context.Context, webSocketInput) (webSocketOutput, error) {
			effects.Add(1)
			return webSocketOutput{}, nil
		})}
		_, server := webSocketOpen(t, o)
		c := webSocketDial(t, server.URL)
		raw := `{"type":"echo","version":1,"payload":{}}`
		webSocketSend(t, c, raw)
		if _, err := webSocketReceive(t, c); err != nil {
			t.Fatal(err)
		}
		allowed.Store(false)
		webSocketSend(t, c, raw)
		_, err := webSocketReceive(t, c)
		if transport.CloseStatus(err) != 1008 || effects.Load() != 1 {
			t.Fatal(err, effects.Load())
		}
	})
}

func TestWebSocketOversizeAndReturnedOutputOwnership(t *testing.T) {
	t.Run("oversize input", func(t *testing.T) {
		o := webSocketOptions(t)
		o.MaxMessageBytes = 128
		_, server := webSocketOpen(t, o)
		c := webSocketDial(t, server.URL)
		webSocketSend(t, c, `{"type":"echo","version":1,"payload":{"text":"`+strings.Repeat("x", 200)+`"}}`)
		_, err := webSocketReceive(t, c)
		if transport.CloseStatus(err) != 1009 {
			t.Fatal(err)
		}
	})
	t.Run("captured output and options", func(t *testing.T) {
		type output struct {
			Values []string `json:"values"`
		}
		retained := []string{"original"}
		d, err := WebSocketMessage("echo", 1, func(context.Context, webSocketInput) error { return nil }, func(context.Context, webSocketInput) (output, error) { return output{retained}, nil })
		if err != nil {
			t.Fatal(err)
		}
		o := webSocketOptions(t)
		o.Messages = []WebSocketMessageDefinition{d}
		o.AuthorizeMessage = func(_ *http.Request, action string, _ WebSocketEnvelope) error {
			if action == WebSocketSend {
				retained[0] = "private replacement"
			}
			return nil
		}
		_, server := webSocketOpen(t, o)
		o.Origins[0] = "https://attacker.example"
		o.Messages[0] = WebSocketMessageDefinition{}
		o.Authorize = func(*http.Request) error { return auth.ErrPermissionDenied }
		c := webSocketDial(t, server.URL)
		webSocketSend(t, c, `{"type":"echo","version":1,"payload":{}}`)
		data, err := webSocketReceive(t, c)
		if err != nil || !strings.Contains(string(data), `"original"`) || strings.Contains(string(data), "private") {
			t.Fatal(string(data), err)
		}
	})
}

func TestWebSocketShutdownDuringHandshakeNeverAccepts(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	o := webSocketOptions(t)
	o.Authorize = func(*http.Request) error { close(entered); <-release; return nil }
	h, server := webSocketOpen(t, o)
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c, response, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &transport.DialOptions{Subprotocols: []string{WebSocketProtocol}, HTTPHeader: http.Header{"Origin": {"https://app.example.test"}}})
		if c != nil {
			c.CloseNow()
		}
		code := 0
		if response != nil {
			code = response.StatusCode
		}
		done <- result{code, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("grant did not begin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case got := <-done:
		if got.code != 503 || got.err == nil {
			t.Fatal("upgrade escaped drain", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake did not finish")
	}
}

func TestWebSocketCallbackCancellationPrecedesDomainOrNormalClose(t *testing.T) {
	for _, mode := range []string{"validation", "handler normal close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o := webSocketOptions(t)
			d, err := WebSocketMessage("echo", 1, func(context.Context, webSocketInput) error {
				if mode == "validation" {
					cancel()
					return ErrWebSocketInvalidMessage
				}
				return nil
			}, func(context.Context, webSocketInput) (webSocketOutput, error) {
				cancel()
				return webSocketOutput{}, ErrWebSocketClose
			})
			if err != nil {
				t.Fatal(err)
			}
			o.Messages = []WebSocketMessageDefinition{d}
			h, err := NewWebSocket(o)
			if err != nil {
				t.Fatal(err)
			}
			s := &webSocketSession{config: h.config, request: webSocketHandshakeRequest(), ctx: ctx, cancel: cancel, closing: make(chan struct{}), outbound: make(chan webSocketPacket, 1)}
			if code := s.dispatch([]byte(`{"type":"echo","version":1,"payload":{}}`)); code != 1011 {
				t.Fatal("canceled callback invented domain/normal outcome", code)
			}
		})
	}
}

func webSocketRaw(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(address, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: socket.example.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: %s\r\nOrigin: https://app.example.test\r\n\r\n", WebSocketProtocol)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), httptest.NewRequest("GET", address, nil))
	if err != nil || response.StatusCode != 101 {
		t.Fatal(response, err)
	}
	return conn
}
func webSocketMasked(t *testing.T, conn net.Conn, opcode byte, fin bool, payload []byte) {
	t.Helper()
	first := opcode
	if fin {
		first |= 0x80
	}
	header := []byte{first, 0x80 | byte(len(payload))}
	if len(payload) > 125 {
		t.Fatal("test frame too large")
	}
	mask := []byte{1, 2, 3, 4}
	header = append(header, mask...)
	for i, c := range payload {
		header = append(header, c^mask[i%4])
	}
	if _, err := conn.Write(header); err != nil {
		t.Fatal(err)
	}
}
func TestWebSocketControlRateAndPartialMessageDeadline(t *testing.T) {
	t.Run("ping rate", func(t *testing.T) {
		o := webSocketOptions(t)
		o.Burst = 2
		o.MessagesPerSecond = 1
		_, server := webSocketOpen(t, o)
		conn := webSocketRaw(t, server.URL)
		for range 4 {
			webSocketMasked(t, conn, 9, true, nil)
		}
		for range 4 {
			var header [2]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				t.Fatal(err)
			}
			payload := make([]byte, int(header[1]&0x7f))
			if _, err := io.ReadFull(conn, payload); err != nil {
				t.Fatal(err)
			}
			if header[0]&0xf == 8 {
				if len(payload) < 2 || binary.BigEndian.Uint16(payload) != 1008 {
					t.Fatal(payload)
				}
				return
			}
		}
		t.Fatal("control flood did not close")
	})
	t.Run("unfinished fragments", func(t *testing.T) {
		o := webSocketOptions(t)
		o.IdleTimeout = 30 * time.Millisecond
		_, server := webSocketOpen(t, o)
		conn := webSocketRaw(t, server.URL)
		webSocketMasked(t, conn, 1, false, []byte("{"))
		webSocketMasked(t, conn, 0, false, nil)
		var b [1]byte
		if _, err := conn.Read(b[:]); err == nil {
			t.Fatal("unfinished message retained transport")
		}
	})
}
func webSocketOpen(t testing.TB, options WebSocketOptions) (*WebSocket, *httptest.Server) {
	t.Helper()
	handler, err := NewWebSocket(options)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = handler.Shutdown(ctx)
		server.Close()
	})
	return handler, server
}
func webSocketDial(t testing.TB, serverURL string) *transport.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, _, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http"), &transport.DialOptions{Subprotocols: []string{WebSocketProtocol}, HTTPHeader: http.Header{"Origin": {"https://app.example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	return connection
}
func webSocketSend(t testing.TB, c *transport.Conn, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Write(ctx, transport.MessageText, []byte(text)); err != nil {
		t.Fatal(err)
	}
}
func webSocketReceive(t testing.TB, c *transport.Conn) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	return data, err
}

func TestWebSocketHandshakeAndTypedExchange(t *testing.T) {
	options := webSocketOptions(t)
	var receives, sends atomic.Int32
	options.AuthorizeMessage = func(r *http.Request, action string, envelope WebSocketEnvelope) error {
		if r.TLS != nil || r.Body != http.NoBody || r.PathValue("private") != "" {
			t.Error("raw request state leaked")
		}
		if action == WebSocketReceive {
			receives.Add(1)
		} else if action == WebSocketSend {
			sends.Add(1)
		} else {
			t.Error(action)
		}
		envelope.Payload[0] = '['
		r.Header.Set("Origin", "https://attacker.example")
		return nil
	}
	_, server := webSocketOpen(t, options)
	c := webSocketDial(t, server.URL)
	webSocketSend(t, c, `{"type":"echo","version":1,"payload":{"text":"hello","number":9007199254740993}}`)
	data, err := webSocketReceive(t, c)
	if err != nil || !strings.Contains(string(data), `"text":"hello"`) || !strings.Contains(string(data), `9007199254740993`) || receives.Load() != 2 || sends.Load() != 2 {
		t.Fatal(string(data), err, receives.Load(), sends.Load())
	}
}

func webSocketHandshakeRequest() *http.Request {
	r := httptest.NewRequest("GET", "http://socket.example.test/socket/", nil)
	r.Header = http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Protocol": {WebSocketProtocol}, "Origin": {"https://app.example.test"}}
	return r
}
func TestWebSocketHandshakeRefusalsAndConfiguration(t *testing.T) {
	for _, mode := range []string{"origin", "missing origin", "null origin", "duplicate origin", "key", "duplicate key", "version", "protocol", "method", "body", "HTTP2", "header case", "header body", "unavailable hijacker", "denied", "panic"} {
		t.Run(mode, func(t *testing.T) {
			o := webSocketOptions(t)
			calls := 0
			o.Authorize = func(*http.Request) error {
				calls++
				if mode == "denied" {
					return auth.ErrPermissionDenied
				}
				if mode == "panic" {
					panic("private panic")
				}
				return nil
			}
			r := webSocketHandshakeRequest()
			want := 403
			switch mode {
			case "origin":
				r.Header.Set("Origin", "https://attacker.example")
			case "missing origin":
				r.Header.Del("Origin")
			case "null origin":
				r.Header.Set("Origin", "null")
			case "duplicate origin":
				r.Header.Add("Origin", "https://app.example.test")
			case "key":
				r.Header.Set("Sec-WebSocket-Key", "invalid")
			case "duplicate key":
				r.Header.Add("Sec-WebSocket-Key", r.Header.Get("Sec-WebSocket-Key"))
			case "version":
				r.Header.Set("Sec-WebSocket-Version", "12")
			case "protocol":
				r.Header.Set("Sec-WebSocket-Protocol", "GOGO.JSON.V1")
			case "method":
				r.Method = "POST"
				want = 405
			case "body":
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), http.NoBody, 1)
				want = 400
			case "HTTP2":
				r.ProtoMajor = 2
				want = 400
			case "header case":
				r.Header["origin"] = []string{"https://app.example.test"}
				want = 400
			case "header body":
				r.Header.Set("Content-Length", "1")
				want = 400
			case "unavailable hijacker":
				want = 503
			case "denied":
				want = 403
			case "panic":
				want = 503
			}
			h, err := NewWebSocket(o)
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			var w http.ResponseWriter = out
			if mode == "denied" || mode == "panic" {
				w = &webSocketFakeHijacker{ResponseRecorder: out}
			}
			h.ServeHTTP(w, r)
			if out.Code != want || strings.Contains(out.Body.String(), "private") || out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(out.Code, out.Body.String(), calls)
			}
		})
	}
	for _, mutate := range []func(*WebSocketOptions){func(o *WebSocketOptions) { o.Authorize = nil }, func(o *WebSocketOptions) { o.AuthorizeMessage = nil }, func(o *WebSocketOptions) { o.Messages = nil }, func(o *WebSocketOptions) { o.Origins = []string{"*"} }, func(o *WebSocketOptions) { o.OutboundQueue = 33 }, func(o *WebSocketOptions) { o.MaxMessageBytes = 9 << 20 }, func(o *WebSocketOptions) { o.CloseTimeout = -1 }} {
		o := webSocketOptions(t)
		mutate(&o)
		if _, err := NewWebSocket(o); err != ErrWebSocketConfiguration {
			t.Fatal("accepted configuration", err)
		}
	}
}

type webSocketFakeHijacker struct{ *httptest.ResponseRecorder }

func (*webSocketFakeHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("private hijack error")
}
func (*webSocketFakeHijacker) SetWriteDeadline(time.Time) error { return nil }

func TestWebSocketDoesNotWriteAnotherResponseAfterUpgradeFailure(t *testing.T) {
	o := webSocketOptions(t)
	h, err := NewWebSocket(o)
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(&webSocketFakeHijacker{out}, webSocketHandshakeRequest())
	if out.Code != 101 || out.Body.Len() != 0 {
		t.Fatal("second response", out.Code, out.Body.String())
	}
}
func TestWebSocketMissingOriginOptInAndExactNegotiation(t *testing.T) {
	o := webSocketOptions(t)
	o.AllowMissingOrigin = true
	_, server := webSocketOpen(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, _, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &transport.DialOptions{Subprotocols: []string{"GOGO.JSON.V1", WebSocketProtocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if c.Subprotocol() != WebSocketProtocol {
		t.Fatal(c.Subprotocol())
	}
}

func TestWebSocketMalformedMessagesNeverExecute(t *testing.T) {
	for _, raw := range []string{`{"type":"missing","version":1,"payload":{}}`, `{"type":"echo","version":2,"payload":{}}`, `{"type":"echo","version":1,"payload":{"Text":"secret"}}`, `{"type":"echo","version":1,"payload":{"text":"a","te\u0078t":"b"}}`, `{"type":"echo","version":1,"payload":{"text":"\ud800"}}`} {
		t.Run(raw, func(t *testing.T) {
			var calls atomic.Int32
			o := webSocketOptions(t)
			o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(context.Context, webSocketInput) (webSocketOutput, error) {
				calls.Add(1)
				return webSocketOutput{}, nil
			})}
			_, server := webSocketOpen(t, o)
			c := webSocketDial(t, server.URL)
			webSocketSend(t, c, raw)
			_, err := webSocketReceive(t, c)
			if transport.CloseStatus(err) != 1007 || calls.Load() != 0 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func TestWebSocketFinalSendDenialAndHandlerOutcomes(t *testing.T) {
	for _, mode := range []string{"receive", "queued send", "writer send", "handler error", "handler panic", "normal"} {
		t.Run(mode, func(t *testing.T) {
			var calls, sends atomic.Int32
			o := webSocketOptions(t)
			o.AuthorizeMessage = func(_ *http.Request, action string, _ WebSocketEnvelope) error {
				if action == WebSocketReceive && mode == "receive" {
					return auth.ErrPermissionDenied
				}
				if action == WebSocketSend {
					n := sends.Add(1)
					if mode == "queued send" || mode == "writer send" && n == 2 {
						return auth.ErrPermissionDenied
					}
				}
				return nil
			}
			o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(context.Context, webSocketInput) (webSocketOutput, error) {
				calls.Add(1)
				switch mode {
				case "handler error":
					return webSocketOutput{}, errors.New("private error")
				case "handler panic":
					panic("private panic")
				case "normal":
					return webSocketOutput{}, ErrWebSocketClose
				}
				return webSocketOutput{Text: "secret"}, nil
			})}
			_, server := webSocketOpen(t, o)
			c := webSocketDial(t, server.URL)
			webSocketSend(t, c, `{"type":"echo","version":1,"payload":{}}`)
			data, err := webSocketReceive(t, c)
			want := 1008
			if mode == "normal" {
				want = 1000
			}
			if strings.HasPrefix(mode, "handler") {
				want = 1011
			}
			if int(transport.CloseStatus(err)) != want || len(data) != 0 || strings.Contains(err.Error(), "private") || mode == "receive" && calls.Load() != 0 {
				t.Fatal(string(data), err, calls.Load())
			}
		})
	}
}

func TestWebSocketShutdownRetainsNoncooperativePermit(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	o := webSocketOptions(t)
	o.MaxConnections = 1
	o.HandlerTimeout = 30 * time.Millisecond
	o.Messages = []WebSocketMessageDefinition{webSocketDefinition(t, func(ctx context.Context, _ webSocketInput) (webSocketOutput, error) {
		close(entered)
		<-release
		defer close(returned)
		return webSocketOutput{}, nil
	})}
	h, server := webSocketOpen(t, o)
	c := webSocketDial(t, server.URL)
	webSocketSend(t, c, `{"type":"echo","version":1,"payload":{}}`)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not entered")
	}
	_, err := webSocketReceive(t, c)
	if err == nil {
		t.Fatal("deadline did not close transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	extra, response, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &transport.DialOptions{Subprotocols: []string{WebSocketProtocol}, HTTPHeader: http.Header{"Origin": {"https://app.example.test"}}})
	if extra != nil {
		extra.CloseNow()
	}
	if err == nil || response.StatusCode != 503 {
		t.Fatal("straggler released permit", err)
	}
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := h.Shutdown(short); err != context.DeadlineExceeded {
		t.Fatal("shutdown fabricated completion", err)
	}
	once.Do(func() { close(release) })
	<-returned
	if err := h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	extra, response, err = transport.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &transport.DialOptions{Subprotocols: []string{WebSocketProtocol}, HTTPHeader: http.Header{"Origin": {"https://app.example.test"}}})
	if extra != nil {
		extra.CloseNow()
	}
	if err == nil || response.StatusCode != 503 {
		t.Fatal("shutdown re-admitted", err)
	}
}
