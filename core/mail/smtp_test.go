package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type smtpOptions struct {
	noSTARTTLS, implicit, dropAfterDATA, hugeBanner bool
	dataCode                                        int
	hold                                            <-chan struct{}
}
type smtpFixture struct {
	listener      net.Listener
	tls           *tls.Config
	roots         *x509.CertPool
	options       smtpOptions
	done          chan struct{}
	data          chan []byte
	connections   atomic.Int64
	authBeforeTLS atomic.Bool
	authCount     atomic.Int64
	wg            sync.WaitGroup
	mu            sync.Mutex
	wires         []net.Conn
}

func newSMTPFixture(t *testing.T, options smtpOptions) *smtpFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic SMTP"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &smtpFixture{listener: listener, tls: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}, roots: roots, options: options, done: make(chan struct{}), data: make(chan []byte, 100)}
	f.wg.Go(func() {
		for {
			wire, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.wires = append(f.wires, wire)
			f.mu.Unlock()
			f.connections.Add(1)
			f.wg.Go(func() { f.serve(wire) })
		}
	})
	t.Cleanup(func() {
		close(f.done)
		_ = listener.Close()
		f.mu.Lock()
		for _, wire := range f.wires {
			_ = wire.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *smtpFixture) config() SMTPConfig {
	host, port, _ := net.SplitHostPort(f.listener.Addr().String())
	number, _ := strconv.Atoi(port)
	mode := TLSStartTLS
	if f.options.implicit {
		mode = TLSImplicit
	}
	return SMTPConfig{Host: host, Port: number, TLSMode: mode, TLSConfig: &tls.Config{RootCAs: f.roots}, Timeout: 2 * time.Second}
}

func (f *smtpFixture) serve(raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	var wire net.Conn = raw
	secured := false
	if f.options.implicit {
		secure := tls.Server(wire, f.tls)
		if secure.Handshake() != nil {
			return
		}
		wire = secure
		secured = true
	}
	tp := textproto.NewConn(wire)
	if f.options.hugeBanner {
		_, _ = fmt.Fprint(wire, "220 "+strings.Repeat("x", (1<<20)+1))
		return
	}
	if tp.PrintfLine("220 synthetic SMTP") != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, _, _ := strings.Cut(line, " ")
		switch verb {
		case "EHLO":
			if tp.PrintfLine("250-synthetic") != nil {
				return
			}
			if !secured && !f.options.noSTARTTLS {
				if tp.PrintfLine("250-STARTTLS") != nil {
					return
				}
			}
			if tp.PrintfLine("250 AUTH PLAIN") != nil {
				return
			}
		case "STARTTLS":
			if tp.PrintfLine("220 begin TLS") != nil {
				return
			}
			secure := tls.Server(wire, f.tls)
			if secure.Handshake() != nil {
				return
			}
			wire = secure
			tp = textproto.NewConn(wire)
			secured = true
		case "AUTH":
			if !secured {
				f.authBeforeTLS.Store(true)
			}
			f.authCount.Add(1)
			if tp.PrintfLine("235 authenticated") != nil {
				return
			}
		case "MAIL", "RSET", "NOOP":
			if tp.PrintfLine("250 okay") != nil {
				return
			}
		case "RCPT":
			if strings.Contains(line, "reject@example.test") {
				err = tp.PrintfLine("550 private provider text MUST NOT LEAK")
			} else {
				err = tp.PrintfLine("250 recipient")
			}
			if err != nil {
				return
			}
		case "DATA":
			if tp.PrintfLine("354 data") != nil {
				return
			}
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			select {
			case f.data <- data:
			case <-f.done:
				return
			}
			if f.options.dropAfterDATA {
				return
			}
			if f.options.hold != nil {
				select {
				case <-f.options.hold:
				case <-f.done:
					return
				}
			}
			code := f.options.dataCode
			if code == 0 {
				code = 250
			}
			if tp.PrintfLine("%d private provider text MUST NOT LEAK", code) != nil {
				return
			}
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("500 unknown")
			return
		}
	}
}

func TestSMTPVerifiedTLSPartialAcceptanceAndPoolReuse(t *testing.T) {
	f := newSMTPFixture(t, smtpOptions{})
	config := f.config()
	config.Username = "synthetic-user"
	config.Password = "synthetic-password"
	backend, err := NewSMTP(config)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	// Snapshot security policy: later caller mutation must not weaken TLS.
	config.TLSConfig.InsecureSkipVerify = true
	config.TLSConfig.ServerName = "wrong.example.test"
	m := testMessage()
	m.Bcc = []string{"hidden@example.test"}
	m.Cc = []string{"reject@example.test"}
	r, err := backend.Send(context.Background(), m)
	if !errors.Is(err, ErrRejected) || r.AllAccepted() || len(r.Recipients) != 3 || r.Recipients[0].State != Accepted || r.Recipients[1].State != Rejected || r.Recipients[2].State != Accepted {
		t.Fatalf("partial receipt %v, %v", r, err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "example.test") {
		t.Fatal("provider detail leaked")
	}
	data := <-f.data
	if bytes.Contains(data, []byte("hidden@example.test")) || !bytes.Contains(data, []byte("\n.Second line")) {
		t.Fatal("Bcc or dot-stuffing failure")
	}
	r, err = backend.Send(context.Background(), testMessage())
	if err != nil || !r.AllAccepted() {
		t.Fatalf("second send %v %v", r, err)
	}
	if f.connections.Load() != 1 || f.authCount.Load() != 1 || f.authBeforeTLS.Load() {
		t.Fatal("pool reuse or authentication policy")
	}
}

func TestSMTPRejectsTLSAndUnsafeConfiguration(t *testing.T) {
	for _, config := range []SMTPConfig{{Host: "mail.example.test", TLSMode: TLSPlaintext, Development: true}, {Host: "127.0.0.1", TLSMode: TLSPlaintext}, {Host: "127.0.0.1", TLSMode: TLSPlaintext, Development: true, Username: "u", Password: "p"}, {Host: "mail.example.test", TLSConfig: &tls.Config{InsecureSkipVerify: true}}, {Host: "mail.example.test", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS10}}, {Host: "mail.example.test", Username: "u"}} {
		if _, err := NewSMTP(config); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	for _, mode := range []string{"missing_starttls", "wrong_name", "untrusted_ca"} {
		t.Run(mode, func(t *testing.T) {
			f := newSMTPFixture(t, smtpOptions{noSTARTTLS: mode == "missing_starttls"})
			config := f.config()
			config.Username = "synthetic-user"
			config.Password = "synthetic-password"
			if mode == "wrong_name" {
				config.TLSConfig.ServerName = "wrong.example.test"
			}
			if mode == "untrusted_ca" {
				config.TLSConfig.RootCAs = x509.NewCertPool()
			}
			backend, err := NewSMTP(config)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			r, err := backend.Send(context.Background(), testMessage())
			if err == nil || r.Recipients[0].State != NotAttempted || f.authCount.Load() != 0 {
				t.Fatal("TLS failure submitted/authenticated")
			}
		})
	}
	if _, err := json.Marshal(SMTPConfig{Password: "secret"}); !errors.Is(err, ErrSensitiveOutput) {
		t.Fatal(err)
	}
}

func TestSMTPUnknownAcceptanceAndExplicitRejectionNeverRetry(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		options smtpOptions
		state   RecipientState
		kind    error
	}{{"disconnect", smtpOptions{dropAfterDATA: true}, Unknown, ErrUnknown}, {"reject", smtpOptions{dataCode: 550}, Rejected, ErrRejected}} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newSMTPFixture(t, scenario.options)
			backend, err := NewSMTP(f.config())
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			r, err := backend.Send(context.Background(), testMessage())
			if !errors.Is(err, scenario.kind) || r.Recipients[0].State != scenario.state || r.AllAccepted() {
				t.Fatalf("bad outcome %v %v", r, err)
			}
			if f.connections.Load() != 1 || len(f.data) != 1 {
				t.Fatal("implicit retry")
			}
		})
	}
}

