package integration

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/app"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/urls"
	transport "github.com/coder/websocket"
)

func TestWebSocketRealServerRouterAndExplicitDrain(t *testing.T) {
	type input struct {
		Value int64 `json:"value"`
	}
	type output struct {
		Value int64  `json:"value"`
		Room  string `json:"room"`
	}
	var effects atomic.Int32
	definition, err := ghttp.WebSocketMessage("echo", 1, func(context.Context, input) error { return nil }, func(_ context.Context, value input) (output, error) {
		effects.Add(1)
		return output{value.Value, "private-room"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	socket, err := ghttp.NewWebSocket(ghttp.WebSocketOptions{Origins: []string{"https://app.example.test"}, Messages: []ghttp.WebSocketMessageDefinition{definition}, Authorize: func(r *http.Request) error {
		if urls.Param(r, "room") != "one" || ghttp.RequestID(r) == "" {
			t.Error("trusted route/request context lost")
		}
		return nil
	}, AuthorizeMessage: func(_ *http.Request, action string, value ghttp.WebSocketEnvelope) error {
		if action != ghttp.WebSocketReceive && action != ghttp.WebSocketSend {
			t.Error(action)
		}
		if value.Type != "echo" {
			t.Error(value.Type)
		}
		return nil
	}, CloseTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Route{Pattern: "/socket/<slug:room>/", Name: "socket", Handler: socket})
	if err != nil {
		t.Fatal(err)
	}
	server, err := ghttp.NewServer(ghttp.ServerConfig{Handler: router, Streaming: func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/socket/") }, HandlerTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = socket.Shutdown(shutdown)
		cancel()
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
			t.Error("server drain blocked")
		}
	})
	clientCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	conn, _, err := transport.Dial(clientCtx, "ws://"+listener.Addr().String()+"/socket/one/", &transport.DialOptions{Subprotocols: []string{ghttp.WebSocketProtocol}, HTTPHeader: http.Header{"Origin": {"https://app.example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(clientCtx, transport.MessageText, []byte(`{"type":"echo","version":1,"payload":{"value":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.Read(clientCtx)
	if err != nil {
		t.Fatal(err)
	}
	var envelope ghttp.WebSocketEnvelope
	if json.Unmarshal(body, &envelope) != nil || !strings.Contains(string(envelope.Payload), "9007199254740993") || effects.Load() != 1 {
		t.Fatal(string(body), effects.Load())
	}
	// The app resource demonstrates explicit socket drain, not a claim that
	// net/http tracks connections after its101/hijack boundary.
	application, err := app.Prepare(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(context.Background(), []app.Resource{{Name: "websocket", Open: func(context.Context) (func(context.Context) error, error) { return socket.Shutdown, nil }}}); err != nil {
		t.Fatal(err)
	}
	application.StopAdmission()
	if err := application.Close(clientCtx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(clientCtx); err == nil {
		t.Fatal("resource drain left socket open")
	}
}
