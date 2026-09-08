package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"
)

const WebSocketProtocol = "gogo.json.v1"
const WebSocketReceive = "receive"
const WebSocketSend = "send"

var ErrWebSocketConfiguration = errors.New("http: invalid websocket configuration")
var ErrWebSocketInvalidMessage = errors.New("http: invalid websocket message")
var ErrWebSocketClose = errors.New("http: close websocket normally")

// WebSocketEnvelope is an independent JSON observation delivered to a grant.
// Changing it never changes the message executed or emitted by the connection.
type WebSocketEnvelope struct {
	Type    string          `json:"type"`
	Version uint32          `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

// WebSocketOptions declares an explicitly mounted, authenticated JSON socket.
// Callbacks must honor their context. A callback that does not return retains
// its connection permit even after disconnect; Go cannot stop arbitrary code.
type WebSocketOptions struct {
	Origins                                                                                      []string
	AllowMissingOrigin                                                                           bool
	Authorize                                                                                    func(*http.Request) error
	AuthorizeMessage                                                                             func(*http.Request, string, WebSocketEnvelope) error
	Messages                                                                                     []WebSocketMessageDefinition
	MaxMessageBytes                                                                              int64
	MaxConnections, OutboundQueue, MessagesPerSecond, Burst                                      int
	IdleTimeout, HandlerTimeout, WriteTimeout, PingInterval, PongTimeout, Lifetime, CloseTimeout time.Duration
}

// WebSocket is a permanently stoppable handler. Mount it outside TimeoutHandler
// (NewServer's Streaming selector), and explicitly call Shutdown while draining
// the application, before closing services used by its callbacks. Do not copy it.
type WebSocket struct {
	config *webSocketConfig
	state  *webSocketRegistry
}
type webSocketConfig struct {
	options  WebSocketOptions
	origins  map[string]bool
	messages map[webSocketKey]*webSocketMessage
}
type webSocketKey struct {
	name    string
	version uint32
}
type webSocketRegistry struct {
	mu       sync.Mutex
	stopped  bool
	sessions map[*webSocketSession]struct{}
	empty    chan struct{}
}

func NewWebSocket(options WebSocketOptions) (*WebSocket, error) {
	if options.Authorize == nil || options.AuthorizeMessage == nil || len(options.Messages) < 1 || len(options.Messages) > 128 || len(options.Origins) > 128 {
		return nil, ErrWebSocketConfiguration
	}
	config := &webSocketConfig{options: options, origins: map[string]bool{}, messages: map[webSocketKey]*webSocketMessage{}}
	for _, raw := range options.Origins {
		origin, ok := webSocketOrigin(raw)
		if !ok || config.origins[origin] {
			return nil, ErrWebSocketConfiguration
		}
		config.origins[origin] = true
	}
	if len(config.origins) == 0 && !options.AllowMissingOrigin {
		return nil, ErrWebSocketConfiguration
	}
	for _, definition := range options.Messages {
		if definition.message == nil {
			return nil, ErrWebSocketConfiguration
		}
		message := *definition.message
		key := webSocketKey{message.name, message.version}
		if config.messages[key] != nil {
			return nil, ErrWebSocketConfiguration
		}
		config.messages[key] = &message
	}
	o := &config.options
	o.Origins, o.Messages = nil, nil
	if o.MaxMessageBytes == 0 {
		o.MaxMessageBytes = 1 << 20
	}
	if o.MaxMessageBytes < 128 || o.MaxMessageBytes > 8<<20 {
		return nil, ErrWebSocketConfiguration
	}
	for _, message := range config.messages {
		if message.input.typ.Size() > uintptr(o.MaxMessageBytes) || message.output.typ.Size() > uintptr(o.MaxMessageBytes) {
			return nil, ErrWebSocketConfiguration
		}
	}
	for _, item := range []struct {
		value         *int
		fallback, max int
	}{
		{&o.MaxConnections, 128, 10000}, {&o.OutboundQueue, 4, 32}, {&o.MessagesPerSecond, 20, 10000}, {&o.Burst, 40, 10000},
	} {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 1 || *item.value > item.max {
			return nil, ErrWebSocketConfiguration
		}
	}
	for _, item := range []struct {
		value         *time.Duration
		fallback, max time.Duration
	}{
		{&o.IdleTimeout, time.Minute, 10 * time.Minute}, {&o.HandlerTimeout, 10 * time.Second, time.Minute},
		{&o.WriteTimeout, 10 * time.Second, time.Minute}, {&o.PingInterval, 20 * time.Second, time.Minute},
		{&o.PongTimeout, 5 * time.Second, 30 * time.Second}, {&o.Lifetime, time.Hour, 24 * time.Hour}, {&o.CloseTimeout, time.Second, 5 * time.Second},
	} {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < time.Millisecond || *item.value > item.max {
			return nil, ErrWebSocketConfiguration
		}
	}
	empty := make(chan struct{})
	close(empty)
	return &WebSocket{config: config, state: &webSocketRegistry{sessions: map[*webSocketSession]struct{}{}, empty: empty}}, nil
}

func (r *webSocketRegistry) reserve(s *webSocketSession, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || len(r.sessions) >= max {
		return false
	}
	if len(r.sessions) == 0 {
		r.empty = make(chan struct{})
	}
	r.sessions[s] = struct{}{}
	return true
}
func (r *webSocketRegistry) release(s *webSocketSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, s)
	if len(r.sessions) == 0 {
		close(r.empty)
	}
}

// Shutdown stops admission permanently, cancels application work and closes
// accepted transports. The caller's context bounds waiting, not application
// code. A later Shutdown may wait again for previously noncooperative callbacks.
func (w *WebSocket) Shutdown(ctx context.Context) error {
	if w == nil || w.state == nil {
		return ErrUnavailable
	}
	state := w.state // Capture before a caller context can replace the handle.
	state.mu.Lock()
	state.stopped = true
	sessions := make([]*webSocketSession, 0, len(state.sessions))
	for session := range state.sessions {
		sessions = append(sessions, session)
	}
	done := state.empty
	state.mu.Unlock()
	for _, session := range sessions {
		session.requestClose(1001, true)
	}
	if err := webSocketContextError(ctx); err != nil {
		return err
	}
	return webSocketWait(ctx, done)
}

func webSocketContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	if ctx == nil {
		return ErrUnavailable
	}
	v := reflect.ValueOf(ctx)
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
		return ErrUnavailable
	}
	switch err := ctx.Err(); err {
	case nil, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}
func webSocketWait(ctx context.Context, done <-chan struct{}) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if err := webSocketContextError(ctx); err != nil {
			return err
		}
		return ErrUnavailable
	}
}

func webSocketCopyEnvelope(value WebSocketEnvelope) WebSocketEnvelope {
	value.Payload = append(json.RawMessage(nil), value.Payload...)
	return value
}
