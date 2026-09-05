package http

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/security"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

type Middleware func(http.Handler) http.Handler

func Chain(handler http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}
	return handler
}

type requestIDKey struct{}

func RequestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}
func RequestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := security.RandomToken(18)
		if err != nil {
			http.Error(w, "service unavailable", 503)
			return
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type trackedWriter struct {
	http.ResponseWriter
	written bool
}

func (w *trackedWriter) WriteHeader(status int) {
	if status >= 200 {
		w.written = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *trackedWriter) Write(value []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(value)
}
func (w *trackedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := &trackedWriter{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				if out.written {
					panic(http.ErrAbortHandler)
				}
				w.Header().Del("Content-Length")
				WriteError(w, r, errors.New("handler panic"))
			}
		}()
		next.ServeHTTP(out, r)
	})
}
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if maxBytes <= 0 {
				WriteError(w, r, ErrUnavailable)
				return
			}
			if r.ContentLength > maxBytes {
				WriteError(w, r, &Error{413, "BODY_TOO_LARGE", "Request body too large", nil, nil})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

type ServerConfig struct {
	Address                                                       string
	Handler                                                       http.Handler
	ReadHeaderTimeout, IdleTimeout, HandlerTimeout, ShutdownGrace time.Duration
	MaxHeaderBytes                                                int
	Streaming                                                     func(*http.Request) bool
}
type Server struct {
	server *http.Server
	grace  time.Duration
	ready  atomic.Bool
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.Handler == nil {
		return nil, errors.New("HTTP handler required")
	}
	if config.Address == "" {
		config.Address = "127.0.0.1:8000"
	}
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = 5 * time.Second
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 60 * time.Second
	}
	if config.HandlerTimeout == 0 {
		config.HandlerTimeout = 30 * time.Second
	}
	if config.ShutdownGrace == 0 {
		config.ShutdownGrace = 30 * time.Second
	}
	if config.MaxHeaderBytes == 0 {
		config.MaxHeaderBytes = 1 << 20
	}
	if config.ReadHeaderTimeout < 0 || config.IdleTimeout < 0 || config.HandlerTimeout < 0 || config.ShutdownGrace < 0 || config.MaxHeaderBytes < 1 {
		return nil, errors.New("invalid HTTP limits")
	}
	timed := http.TimeoutHandler(config.Handler, config.HandlerTimeout, "Request timed out")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if config.Streaming != nil && config.Streaming(r) {
			config.Handler.ServeHTTP(w, r)
		} else {
			timed.ServeHTTP(w, r)
		}
	})
	return &Server{server: &http.Server{Addr: config.Address, Handler: Chain(handler, Recovery, RequestIDs), ReadHeaderTimeout: config.ReadHeaderTimeout, IdleTimeout: config.IdleTimeout, MaxHeaderBytes: config.MaxHeaderBytes}, grace: config.ShutdownGrace}, nil
}
func (s *Server) Ready() bool { return s.ready.Load() }
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.ready.Store(true)
	done := make(chan error, 1)
	go func() {
		err := s.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		s.ready.Store(false)
		return err
	case <-ctx.Done():
		s.ready.Store(false)
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.grace)
		defer cancel()
		err := s.server.Shutdown(shutdown)
		if err != nil {
			err = errors.Join(err, s.server.Close())
		}
		return errors.Join(err, <-done)
	}
}
func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}
