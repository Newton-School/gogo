package http

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

// CreateViewOptions declares a local-scalar HTML creation form. Fields is an
// explicit input allowlist, not a saved-object output projection. ReadonlyFields
// are omitted from controls and cannot be submitted. All hooks are trusted
// application code; authorization hooks must be read-only and honor cancellation.
type CreateViewOptions struct {
	TemplateViewOptions
	Store          *orm.Store
	Model          string
	Policy         auth.Policy
	AllowAnonymous bool
	Scope          func(context.Context, auth.Principal, models.Schema) (db.Predicate, error)
	Factory        func() models.Model
	Fields         []string
	ReadonlyFields []string
	// Initial affects unbound editable controls only. It never supplies omitted
	// POST values. Prepare fills server-owned data after successful binding.
	Initial func(*http.Request) (map[string]any, error)
	Prepare func(context.Context, models.Record) error
	// ValidateWrite is mandatory and receives a detached proposed/saved record.
	ValidateWrite func(context.Context, auth.Principal, models.Record) error
	// SuccessURL receives only detached, persisted primary-key values. The local
	// URL is sealed before commit, followed by a final persisted-state fence.
	SuccessURL func(context.Context, map[string]any) (string, error)
	// CSRF cannot contain Exempt. Nil uses secure cookies and builtin defaults.
	CSRF *security.CSRFConfig
	// MaxBodyBytes defaults to 1 MiB; the hard maximum is 10 MiB. Only URL forms
	// are accepted. Files, relations and custom field codecs are unsupported.
	MaxBodyBytes int64
}

type genericCreate struct {
	options     CreateViewOptions
	model       *genericModel
	template    *genericTemplate
	csrf        func(http.Handler) http.Handler
	editable    map[string]bool
	fingerprint string
}

// NewCreateView constructs an immutable GET/HEAD/POST form handler. Safe methods
// do not open transactions or save records. POST uses builtin CSRF and an owned
// durable force-insert transaction; it never updates an existing identity.
func NewCreateView(options CreateViewOptions) (handler http.Handler, err error) {
	defer func() {
		if recover() != nil {
			handler, err = nil, ErrGenericConfiguration
		}
	}()
	if options.Factory == nil || options.ValidateWrite == nil || options.SuccessURL == nil || options.Authorize == nil || options.Timeout < 0 || options.Timeout > time.Minute || options.Timeout > 0 && options.Timeout < time.Millisecond || options.MaxBodyBytes < 0 || options.MaxBodyBytes > 10<<20 {
		return nil, ErrGenericConfiguration
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = 1 << 20
	}
	options.Fields = append([]string(nil), options.Fields...)
	options.ReadonlyFields = append([]string(nil), options.ReadonlyFields...)
	if options.Templates.LocaleResolver != nil {
		copy := *options.Templates.LocaleResolver
		options.Templates.LocaleResolver = &copy
	}
	model, editable, fingerprint, err := newGenericCreateModel(options)
	if err != nil {
		return nil, err
	}
	renderer, err := newGenericTemplate(options.TemplateViewOptions)
	if err != nil {
		return nil, err
	}
	csrf := security.CSRFConfig{Secure: true}
	if options.CSRF != nil {
		csrf = *options.CSRF
		csrf.TrustedOrigins = append([]string(nil), csrf.TrustedOrigins...)
	}
	if csrf.Exempt != nil || len(csrf.TrustedOrigins) > 64 || len(csrf.CookieName) > 128 {
		return nil, ErrGenericConfiguration
	}
	if csrf.CookieName != "" && (&http.Cookie{Name: csrf.CookieName, Value: "configured"}).Valid() != nil {
		return nil, ErrGenericConfiguration
	}
	for _, origin := range csrf.TrustedOrigins {
		if len(origin) > 2048 {
			return nil, ErrGenericConfiguration
		}
	}
	csrf.MaxBodyBytes = options.MaxBodyBytes
	csrfMiddleware, err := security.CSRF(csrf)
	if err != nil {
		return nil, ErrGenericConfiguration
	}
	view := &genericCreate{options: options, model: model, template: renderer, csrf: csrfMiddleware, editable: editable, fingerprint: fingerprint}
	return http.HandlerFunc(view.serve), nil
}

func (v *genericCreate) serve(w http.ResponseWriter, r *http.Request) {
	response, frozen, err := v.execute(r)
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
		panic(http.ErrAbortHandler)
	}
}

