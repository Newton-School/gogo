package http

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	transport "github.com/coder/websocket"
)

type webSocketPacket struct {
	envelope WebSocketEnvelope
	bytes    []byte
}
type webSocketSession struct {
	config          *webSocketConfig
	request         *http.Request
	ctx             context.Context
	cancel          context.CancelFunc
	transportCtx    context.Context
	transportCancel context.CancelFunc
	connection      *transport.Conn
	inbound         chan []byte
	outbound        chan webSocketPacket
	pumps           sync.WaitGroup
	mu              sync.Mutex
	raw             net.Conn
	closing         chan struct{}
	closeSet        bool
	code            int
	immediate       bool
	tokens          float64
	lastToken       time.Time
}

func (w *WebSocket) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	tracked := &webSocketResponseWriter{ResponseWriter: writer}
	writer = tracked
	var session *webSocketSession
	var upgrade *webSocketUpgradeWriter
	defer func() {
		if panicValue := recover(); panicValue != nil {
			if session != nil {
				session.requestClose(1011, true)
				session.closeRaw()
			}
			if upgrade != nil && upgrade.attempted {
				return
			}
			if tracked.started || panicValue == http.ErrAbortHandler {
				panic(http.ErrAbortHandler)
			}
			webSocketHTTPError(writer, request, 503)
		}
	}()
	if w == nil || w.config == nil || w.state == nil {
		webSocketHTTPError(writer, request, 503)
		return
	}
	config, state := w.config, w.state
	if request != nil && request.Method != http.MethodGet {
		webSocketHTTPError(writer, request, 405)
		return
	}
	frozen, ok := webSocketRequest(request)
	if !ok {
		webSocketHTTPError(writer, request, 400)
		return
	}
	if !config.handshake(frozen) {
		webSocketHTTPError(writer, frozen, 403)
		return
	}
	hijacker, ok := webSocketHijacker(writer)
	if !ok {
		webSocketHTTPError(writer, frozen, 503)
		return
	}
	if webSocketContextError(frozen.Context()) != nil {
		webSocketHTTPError(writer, frozen, 503)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(frozen.Context()), config.options.Lifetime)
	session = &webSocketSession{config: config, request: frozen, ctx: ctx, cancel: cancel, closing: make(chan struct{}), inbound: make(chan []byte, 1), outbound: make(chan webSocketPacket, config.options.OutboundQueue), tokens: float64(config.options.Burst), lastToken: time.Now()}
	if !state.reserve(session, config.options.MaxConnections) {
		cancel()
		webSocketHTTPError(writer, frozen, 503)
		return
	}
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			session.requestClose(1011, true)
		} else {
			session.requestClose(1000, false)
		}
		if session.connection == nil {
			session.closeRaw()
		}
		session.pumps.Wait()
		session.closeRaw()
		cancel()
		state.release(session)
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	handshakeCtx, handshakeCancel := context.WithTimeout(frozen.Context(), config.options.HandlerTimeout)
	err := session.sessionGrant(handshakeCtx)
	handshakeCancel()
	if err != nil || webSocketContextError(ctx) != nil {
		status := 503
		if err == auth.ErrUnauthenticated {
			status = 401
		}
		if err == auth.ErrPermissionDenied {
			status = 403
		}
		webSocketHTTPError(writer, frozen, status)
		return
	}
	// The transport's selector is case-insensitive; its private request must
	// contain only our already-validated exact subprotocol token.
	frozen.Header.Set("Sec-WebSocket-Protocol", WebSocketProtocol)
	upgrade = &webSocketUpgradeWriter{ResponseWriter: writer, hijacker: hijacker, session: session}
	// This also bounds 101 emission before Hijack has returned the raw socket.
	if err := http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(config.options.WriteTimeout)); err != nil {
		webSocketHTTPError(writer, frozen, 503)
		return
	}
	connection, err := transport.Accept(upgrade, frozen, &transport.AcceptOptions{
		InsecureSkipVerify: true, Subprotocols: []string{WebSocketProtocol}, CompressionMode: transport.CompressionDisabled,
		OnPingReceived: func(context.Context, []byte) bool {
			if !session.allowFrame() {
				session.requestClose(1008, false)
				return false
			}
			return true
		},
		OnPongReceived: func(context.Context, []byte) {
			if !session.allowFrame() {
				session.requestClose(1008, false)
			}
		},
	})
	if err != nil {
		return
	} // Accept owns its handshake response; never append one.
	session.connection = connection
	if !session.resetDeadline() {
		session.closeRaw()
		return
	}
	connection.SetReadLimit(config.options.MaxMessageBytes)
	session.transportCtx, session.transportCancel = context.WithCancel(context.Background())
	session.pumps.Add(3)
	go session.control()
	go session.read()
	go session.write()
	session.run()
}

