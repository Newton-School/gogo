package http

import (
	"context"
	"errors"
	"net"
	nethttp "net/http"
	"time"

	"github.com/Newton-School/gogo/app"
	"github.com/Newton-School/gogo/conf"
)

const (
	defaultHealthPath        = "/health/"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
)

// HealthCheck checks whether the server should report healthy.
type HealthCheck func(context.Context) error

// ServerConfig configures the HTTP server runtime.
type ServerConfig struct {
	Settings          conf.Settings
	Registry          *app.Registry
	Router            *Router
	Middleware        []Middleware
	HealthPath        string
	HealthCheck       HealthCheck
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// Server owns the framework HTTP runtime.
type Server struct {
	registry        *app.Registry
	handler         Handler
	httpServer      *nethttp.Server
	shutdownTimeout time.Duration
	healthCheck     HealthCheck
}

// NewServer builds a server from settings, app registry, router, and middleware.
func NewServer(config ServerConfig) (*Server, error) {
	registry := config.Registry
	if registry == nil {
		registry = app.NewRegistry()
	}
	router := config.Router
	if router == nil {
		router = NewRouter()
	}

	server := &Server{
		registry:        registry,
		shutdownTimeout: durationOrDefault(config.ShutdownTimeout, defaultShutdownTimeout),
		healthCheck:     config.HealthCheck,
	}

	mux := nethttp.NewServeMux()
	healthPath := config.HealthPath
	if healthPath == "" {
		healthPath = defaultHealthPath
	}
	mux.HandleFunc(healthPath, server.health)
	if shouldMountDevelopmentFiles(config.Settings.StaticURL, config.Settings.StaticRoot) {
		if err := MountStatic(mux, StaticMountConfig{Env: config.Settings.Env, URLPrefix: config.Settings.StaticURL, Root: config.Settings.StaticRoot}); err != nil {
			return nil, err
		}
	}
	if shouldMountDevelopmentFiles(config.Settings.MediaURL, config.Settings.MediaRoot) {
		if err := MountStatic(mux, StaticMountConfig{Env: config.Settings.Env, URLPrefix: config.Settings.MediaURL, Root: config.Settings.MediaRoot}); err != nil {
			return nil, err
		}
	}
	mux.Handle("/", router)

	server.handler = Chain(mux, config.Middleware...)
	server.httpServer = &nethttp.Server{
		Addr:              addressOrDefault(config.Settings.HTTPAddr),
		Handler:           server.handler,
		ReadHeaderTimeout: durationOrDefault(config.ReadHeaderTimeout, defaultReadHeaderTimeout),
		ReadTimeout:       durationOrDefault(config.ReadTimeout, defaultReadTimeout),
		WriteTimeout:      durationOrDefault(config.WriteTimeout, defaultWriteTimeout),
		IdleTimeout:       durationOrDefault(config.IdleTimeout, defaultIdleTimeout),
	}
	return server, nil
}

// Handler returns the composed HTTP handler.
func (s *Server) Handler() Handler {
	return s.handler
}

// HTTPServer returns the underlying standard HTTP server.
func (s *Server) HTTPServer() *nethttp.Server {
	return s.httpServer
}

// ListenAndServe listens on the configured address until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve serves using an existing listener until the context is cancelled.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if err := s.registry.Ready(ctx); err != nil {
		_ = listener.Close()
		return err
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
			defer cancel()
			_ = s.httpServer.Shutdown(shutdownCtx)
			_ = s.registry.Shutdown(shutdownCtx)
		case <-done:
		}
	}()

	err := s.httpServer.Serve(listener)
	close(done)
	if errors.Is(err, nethttp.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) health(w nethttp.ResponseWriter, r *nethttp.Request) {
	if s.healthCheck != nil {
		if err := s.healthCheck(r.Context()); err != nil {
			nethttp.Error(w, "unhealthy", nethttp.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func addressOrDefault(addr string) string {
	if addr != "" {
		return addr
	}
	return ":8000"
}

func shouldMountDevelopmentFiles(urlPrefix, root string) bool {
	return urlPrefix != "" && root != ""
}