func TestSMTPCancellationAfterDATAAndPoolWaitAreBounded(t *testing.T) {
	hold := make(chan struct{})
	f := newSMTPFixture(t, smtpOptions{hold: hold})
	backend, err := NewSMTP(f.config())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Delivery, 1)
	go func() { r, err := backend.Send(ctx, testMessage()); done <- Delivery{r, err} }()
	select {
	case <-f.data:
	case <-time.After(2 * time.Second):
		t.Fatal("DATA not reached")
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	r, err := backend.Send(waitCtx, testMessage())
	if !errors.Is(err, context.DeadlineExceeded) || r.Recipients[0].State != NotAttempted {
		t.Fatal("pool wait bound")
	}
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.Err, ErrUnknown) || !errors.Is(result.Err, context.Canceled) || result.Receipt.Recipients[0].State != Unknown {
			t.Fatalf("bad cancellation %v %v", result.Receipt, result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel unbounded")
	}
	close(hold)
	r, err = backend.Send(context.Background(), testMessage())
	if err != nil || !r.AllAccepted() || f.connections.Load() != 2 {
		t.Fatal("broken connection reused")
	}
}

func TestSMTPPoolCapacityCloseAndImplicitTLS(t *testing.T) {
	hold := make(chan struct{})
	f := newSMTPFixture(t, smtpOptions{hold: hold, implicit: true})
	config := f.config()
	config.PoolSize = 2
	backend, err := NewSMTP(config)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan Delivery, 3)
	for range 3 {
		go func() { r, err := backend.Send(context.Background(), testMessage()); results <- Delivery{r, err} }()
	}
	for range 2 {
		select {
		case <-f.data:
		case <-time.After(2 * time.Second):
			t.Fatal("pool not active")
		}
	}
	if f.connections.Load() != 2 {
		t.Fatal("pool exceeded bound")
	}
	_ = backend.Close()
	for range 3 {
		select {
		case result := <-results:
			if result.Err == nil {
				t.Fatal("close did not interrupt")
			}
		case <-time.After(time.Second):
			t.Fatal("close unbounded")
		}
	}
	if _, err := backend.Send(context.Background(), testMessage()); !errors.Is(err, ErrClosed) {
		t.Fatal("closed backend reused")
	}
}

