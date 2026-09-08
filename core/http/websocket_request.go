package http

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func webSocketOrigin(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsSpace) || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", false
	}
	// Exact configured origins; no request Host fallback or wildcard pattern.
	if strings.ContainsAny(u.Host, "*%") || u.Hostname() == "" {
		return "", false
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), true
}

func webSocketRequest(r *http.Request) (*http.Request, bool) {
	if r == nil || r.URL == nil || r.Method != http.MethodGet || r.ProtoMajor != 1 || r.ProtoMinor != 1 || r.ContentLength != 0 || r.Body != nil && r.Body != http.NoBody || r.GetBody != nil || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 {
		return nil, false
	}
	u := *r.URL
	if u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || !strings.HasPrefix(u.Path, "/") || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	budget := 16 << 10
	for _, v := range []string{r.Proto, r.Host, r.RemoteAddr, r.RequestURI, u.Scheme, u.Host, u.Path, u.RawPath, u.RawQuery} {
		budget -= len(v)
		if budget < 0 || !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl) {
			return nil, false
		}
	}
	if u.RawPath != "" {
		decoded, err := url.PathUnescape(u.RawPath)
		if err != nil || decoded != u.Path {
			return nil, false
		}
	}
	if len(r.Header) > 128 {
		return nil, false
	}
	headers := make(http.Header, len(r.Header))
	count := 0
	budget = 32 << 10
	for key, values := range r.Header {
		if len(key) > budget {
			return nil, false
		}
		if key == "Transfer-Encoding" || key == "Trailer" || key == "Expect" || key == "Content-Length" && (len(values) != 1 || values[0] != "0") {
			return nil, false
		}
		if !validHeaderName(key) || textproto.CanonicalMIMEHeaderKey(key) != key {
			return nil, false
		}
		budget -= len(key)
		count += len(values)
		if count > 256 || budget < 0 {
			return nil, false
		}
		for _, v := range values {
			budget -= len(v)
			if budget < 0 || !utf8.ValidString(v) || strings.ContainsFunc(v, func(c rune) bool { return unicode.IsControl(c) && c != '\t' }) {
				return nil, false
			}
		}
		headers[key] = append([]string(nil), values...)
	}
	// Reconstruct instead of copying Request's opaque PathValue storage, TLS,
	// form caches or body. Gogo route parameters remain trusted context values.
	copy := &http.Request{Method: r.Method, URL: &u, Proto: r.Proto, ProtoMajor: 1, ProtoMinor: 1, Header: headers, Body: http.NoBody, Host: r.Host, RemoteAddr: r.RemoteAddr, RequestURI: r.RequestURI}
	return copy.WithContext(r.Context()), true
}
func (c *webSocketConfig) handshake(r *http.Request) bool {
	single := func(name string) (string, bool) {
		values := r.Header.Values(name)
		returnValue := ""
		if len(values) == 1 {
			returnValue = values[0]
		}
		return returnValue, len(values) == 1
	}
	key, ok := single("Sec-WebSocket-Key")
	if !ok {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 16 || base64.StdEncoding.EncodeToString(decoded) != key {
		return false
	}
	version, ok := single("Sec-WebSocket-Version")
	if !ok || version != "13" {
		return false
	}
	if !webSocketHeaderToken(r.Header, "Connection", "upgrade", false) || !webSocketHeaderToken(r.Header, "Upgrade", "websocket", false) || !webSocketHeaderToken(r.Header, "Sec-WebSocket-Protocol", WebSocketProtocol, true) {
		return false
	}
	origin, present := r.Header["Origin"]
	if !present {
		return c.options.AllowMissingOrigin
	}
	if len(origin) != 1 {
		return false
	}
	normalized, ok := webSocketOrigin(origin[0])
	return ok && c.origins[normalized]
}
func webSocketHeaderToken(h http.Header, key, want string, exact bool) bool {
	found := false
	for _, line := range h.Values(key) {
		for _, item := range strings.Split(line, ",") {
			token := strings.TrimSpace(item)
			if token == "" {
				return false
			}
			if exact {
				if token == want {
					found = true
				}
			} else if strings.EqualFold(token, want) {
				found = true
			}
		}
	}
	return found
}

// Resolve a real hijacker before writing 101. The library otherwise recursively
// unwraps without a bound. Provider wrappers remain trusted application code.
func webSocketHijacker(w http.ResponseWriter) (http.Hijacker, bool) {
	for range 16 {
		if w == nil {
			return nil, false
		}
		if h, ok := w.(http.Hijacker); ok {
			return h, true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil, false
		}
		w = u.Unwrap()
	}
	return nil, false
}

type webSocketUpgradeWriter struct {
	http.ResponseWriter
	hijacker  http.Hijacker
	session   *webSocketSession
	attempted bool
	written   bool
}

type webSocketResponseWriter struct {
	http.ResponseWriter
	started bool
}

func (w *webSocketResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *webSocketResponseWriter) WriteHeader(status int) {
	if status == 101 || status >= 200 {
		w.started = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *webSocketResponseWriter) Write(value []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(value)
}

func (w *webSocketUpgradeWriter) WriteHeader(status int) {
	if w.attempted {
		return
	}
	if status == 101 {
		w.attempted = true
	}
	if status >= 200 {
		w.written = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *webSocketUpgradeWriter) Write(value []byte) (int, error) {
	// Accept may attempt an HTTP error after a failed Hijack. No bytes may be
	// appended after 101, even on a transport-provider failure.
	if w.attempted {
		return 0, net.ErrClosed
	}
	w.written = true
	n, err := w.ResponseWriter.Write(value)
	if err != nil || n != len(value) {
		panic(http.ErrAbortHandler)
	}
	return n, err
}
func (w *webSocketUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.attempted = true
	conn, buffer, err := w.hijacker.Hijack()
	if conn != nil {
		w.session.capture(conn)
	}
	return conn, buffer, err
}
func webSocketHTTPError(w http.ResponseWriter, r *http.Request, status int) {
	response := genericStatusResponse(status)
	response.Headers.Set("Cache-Control", "private, no-store")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
	if status == 405 {
		response.Headers.Set("Allow", "GET")
	}
	if r == nil {
		r = &http.Request{Method: http.MethodGet, Header: make(http.Header)}
	}
	if err := response.Write(w, r); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func webSocketFreshRequest(r *http.Request, ctx context.Context) *http.Request { return r.Clone(ctx) }