func (v *genericCreate) execute(r *http.Request) (response Response, frozen *http.Request, err error) {
	defer func() {
		if recover() != nil {
			response, err = Response{}, ErrUnavailable
		}
	}()
	frozen = &http.Request{Method: http.MethodGet, Header: make(http.Header)}
	if r == nil {
		return genericStatusResponse(400), frozen, nil
	}
	frozen.Method = r.Method
	allow := "GET, HEAD, POST"
	if v.options.AllowOptions {
		allow += ", OPTIONS"
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost && !(r.Method == http.MethodOptions && v.options.AllowOptions) {
		response = genericStatusResponse(405)
		response.Headers.Set("Allow", allow)
		return response, frozen, nil
	}
	body, length := r.Body, r.ContentLength
	copy := *r
	if r.Method == http.MethodPost {
		if len(r.Trailer) != 0 || len(r.TransferEncoding) > 1 || len(r.TransferEncoding) == 1 && r.TransferEncoding[0] != "chunked" || length < -1 {
			return genericStatusResponse(400), frozen, nil
		}
		copy.Body, copy.GetBody, copy.ContentLength, copy.TransferEncoding = http.NoBody, nil, 0, nil
	}
	var valid bool
	frozen, valid = freezeReadRequest(&copy)
	if !valid {
		return genericStatusResponse(400), frozen, nil
	}
	params, err := freezeReadParams(frozen)
	if err != nil {
		return Response{}, frozen, err
	}
	ctx, cancel := context.WithTimeout(frozen.Context(), v.options.Timeout)
	defer cancel()
	if !genericContextOK(ctx) {
		return Response{}, frozen, ErrUnavailable
	}
	if resolver := v.options.Templates.LocaleResolver; resolver != nil {
		ctx, err = resolver.WithLocale(ctx, i18n.Preferences{})
		if err != nil || !genericContextOK(ctx) {
			return Response{}, frozen, ErrUnavailable
		}
	}
	frozen = frozen.WithContext(ctx)
	call := &readViewCall{base: frozen, routeParams: params}
	if err := v.authorize(call); err != nil {
		return Response{}, frozen, err
	}
	if frozen.Method == http.MethodOptions {
		if err := v.authorize(call); err != nil {
			return Response{}, frozen, err
		}
		return Response{Status: 200, Headers: http.Header{"Allow": {allow}}}, frozen, nil
	}
	var data url.Values
	if frozen.Method == http.MethodPost {
		media, parameters, parseErr := mime.ParseMediaType(frozen.Header.Get("Content-Type"))
		if parseErr != nil || media != "application/x-www-form-urlencoded" || len(parameters) > 1 || len(parameters) == 1 && !strings.EqualFold(parameters["charset"], "utf-8") {
			return genericStatusResponse(415), frozen, nil
		}
		if length > v.options.MaxBodyBytes {
			return genericStatusResponse(413), frozen, nil
		}
		raw := []byte{}
		if body != nil {
			raw, err = io.ReadAll(io.LimitReader(body, v.options.MaxBodyBytes+1))
		}
		if !genericContextOK(ctx) || err != nil {
			return Response{}, frozen, ErrUnavailable
		}
		if int64(len(raw)) > v.options.MaxBodyBytes {
			return genericStatusResponse(413), frozen, nil
		}
		if !utf8.Valid(raw) || bytes.Count(raw, []byte("&")) >= 256 {
			return genericStatusResponse(400), frozen, nil
		}
		data, err = url.ParseQuery(string(raw))
		if err != nil || !v.validData(data) {
			return genericStatusResponse(400), frozen, nil
		}
	}
	// Run the actual builtin middleware against private form caches and a
	// bounded materialized writer. Nothing reaches the socket before completion.
	csrfRequest := call.request()
	csrfRequest.PostForm = data
	csrfRequest.Form = make(url.Values)
	for k, values := range data {
		csrfRequest.Form[k] = append([]string(nil), values...)
	}
	capture := &genericCreateCapture{header: make(http.Header)}
	called := false
	v.csrf(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		called = true
		inner := &readViewCall{base: req, routeParams: params}
		response, err = v.run(inner, data)
	})).ServeHTTP(capture, csrfRequest)
	if !called {
		if capture.status == 403 {
			return genericStatusResponse(403), frozen, nil
		}
		return Response{}, frozen, ErrUnavailable
	}
	if err != nil {
		return Response{}, frozen, err
	}
	if response.Headers == nil {
		response.Headers = make(http.Header)
	}
	for key, values := range capture.header {
		response.Headers[key] = append([]string(nil), values...)
	}
	return response, frozen, nil
}

func (v *genericCreate) authorize(call *readViewCall) error {
	ctx := call.base.Context()
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	err := v.options.Authorize(call.request())
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	principal := auth.FromContext(ctx)
	if !principal.Authenticated && !v.model.allowAnonymous {
		return auth.ErrUnauthenticated
	}
	if principal.Authenticated && (!principal.Active || principal.ID == "") {
		return auth.ErrPermissionDenied
	}
	err = v.model.policy.Authorize(ctx, principal, "add", auth.Resource{App: v.model.schema.AppLabel, Model: v.model.schema.Name})
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	return err
}

func (v *genericCreate) validData(data url.Values) bool {
	if len(data) > len(v.editable)+1 {
		return false
	}
	for key, values := range data {
		if key != "csrfmiddlewaretoken" && !v.editable[key] || len(values) != 1 || len(key) > 128 || !utf8.ValidString(key) || strings.ContainsRune(values[0], 0) || !utf8.ValidString(values[0]) {
			return false
		}
	}
	return true
}

type genericCreateCapture struct {
	header        http.Header
	status, bytes int
}

func (w *genericCreateCapture) Header() http.Header { return w.header }
func (w *genericCreateCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *genericCreateCapture) Write(p []byte) (int, error) {
	if len(p) > 1024-w.bytes {
		return 0, io.ErrShortWrite
	}
	w.bytes += len(p)
	if w.status == 0 {
		w.status = 200
	}
	return len(p), nil
}