func (s *webSocketSession) capture(raw net.Conn) {
	s.mu.Lock()
	s.raw = raw
	closing := s.closeSet
	s.mu.Unlock()
	if closing {
		s.closeRaw()
	}
}
func (s *webSocketSession) resetDeadline() bool {
	s.mu.Lock()
	raw := s.raw
	s.mu.Unlock()
	return raw != nil && raw.SetDeadline(time.Time{}) == nil
}
func (s *webSocketSession) closeRaw() {
	defer func() { _ = recover() }()
	s.mu.Lock()
	raw := s.raw
	s.mu.Unlock()
	if raw != nil {
		_ = raw.Close()
	}
}
func (s *webSocketSession) requestClose(code int, immediate bool) {
	s.mu.Lock()
	if !s.closeSet {
		s.closeSet = true
		s.code = code
		s.immediate = immediate
		close(s.closing)
	} else if immediate {
		s.immediate = true
	}
	s.mu.Unlock()
	s.cancel()
	// Only shutdown/fatal outer cleanup requests immediate closure. Control
	// callbacks always signal a graceful close and never close under readMu.
	if immediate {
		s.closeRaw()
	}
}
func (s *webSocketSession) allowFrame() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.tokens += now.Sub(s.lastToken).Seconds() * float64(s.config.options.MessagesPerSecond)
	s.lastToken = now
	if s.tokens > float64(s.config.options.Burst) {
		s.tokens = float64(s.config.options.Burst)
	}
	if s.closeSet || s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}
