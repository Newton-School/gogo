package http

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/urls"
)

// ErrGenericConfiguration reports invalid generic-view construction without
// retaining template names, application data, provider errors or callback text.
var ErrGenericConfiguration = errors.New("http: invalid generic view configuration")

// ReadViewOptions is the common access boundary for read-only generic views.
// Authorize is required, including for deliberately public views. It runs before
// reading and again before emission. Return exactly auth.ErrUnauthenticated,
// auth.ErrPermissionDenied or ErrNotFound for a public denial; other errors and
// panics fail closed with 503. Hooks must be read-only and honor cancellation.
// Timeout bounds cooperative work, not the lifetime of non-cooperative Go code.
type ReadViewOptions struct {
	Authorize    func(*http.Request) error
	AllowOptions bool
	Timeout      time.Duration
}

type readViewCall struct {
	base        *http.Request
	routeParams map[string]any
}

// A callback gets its own HTTP metadata. Context services and TLS connection
// metadata remain trusted application-owned values, not a general sandbox.
func (c *readViewCall) request() *http.Request { return c.base.Clone(c.base.Context()) }
func (c *readViewCall) params() map[string]any {
	copy := make(map[string]any, len(c.routeParams))
	for k, v := range c.routeParams {
		copy[k] = v
	}
	return copy
}
func (c *readViewCall) rawQuery() string { return c.base.URL.RawQuery }

type readViewResult struct {
	response Response
	// finalize may recheck a resource-specific grant after the ordinary grant.
	// It must not perform reads, mutate business data or widen the representation.
	finalize func(*readViewCall) error
}

func newReadView(options ReadViewOptions, run func(*readViewCall) (readViewResult, error)) (http.Handler, error) {
	if options.Authorize == nil || run == nil || options.Timeout < 0 || options.Timeout > time.Minute || options.Timeout > 0 && options.Timeout < time.Millisecond {
		return nil, ErrGenericConfiguration
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		response, frozen, err := executeReadView(req, options, run)
		if err != nil {
			response = genericErrorResponse(err)
		}
		if frozen == nil {
			frozen = &http.Request{Method: http.MethodGet, Header: make(http.Header)}
		}
		if response.Headers == nil {
			response.Headers = make(http.Header)
		}
		response.Headers.Set("Cache-Control", "private, no-store")
		response.Headers.Set("X-Content-Type-Options", "nosniff")
		response.Headers.Set("Content-Length", strconv.Itoa(len(response.Body)))
		if err := response.Write(w, frozen); err != nil {
			// All rendering/validation precedes writing. Any transport error here
			// must abort the response; never append a second error document.
			panic(http.ErrAbortHandler)
		}
	}), nil
}

func executeReadView(req *http.Request, options ReadViewOptions, run func(*readViewCall) (readViewResult, error)) (response Response, frozen *http.Request, err error) {
	defer func() {
		if recover() != nil {
			response, err = Response{}, ErrUnavailable
		}
	}()
	// Freeze the original method even when malformed input fails before a full
	// snapshot. HEAD errors still have the same headers and no response body.
	frozen = &http.Request{Method: http.MethodGet, Header: make(http.Header)}
	if req == nil {
		return genericStatusResponse(400), frozen, nil
	}
	frozen.Method = req.Method
	allow := "GET, HEAD"
	if options.AllowOptions {
		allow += ", OPTIONS"
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead && !(options.AllowOptions && req.Method == http.MethodOptions) {
		response = genericStatusResponse(405)
		response.Headers.Set("Allow", allow)
		return response, frozen, nil
	}
	var valid bool
	frozen, valid = freezeReadRequest(req)
	if !valid {
		return genericStatusResponse(400), frozen, nil
	}
	// Only immutable scalar converter values cross this view boundary. The
	// router's ordinary behavior and custom-converter contract remain unchanged.
	params, err := freezeReadParams(frozen)
	if err != nil {
		return Response{}, frozen, err
	}
	ctx, cancel := context.WithTimeout(frozen.Context(), options.Timeout)
	defer cancel()
	frozen = frozen.WithContext(ctx)
	call := &readViewCall{base: frozen, routeParams: params}
	if !genericContextOK(ctx) {
		return Response{}, frozen, ErrUnavailable
	}
	err = options.Authorize(call.request())
	if !genericContextOK(ctx) {
		return Response{}, frozen, ErrUnavailable
	}
	if err != nil {
		return Response{}, frozen, err
	}
	result := readViewResult{response: Response{Status: 200, Headers: http.Header{"Allow": {allow}}}}
	if frozen.Method != http.MethodOptions {
		result, err = run(call)
	}
	if !genericContextOK(ctx) {
		return Response{}, frozen, ErrUnavailable
	}
	if err != nil {
		return Response{}, frozen, err
	}
	response, err = freezeReadResponse(result.response)
	if err != nil {
		return Response{}, frozen, err
	}
	err = options.Authorize(call.request())
	if !genericContextOK(ctx) {
		return Response{}, frozen, ErrUnavailable
	}
	if err != nil {
		return Response{}, frozen, err
	}
	if result.finalize != nil {
		err = result.finalize(call)
	}
	if !genericContextOK(ctx) {
		return Response{}, frozen, ErrUnavailable
	}
	if err != nil {
		return Response{}, frozen, err
	}
	return response, frozen, nil
}

func genericContextOK(ctx context.Context) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return ctx != nil && ctx.Err() == nil
}

