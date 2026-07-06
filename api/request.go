package api

import (
	"net"
	"net/http"

	"github.com/Newton-School/gogo/auth"
)

// Request wraps a standard HTTP request with API lifecycle metadata.
type Request struct {
	raw              *http.Request
	pathParams       map[string]string
	parsedBody       any
	user             auth.User
	authValue        any
	version          string
	acceptedRenderer string
}

// NewRequest wraps a standard HTTP request.
func NewRequest(raw *http.Request) *Request {
	return &Request{raw: raw, pathParams: map[string]string{}}
}

// Raw returns the underlying HTTP request.
func (r *Request) Raw() *http.Request {
	return r.raw
}

// QueryParam returns the first query parameter value.
func (r *Request) QueryParam(name string) string {
	return r.raw.URL.Query().Get(name)
}

// Method returns the HTTP request method.
func (r *Request) Method() string {
	return r.raw.Method
}

// RemoteIP returns the request remote address without a port when possible.
func (r *Request) RemoteIP() string {
	host, _, err := net.SplitHostPort(r.raw.RemoteAddr)
	if err == nil {
		return host
	}
	return r.raw.RemoteAddr
}

// WithPathParam attaches a resolved route path parameter.
func (r *Request) WithPathParam(name, value string) *Request {
	r.pathParams[name] = value
	return r
}

// PathParam returns a resolved route path parameter.
func (r *Request) PathParam(name string) string {
	return r.pathParams[name]
}

// WithParsedBody attaches the parsed request body.
func (r *Request) WithParsedBody(body any) *Request {
	r.parsedBody = body
	return r
}

// ParsedBody returns the parsed request body.
func (r *Request) ParsedBody() any {
	return r.parsedBody
}

// WithUser attaches the authenticated user.
func (r *Request) WithUser(user auth.User) *Request {
	r.user = user
	return r
}

// User returns the authenticated user.
func (r *Request) User() auth.User {
	return r.user
}

// WithAuth attaches authentication metadata.
func (r *Request) WithAuth(value any) *Request {
	r.authValue = value
	return r
}

// Auth returns authentication metadata.
func (r *Request) Auth() any {
	return r.authValue
}

// WithVersion attaches the resolved API version.
func (r *Request) WithVersion(version string) *Request {
	r.version = version
	return r
}

// Version returns the resolved API version.
func (r *Request) Version() string {
	return r.version
}

// WithAcceptedRenderer attaches the selected renderer name.
func (r *Request) WithAcceptedRenderer(renderer string) *Request {
	r.acceptedRenderer = renderer
	return r
}

// AcceptedRenderer returns the selected renderer name.
func (r *Request) AcceptedRenderer() string {
	return r.acceptedRenderer
}
