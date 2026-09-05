package mail

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type TLSMode string

const (
	TLSStartTLS  TLSMode = "starttls"
	TLSImplicit  TLSMode = "implicit"
	TLSPlaintext TLSMode = "development_plaintext"
)

// SMTPConfig is snapshotted by NewSMTP. Credentials are optional only when
// authentication is not selected (both Username and Password empty). Plaintext
// is restricted to explicitly selected loopback development with no credentials.
// TLS callback functions, certificate private keys/Leaf objects and session
// caches remain caller-owned interfaces: keep them immutable or concurrency-safe.
// TLS key logging and insecure verification are rejected.
type SMTPConfig struct {
	Host, Username, Password, LocalName string
	Port                                int
	TLSMode                             TLSMode
	TLSConfig                           *tls.Config
	PoolSize                            int
	Timeout                             time.Duration
	Development                         bool
	Limits                              Limits
}

func (SMTPConfig) String() string                      { return "mail.SMTPConfig{credentials:redacted}" }
func (c SMTPConfig) GoString() string                  { return c.String() }
func (c SMTPConfig) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, c.String()) }
func (SMTPConfig) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }

// SMTP owns a bounded pool. Each Send has one envelope attempt; it does not
// retry stale connections, recipient failures, or ambiguous DATA completion.
// Close interrupts active network operations and rejects subsequent sends.
type SMTP struct {
	*smtpState
}
type smtpState struct {
	config      SMTPConfig
	slots       chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	idle        []*smtpConnection
	connections map[*smtpConnection]struct{}
}

type smtpConnection struct {
	wire   *responseBoundConn
	client *smtp.Client
}

// SMTP response text is untrusted. The standard text protocol reader does not
// bound lines, so cap total wire bytes read per submission (including TLS).
type responseBoundConn struct {
	net.Conn
	remaining atomic.Int64
}

func (c *responseBoundConn) Read(p []byte) (int, error) {
	remaining := c.remaining.Load()
	if remaining <= 0 {
		return 0, ErrLimit
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining.Add(-int64(n))
	return n, err
}

func NewSMTP(config SMTPConfig) (*SMTP, error) {
	if config.Host == "" || !safeHeader(config.Host) || strings.ContainsAny(config.Host, "/\\ @<>") || len(config.Host) > 253 {
		return nil, ErrValidation
	}
	if net.ParseIP(config.Host) == nil {
		for _, r := range config.Host {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
				return nil, ErrValidation
			}
		}
	}
	if config.TLSMode == "" {
		config.TLSMode = TLSStartTLS
	}
	if config.TLSMode != TLSStartTLS && config.TLSMode != TLSImplicit && config.TLSMode != TLSPlaintext {
		return nil, ErrValidation
	}
	if config.Port == 0 {
		config.Port = 587
		if config.TLSMode == TLSImplicit {
			config.Port = 465
		}
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, ErrValidation
	}
	if config.PoolSize == 0 {
		config.PoolSize = 1
	}
	if config.PoolSize < 1 || config.PoolSize > 20 {
		return nil, ErrLimit
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Timeout < time.Millisecond || config.Timeout > 10*time.Minute {
		return nil, ErrLimit
	}
	if config.LocalName == "" {
		config.LocalName = "localhost"
	}
	if !safeHeader(config.LocalName) || strings.ContainsAny(config.LocalName, " <>@") || len(config.LocalName) > 253 {
		return nil, ErrValidation
	}
	if (config.Username == "") != (config.Password == "") || len(config.Username) > 1024 || len(config.Password) > 8192 || strings.ContainsRune(config.Username, 0) || strings.ContainsRune(config.Password, 0) {
		return nil, ErrValidation
	}
	if config.TLSMode == TLSPlaintext {
		ip := net.ParseIP(config.Host)
		if !config.Development || config.Host != "localhost" && (ip == nil || !ip.IsLoopback()) || config.Username != "" || config.TLSConfig != nil {
			return nil, ErrValidation
		}
	} else {
		if config.TLSConfig == nil {
			config.TLSConfig = &tls.Config{}
		} else {
			config.TLSConfig = config.TLSConfig.Clone()
		}
		tc := config.TLSConfig
		if tc.InsecureSkipVerify || tc.KeyLogWriter != nil || tc.MinVersion != 0 && tc.MinVersion < tls.VersionTLS12 || tc.MaxVersion != 0 && tc.MaxVersion < tls.VersionTLS12 {
			return nil, ErrValidation
		}
		if tc.MinVersion == 0 {
			tc.MinVersion = tls.VersionTLS12
		}
		if tc.ServerName == "" {
			tc.ServerName = config.Host
		}
		if tc.RootCAs != nil {
			tc.RootCAs = tc.RootCAs.Clone()
		}
		if tc.ClientCAs != nil {
			tc.ClientCAs = tc.ClientCAs.Clone()
		}
		tc.NextProtos = slices.Clone(tc.NextProtos)
		tc.CipherSuites = slices.Clone(tc.CipherSuites)
		tc.CurvePreferences = slices.Clone(tc.CurvePreferences)
		tc.Certificates = slices.Clone(tc.Certificates)
		for i := range tc.Certificates {
			cert := &tc.Certificates[i]
			cert.Certificate = slices.Clone(cert.Certificate)
			for j := range cert.Certificate {
				cert.Certificate[j] = slices.Clone(cert.Certificate[j])
			}
			cert.OCSPStaple = slices.Clone(cert.OCSPStaple)
			cert.SignedCertificateTimestamps = slices.Clone(cert.SignedCertificateTimestamps)
			for j := range cert.SignedCertificateTimestamps {
				cert.SignedCertificateTimestamps[j] = slices.Clone(cert.SignedCertificateTimestamps[j])
			}
		}
	}
	l, err := config.Limits.defaults()
	if err != nil {
		return nil, err
	}
	config.Limits = l
	ctx, cancel := context.WithCancel(context.Background())
	return &SMTP{smtpState: &smtpState{config: config, slots: make(chan struct{}, config.PoolSize), ctx: ctx, cancel: cancel, connections: map[*smtpConnection]struct{}{}}}, nil
}

func (s *SMTP) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	for connection := range s.connections {
		_ = connection.wire.Close()
	}
	s.idle = nil
	return nil
}