func freezeReadRequest(req *http.Request) (*http.Request, bool) {
	copy := *req
	copy.Header = make(http.Header)
	if req.URL == nil || req.URL.User != nil || req.URL.Opaque != "" || req.URL.Fragment != "" || req.URL.RawFragment != "" || len(req.Host) > 1024 || len(req.RequestURI) > 16<<10 || len(req.RemoteAddr) > 1024 || len(req.Proto) > 64 || req.ContentLength != 0 || req.Body != nil && req.Body != http.NoBody || len(req.TransferEncoding) != 0 || len(req.Trailer) != 0 {
		return &copy, false
	}
	remaining := 16 << 10
	for _, value := range []string{req.URL.Scheme, req.URL.Host, req.URL.Path, req.URL.RawPath, req.URL.RawQuery} {
		if len(value) > remaining || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return &copy, false
		}
		remaining -= len(value)
	}
	if req.URL.Scheme != "" && req.URL.Scheme != "http" && req.URL.Scheme != "https" || !strings.HasPrefix(req.URL.Path, "/") {
		return &copy, false
	}
	if req.URL.RawPath != "" {
		decoded, err := url.PathUnescape(req.URL.RawPath)
		if err != nil || decoded != req.URL.Path {
			return &copy, false
		}
	}
	urlCopy := *req.URL
	copy.URL = &urlCopy
	// Read views do not inherit potentially aliased middleware form caches or a
	// transport body. FormValue may parse the frozen query in a callback's clone.
	copy.Form, copy.PostForm, copy.MultipartForm = nil, nil, nil
	copy.Body, copy.GetBody, copy.Response = http.NoBody, nil, nil
	copy.Trailer, copy.TransferEncoding = nil, nil
	bytes, entries := 0, 0
	if len(req.Header) > 256 {
		return &copy, false
	}
	for k, values := range req.Header {
		bytes += len(k)
		if !validHeaderName(k) || len(values) > 1024-entries {
			return &copy, false
		}
		entries += len(values)
		for _, value := range values {
			bytes += len(value)
			if bytes > 64<<10 || strings.ContainsAny(value, "\r\n\x00") {
				return &copy, false
			}
		}
		copy.Header[k] = append([]string(nil), values...)
	}
	if bytes > 64<<10 {
		return &copy, false
	}
	// Request.Clone also detaches net/http's private PathValue match storage.
	// Merely copying the exported fields leaves SetPathValue aliases behind.
	return copy.Clone(copy.Context()), true
}

func freezeReadParams(req *http.Request) (map[string]any, error) {
	params := urls.Params(req)
	if len(params) > 64 {
		return nil, ErrUnavailable
	}
	bytes := 0
	for k, value := range params {
		bytes += len(k)
		if bytes > 16<<10 {
			return nil, ErrUnavailable
		}
		if value == nil || !utf8.ValidString(k) {
			return nil, ErrUnavailable
		}
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		case reflect.Float32, reflect.Float64:
			if math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0) {
				return nil, ErrUnavailable
			}
		case reflect.String:
			bytes += len(v.String())
			if !utf8.ValidString(v.String()) {
				return nil, ErrUnavailable
			}
		default:
			return nil, ErrUnavailable
		}
		if bytes > 16<<10 {
			return nil, ErrUnavailable
		}
	}
	return params, nil
}

func freezeReadResponse(response Response) (Response, error) {
	if response.Render != nil || response.ETag != "" || !response.LastModified.IsZero() || response.Status < 200 || response.Status > 599 || len(response.Body) > 16<<20 || len(response.Headers) > 32 {
		return Response{}, ErrUnavailable
	}
	bytes := 0
	for k, values := range response.Headers {
		if !validHeaderName(k) || len(values) > 16 {
			return Response{}, ErrUnavailable
		}
		bytes += len(k)
		if bytes > 16<<10 {
			return Response{}, ErrUnavailable
		}
		for _, v := range values {
			bytes += len(v)
			if bytes > 16<<10 || strings.ContainsAny(v, "\r\n\x00") {
				return Response{}, ErrUnavailable
			}
		}
	}
	response.Headers = response.Headers.Clone()
	response.Body = append([]byte(nil), response.Body...)
	return response, nil
}

func genericErrorResponse(err error) Response {
	status := 503
	switch err {
	case auth.ErrUnauthenticated:
		status = 401
	case auth.ErrPermissionDenied:
		status = 403
	case ErrNotFound:
		status = 404
	}
	return genericStatusResponse(status)
}

func genericStatusResponse(status int) Response {
	return Text(status, http.StatusText(status)+"\n")
}