func TestSMTPResponseAndOperationTimeoutBounds(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		f := newSMTPFixture(t, smtpOptions{hugeBanner: true})
		backend, err := NewSMTP(f.config())
		if err != nil {
			t.Fatal(err)
		}
		defer backend.Close()
		r, err := backend.Send(context.Background(), testMessage())
		if err == nil || r.Recipients[0].State != NotAttempted {
			t.Fatal("unbounded banner accepted")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		hold := make(chan struct{})
		f := newSMTPFixture(t, smtpOptions{hold: hold})
		config := f.config()
		config.Timeout = 100 * time.Millisecond
		backend, err := NewSMTP(config)
		if err != nil {
			t.Fatal(err)
		}
		defer backend.Close()
		start := time.Now()
		r, err := backend.Send(context.Background(), testMessage())
		if !errors.Is(err, ErrUnknown) || r.Recipients[0].State != Unknown || time.Since(start) > time.Second {
			t.Fatal("operation timeout bound")
		}
	})
}

func TestSMTPCompletedContextCannotPoisonReusedConnection(t *testing.T) {
	f := newSMTPFixture(t, smtpOptions{})
	backend, err := NewSMTP(f.config())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for range 30 {
		ctx, cancel := context.WithCancel(context.Background())
		r, err := backend.Send(ctx, testMessage())
		cancel()
		if err != nil || !r.AllAccepted() {
			t.Fatal("completed context poisoned connection", err)
		}
		<-f.data
	}
	if f.connections.Load() != 1 {
		t.Fatal("healthy pooled connection was replaced")
	}
}

func TestSMTPPlaintextRequiresExplicitLoopbackDevelopment(t *testing.T) {
	f := newSMTPFixture(t, smtpOptions{})
	config := f.config()
	config.TLSMode = TLSPlaintext
	config.TLSConfig = nil
	config.Development = true
	backend, err := NewSMTP(config)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	r, err := backend.Send(context.Background(), testMessage())
	if err != nil || !r.AllAccepted() || f.authCount.Load() != 0 {
		t.Fatal("explicit unauthenticated loopback development failed", err)
	}
}