func bindDeadline(ctx context.Context, connection net.Conn) func() {
	deadline, _ := ctx.Deadline()
	_ = connection.SetDeadline(deadline)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		_ = connection.SetDeadline(time.Time{})
	}
}

func (s *SMTP) acquire(ctx context.Context) (*smtpConnection, func(), error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, nil, ErrClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.slots
		return nil, nil, ErrClosed
	}
	if len(s.idle) > 0 {
		connection := s.idle[len(s.idle)-1]
		s.idle = s.idle[:len(s.idle)-1]
		s.mu.Unlock()
		connection.wire.remaining.Store(1 << 20)
		return connection, bindDeadline(ctx, connection.wire), nil
	}
	s.mu.Unlock()
	dialCtx, cancel := context.WithCancel(ctx)
	stopClose := context.AfterFunc(s.ctx, cancel)
	wire, err := new(net.Dialer).DialContext(dialCtx, "tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	stopClose()
	cancel()
	if err != nil {
		<-s.slots
		return nil, nil, err
	}
	connection := &smtpConnection{wire: &responseBoundConn{Conn: wire}}
	connection.wire.remaining.Store(1 << 20)
	cleanup := bindDeadline(ctx, connection.wire)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cleanup()
		_ = wire.Close()
		<-s.slots
		return nil, nil, ErrClosed
	}
	s.connections[connection] = struct{}{}
	s.mu.Unlock()
	var transport net.Conn = connection.wire
	if s.config.TLSMode == TLSImplicit {
		secure := tls.Client(transport, s.config.TLSConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			cleanup()
			s.release(connection, false)
			return nil, nil, err
		}
		transport = secure
	}
	connection.client, err = smtp.NewClient(transport, s.config.Host)
	if err == nil {
		err = connection.client.Hello(s.config.LocalName)
	}
	if err == nil && s.config.TLSMode == TLSStartTLS {
		if ok, _ := connection.client.Extension("STARTTLS"); !ok {
			err = ErrTransport
		} else {
			err = connection.client.StartTLS(s.config.TLSConfig)
		}
	}
	if err == nil && s.config.Username != "" {
		if ok, methods := connection.client.Extension("AUTH"); !ok || !slices.Contains(strings.Fields(methods), "PLAIN") {
			err = ErrTransport
		} else {
			err = connection.client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host))
		}
	}
	if err != nil {
		cleanup()
		s.release(connection, false)
		return nil, nil, err
	}
	return connection, cleanup, nil
}

