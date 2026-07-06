package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/Newton-School/gogo/conf"
)

const RequestIDHeader = "X-Request-ID"

type contextKey string

const (
	requestIDContextKey contextKey = "gogo.request_id"
	accessLogContextKey contextKey = "gogo.access_log"
)

type accessLogFields struct {
	routeName string
}

// Handler is the HTTP handler boundary used by framework middleware.
type Handler = nethttp.Handler

// Middleware wraps one handler with request/response behavior.
type Middleware func(Handler) Handler

// MiddlewareFactory builds middleware from settings.
type MiddlewareFactory func(conf.Settings) Middleware

// MiddlewareRegistry maps configured middleware names to factories.
type MiddlewareRegistry map[string]MiddlewareFactory

// Chain applies middleware in order so the first middleware is the outermost wrapper.
func Chain(handler Handler, middleware ...Middleware) Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}
	return handler
}

// BuildMiddleware resolves configured middleware names using a registry.
func BuildMiddleware(settings conf.Settings, registry MiddlewareRegistry) ([]Middleware, error) {
	middleware := make([]Middleware, 0, len(settings.Middleware))
	for _, name := range settings.Middleware {
		factory, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("middleware %q is not registered", name)
		}
		middleware = append(middleware, factory(settings))
	}
	return middleware, nil
}

// BuiltInMiddlewareRegistry returns the framework built-in middleware registry.
func BuiltInMiddlewareRegistry(accessLog io.Writer) MiddlewareRegistry {
	return MiddlewareRegistry{
		"gogo.http.RequestIDMiddleware": func(conf.Settings) Middleware {
			return RequestIDMiddleware()
		},
		"gogo.http.PanicRecoveryMiddleware": func(conf.Settings) Middleware {
			return PanicRecoveryMiddleware()
		},
		"gogo.http.AccessLogMiddleware": func(conf.Settings) Middleware {
			return AccessLogMiddleware(accessLog)
		},
		"gogo.http.HostValidationMiddleware": func(settings conf.Settings) Middleware {
			return HostValidationMiddleware(settings.AllowedHosts)
		},
	}
}

// RequestIDMiddleware attaches a stable request ID to the response and context.
func RequestIDMiddleware() Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestID := strings.TrimSpace(r.Header.Get(RequestIDHeader))
			if requestID == "" {
				requestID = newRequestID()
			}
			w.Header().Set(RequestIDHeader, requestID)
			ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext returns the request ID attached by RequestIDMiddleware.
func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

// PanicRecoveryMiddleware converts panics into safe 500 responses.
func PanicRecoveryMiddleware() Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			defer func() {
				if recover() != nil {
					nethttp.Error(w, "Internal Server Error", nethttp.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLogMiddleware writes one structured JSON line per request.
func AccessLogMiddleware(writer io.Writer) Middleware {
	if writer == nil {
		writer = io.Discard
	}
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			recorder := &statusRecorder{ResponseWriter: w, status: nethttp.StatusOK}
			fields := &accessLogFields{}
			r = r.WithContext(context.WithValue(r.Context(), accessLogContextKey, fields))
			started := time.Now()
			next.ServeHTTP(recorder, r)

			entry := map[string]any{
				"duration_ms": time.Since(started).Milliseconds(),
				"host":        r.Host,
				"method":      r.Method,
				"path":        r.URL.Path,
				"remote_addr": r.RemoteAddr,
				"request_id":  RequestIDFromContext(r.Context()),
				"status":      recorder.status,
			}
			if fields.routeName != "" {
				entry["route_name"] = fields.routeName
			}
			_ = json.NewEncoder(writer).Encode(entry)
		})
	}
}

// HostValidationMiddleware rejects requests with hosts outside AllowedHosts.
func HostValidationMiddleware(allowedHosts []string) Middleware {
	return func(next Handler) Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			if len(allowedHosts) == 0 || hostAllowed(r.Host, allowedHosts) {
				next.ServeHTTP(w, r)
				return
			}
			nethttp.Error(w, "Bad Request", nethttp.StatusBadRequest)
		})
	}
}

type statusRecorder struct {
	nethttp.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = nethttp.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func setAccessLogRouteName(ctx context.Context, routeName string) {
	fields, _ := ctx.Value(accessLogContextKey).(*accessLogFields)
	if fields == nil {
		return
	}
	fields.routeName = routeName
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func hostAllowed(rawHost string, allowedHosts []string) bool {
	host := strings.ToLower(stripHostPort(rawHost))
	for _, allowed := range allowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "*" || allowed == host {
			return true
		}
		if strings.HasPrefix(allowed, ".") {
			domain := strings.TrimPrefix(allowed, ".")
			if host == domain || strings.HasSuffix(host, allowed) {
				return true
			}
		}
	}
	return false
}

func stripHostPort(rawHost string) string {
	host := rawHost
	if strings.HasPrefix(host, "[") {
		if end := strings.LastIndex(host, "]"); end >= 0 {
			return strings.Trim(host[:end+1], "[]")
		}
	}
	if withoutPort, _, err := net.SplitHostPort(host); err == nil {
		return withoutPort
	}
	if index := strings.LastIndex(host, ":"); index > -1 && strings.Count(host, ":") == 1 {
		return host[:index]
	}
	return host
}