func (s *webSocketSession) control() {
	defer s.pumps.Done()
	defer s.transportCancel()
	defer func() {
		if recover() != nil {
			s.closeRaw()
		}
	}()
	select {
	case <-s.closing:
	case <-s.ctx.Done():
		s.requestClose(1001, false)
	}
	s.mu.Lock()
	code, immediate := s.code, s.immediate
	s.mu.Unlock()
	if immediate {
		s.closeRaw()
		_ = s.connection.CloseNow()
		return
	}
	// The pinned transport uses its own close-handshake deadlines. Closing the
	// captured raw socket bounds that handshake without another reader/writer.
	timer := time.AfterFunc(s.config.options.CloseTimeout, s.closeRaw)
	defer timer.Stop()
	_ = s.connection.Close(transport.StatusCode(code), webSocketCloseReason(code))
	s.closeRaw()
}
func webSocketCloseReason(code int) string {
	switch code {
	case 1000:
		return "complete"
	case 1001:
		return "going away"
	case 1007:
		return "invalid message"
	case 1008:
		return "message denied"
	case 1009:
		return "message too large"
	case 1013:
		return "connection busy"
	default:
		return "service unavailable"
	}
}
func (s *webSocketSession) read() {
	defer s.pumps.Done()
	defer func() {
		if recover() != nil {
			s.requestClose(1011, false)
		}
	}()
	for {
		ctx, cancel := context.WithTimeout(s.transportCtx, s.config.options.IdleTimeout)
		typ, data, err := s.connection.Read(ctx)
		cancel()
		if err != nil {
			s.requestClose(1001, false)
			return
		}
		if typ != transport.MessageText {
			s.requestClose(1007, false)
			return
		}
		if !s.allowFrame() {
			s.requestClose(1008, false)
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case s.inbound <- data:
		default:
			s.requestClose(1013, false)
			return
		}
	}
}
func (s *webSocketSession) write() {
	defer s.pumps.Done()
	defer func() {
		if recover() != nil {
			s.requestClose(1011, false)
		}
	}()
	ticker := time.NewTicker(s.config.options.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.transportCtx, s.config.options.PongTimeout)
			err := s.connection.Ping(ctx)
			cancel()
			if err != nil {
				s.requestClose(1001, false)
				return
			}
		case packet := <-s.outbound:
			ctx, cancel := context.WithTimeout(s.ctx, s.config.options.WriteTimeout)
			stop := context.AfterFunc(ctx, func() { s.requestClose(1011, false) })
			err := s.grant(ctx, WebSocketSend, packet.envelope)
			if err == nil {
				err = s.connection.Write(ctx, transport.MessageText, packet.bytes)
			}
			stop()
			cancel()
			if err != nil {
				s.requestClose(webSocketGrantCode(err), false)
				return
			}
		}
	}
}
func webSocketGrantCode(err error) int {
	if err == auth.ErrUnauthenticated || err == auth.ErrPermissionDenied {
		return 1008
	}
	return 1011
}
func (s *webSocketSession) sessionGrant(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if webSocketContextError(ctx) != nil || webSocketContextError(s.ctx) != nil {
			err = ErrUnavailable
		}
	}()
	if webSocketContextError(ctx) != nil || webSocketContextError(s.ctx) != nil {
		return ErrUnavailable
	}
	return s.config.options.Authorize(webSocketFreshRequest(s.request, ctx))
}
func (s *webSocketSession) grant(ctx context.Context, action string, value WebSocketEnvelope) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if webSocketContextError(ctx) != nil || webSocketContextError(s.ctx) != nil {
			err = ErrUnavailable
		}
	}()
	if err := s.sessionGrant(ctx); err != nil {
		return err
	}
	err = s.config.options.AuthorizeMessage(webSocketFreshRequest(s.request, ctx), action, webSocketCopyEnvelope(value))
	return err
}
func (s *webSocketSession) run() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case data := <-s.inbound:
			code := s.dispatch(data)
			if code != 0 {
				s.requestClose(code, false)
				return
			}
		}
	}
}
func (s *webSocketSession) dispatch(data []byte) (code int) {
	defer func() {
		if recover() != nil {
			code = 1011
		}
	}()
	envelope, payload, err := webSocketDecodeEnvelope(data)
	if err != nil {
		return 1007
	}
	message := s.config.messages[webSocketKey{envelope.Type, envelope.Version}]
	if message == nil || !message.input.acceptLimit(payload, int(s.config.options.MaxMessageBytes)) {
		return 1007
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config.options.HandlerTimeout)
	stop := context.AfterFunc(ctx, func() { s.requestClose(1011, false) })
	defer func() { stop(); cancel() }()
	if err := s.grant(ctx, WebSocketReceive, envelope); err != nil {
		return webSocketGrantCode(err)
	}
	validationErr := message.validate(ctx, envelope.Payload)
	if webSocketContextError(ctx) != nil {
		return 1011
	}
	if validationErr != nil {
		return 1008
	}
	// Validation may consult current data. Recheck immediately before effect.
	if err := s.grant(ctx, WebSocketReceive, envelope); err != nil {
		return webSocketGrantCode(err)
	}
	output, err := message.invoke(ctx, envelope.Payload, int(s.config.options.MaxMessageBytes))
	if webSocketContextError(ctx) != nil {
		return 1011
	}
	if err == ErrWebSocketClose {
		return 1000
	}
	if err != nil {
		return 1011
	}
	result := WebSocketEnvelope{Type: envelope.Type, Version: envelope.Version, Payload: output}
	encoded, err := webSocketEnvelopeBytes(result, int(s.config.options.MaxMessageBytes))
	if err != nil {
		return 1011
	}
	if err := s.grant(ctx, WebSocketSend, result); err != nil {
		return webSocketGrantCode(err)
	}
	select {
	case <-s.ctx.Done():
		return 1001
	case s.outbound <- webSocketPacket{result, encoded}:
		return 0
	default:
		return 1013
	}
}
