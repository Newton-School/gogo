package http

import (
	"context"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Newton-School/gogo/app"
	"github.com/Newton-School/gogo/conf"
)

func TestServerHandlerServesRoutesAndHealth(t *testing.T) {
	router := NewRouter()
	if err := router.Handle("index", "/", func(context.Context, *Request) Response {
		return Text(200, "index")
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	server, err := NewServer(ServerConfig{
		Settings:   conf.DefaultSettings(),
		Registry:   app.NewRegistry(),
		Router:     router,
		HealthPath: "/health/",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	response, err := nethttp.Get(testServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(body) != "index" {
		t.Fatalf("GET / = (%d, %q), want (200, index)", response.StatusCode, body)
	}

	health, err := nethttp.Get(testServer.URL + "/health/")
	if err != nil {
		t.Fatalf("GET /health/ error = %v", err)
	}
	defer health.Body.Close()
	if health.StatusCode != 200 {
		t.Fatalf("health status = %d, want 200", health.StatusCode)
	}
}

func TestServerAppliesMiddleware(t *testing.T) {
	router := NewRouter()
	if err := router.Handle("index", "/", func(context.Context, *Request) Response {
		return Text(200, "index")
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	server, err := NewServer(ServerConfig{
		Settings: conf.DefaultSettings(),
		Registry: app.NewRegistry(),
		Router:   router,
		Middleware: []Middleware{
			func(next Handler) Handler {
				return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
					w.Header().Set("X-Middleware", "yes")
					next.ServeHTTP(w, r)
				})
			},
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	if got := recorder.Header().Get("X-Middleware"); got != "yes" {
		t.Fatalf("X-Middleware = %q, want yes", got)
	}
}

func TestServerServesStaticFilesInDevelopment(t *testing.T) {
	staticRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticRoot, "blog"), 0o755); err != nil {
		t.Fatalf("mkdir static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticRoot, "blog", "app.css"), []byte("body{}\n"), 0o644); err != nil {
		t.Fatalf("write static file: %v", err)
	}

	settings := conf.DefaultSettings()
	settings.Env = "development"
	settings.StaticURL = "/static/"
	settings.StaticRoot = staticRoot

	server, err := NewServer(ServerConfig{
		Settings: settings,
		Registry: app.NewRegistry(),
		Router:   NewRouter(),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/static/blog/app.css", nil))
	if recorder.Code != nethttp.StatusOK || recorder.Body.String() != "body{}\n" {
		t.Fatalf("GET static = (%d, %q), want (200, body{})", recorder.Code, recorder.Body.String())
	}
}

func TestServerHTTPServerUsesSecureTimeouts(t *testing.T) {
	server, err := NewServer(ServerConfig{Settings: conf.DefaultSettings(), Registry: app.NewRegistry(), Router: NewRouter()})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	standard := server.HTTPServer()
	if standard.ReadHeaderTimeout <= 0 || standard.ReadTimeout <= 0 || standard.WriteTimeout <= 0 || standard.IdleTimeout <= 0 {
		t.Fatalf("timeouts must all be set: %#v", standard)
	}
}

func TestServerServeShutsDownWhenContextIsCancelled(t *testing.T) {
	server, err := NewServer(ServerConfig{
		Settings:        conf.DefaultSettings(),
		Registry:        app.NewRegistry(),
		Router:          NewRouter(),
		ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()

	waitForServer(t, listener.Addr().String())
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Serve() did not shut down after context cancellation")
	}
}

func waitForServer(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err := nethttp.Get("http://" + addr + "/health/")
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not start", addr)
}
