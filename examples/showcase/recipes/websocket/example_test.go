package http_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/auth"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/urls"
)

func Example_websocketNewWebSocket() {
	type message struct {
		Text string `json:"text"`
	}
	definition, err := ghttp.WebSocketMessage("echo", 1, func(_ context.Context, input message) error {
		if len(input.Text) > 200 {
			return ghttp.ErrWebSocketInvalidMessage
		}
		return nil
	}, func(_ context.Context, input message) (message, error) { return input, nil })
	if err != nil {
		panic(err)
	}
	socket, err := ghttp.NewWebSocket(ghttp.WebSocketOptions{
		Origins: []string{"https://app.example.test"}, Messages: []ghttp.WebSocketMessageDefinition{definition},
		Authorize: func(r *http.Request) error {
			// Trusted middleware establishes the initial principal. Production
			// applications must also recheck current session expiry/revocation here.
			principal := auth.FromContext(r.Context())
			if !principal.Authenticated || !principal.Active {
				return auth.ErrUnauthenticated
			}
			return nil
		},
		AuthorizeMessage: func(_ *http.Request, action string, envelope ghttp.WebSocketEnvelope) error {
			// This endpoint deliberately permits only an authenticated echo.
			// Resource-bearing messages must check current object/scope policy here.
			if envelope.Type != "echo" || (action != ghttp.WebSocketReceive && action != ghttp.WebSocketSend) {
				return auth.ErrPermissionDenied
			}
			return nil
		},
	})
	if err != nil {
		panic(err)
	}
	router, err := urls.New(urls.Path("/socket/", socket, "socket", "GET"))
	if err != nil {
		panic(err)
	}
	_, err = ghttp.NewServer(ghttp.ServerConfig{Handler: router, Streaming: func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/socket/") }})
	if err != nil {
		panic(err)
	}
	application, err := app.Bootstrap(context.Background(), nil, []app.Resource{{Name: "websocket", Open: func(context.Context) (func(context.Context) error, error) { return socket.Shutdown, nil }}}, nil)
	if err != nil {
		panic(err)
	}
	// The server owner stops HTTP admission, drains sockets, then closes shared
	// services. Register this resource after the services it consumes.
	application.StopAdmission()
	if err := application.Close(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println(ghttp.WebSocketProtocol)
	// Output: gogo.json.v1
}