func (s *SMTP) release(connection *smtpConnection, healthy bool) {
	s.mu.Lock()
	if healthy && !s.closed {
		s.idle = append(s.idle, connection)
	} else {
		delete(s.connections, connection)
		_ = connection.wire.Close()
	}
	s.mu.Unlock()
	<-s.slots
}

func smtpCode(err error) int {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		return reply.Code
	}
	return 0
}
func safeSendError(ctx context.Context, kind error, stage string, err error) error {
	return &SendError{Kind: kind, Stage: stage, Code: smtpCode(err), contextError: ctx.Err()}
}

func (s *SMTP) Send(ctx context.Context, message Message) (Receipt, error) {
	p, err := Prepare(ctx, message, s.config.Limits)
	if err != nil {
		return Receipt{}, err
	}
	r := receiptFor("smtp", p)
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	connection, cleanup, err := s.acquire(ctx)
	if err != nil {
		if errors.Is(err, ErrClosed) {
			return r, ErrClosed
		}
		return r, safeSendError(ctx, ErrTransport, "connect", err)
	}
	healthy := false
	defer func() { cleanup(); s.release(connection, healthy && ctx.Err() == nil) }()
	if err = connection.client.Mail(p.sender); err != nil {
		return r, safeSendError(ctx, ErrTransport, "envelope", err)
	}
	pending := []int{}
	rejected := false
	for i, recipient := range p.recipients {
		err = connection.client.Rcpt(recipient)
		if err == nil {
			pending = append(pending, i)
			continue
		}
		code := smtpCode(err)
		if code >= 400 && code <= 599 {
			r.Recipients[i].State = Rejected
			r.Recipients[i].Code = code
			rejected = true
			continue
		}
		return r, safeSendError(ctx, ErrTransport, "recipient", err)
	}
	if len(pending) == 0 {
		healthy = connection.client.Reset() == nil
		return r, &SendError{Kind: ErrRejected, Stage: "recipient"}
	}
	writer, err := connection.client.Data()
	if err != nil {
		code := smtpCode(err)
		if code >= 400 && code <= 599 {
			for _, i := range pending {
				r.Recipients[i].State = Rejected
				r.Recipients[i].Code = code
			}
			return r, safeSendError(ctx, ErrRejected, "data", err)
		}
		return r, safeSendError(ctx, ErrTransport, "data", err)
	}
	markUnknown := func(err error) (Receipt, error) {
		for _, i := range pending {
			r.Recipients[i].State = Unknown
		}
		return r, safeSendError(ctx, ErrUnknown, "acceptance", err)
	}
	if _, err = writer.Write(p.data); err != nil {
		return markUnknown(err)
	}
	if err = writer.Close(); err != nil {
		code := smtpCode(err)
		if code >= 400 && code <= 599 {
			for _, i := range pending {
				r.Recipients[i].State = Rejected
				r.Recipients[i].Code = code
			}
			return r, safeSendError(ctx, ErrRejected, "acceptance", err)
		}
		return markUnknown(err)
	}
	for _, i := range pending {
		r.Recipients[i].State = Accepted
		r.Recipients[i].Code = 250
	}
	healthy = true
	if rejected {
		return r, &SendError{Kind: ErrRejected, Stage: "recipient"}
	}
	return r, nil
}

// Prevent accidental ordinary serialization of a configured transport.
func (SMTP) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }
func (SMTP) String() string                      { return "mail.SMTP{credentials:redacted}" }
func (s SMTP) GoString() string                  { return s.String() }
func (s SMTP) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, s.String()) }

var _ Backend = (*SMTP)(nil)
var _ json.Marshaler = SMTPConfig{}
