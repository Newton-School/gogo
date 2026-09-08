package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

var errDocumentationUnavailable = errors.New("admin: documentation unavailable")

type documentationBudget struct{ bytes int }

func (b *documentationBudget) text(value string, maximum int) bool {
	if len(value) > maximum || len(value) > b.bytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	b.bytes -= len(value)
	return true
}

type documentationModel struct {
	app, name, description string
	fields                 []documentationField
	authorize              func(context.Context, auth.Principal, string, Object) error
}

type documentationField struct {
	name, kind, description            string
	nullable, editable, primary, blank bool
}

type documentationPage struct {
	site      *Site
	path      string
	models    []documentationModel
	routes    []DocumentationRoute
	views     []DocumentationView
	tags      []DocumentationExtension
	filters   []DocumentationExtension
	protected http.Handler
}

// DocumentationHandler is the concrete, fixed-template seam used by the
// optional admin/admindocs package. Register models first: a successful call
// freezes this Site just as Handler does. Only Prefix + "doc/" is served.
// Current sessions/auth middleware must surround this explicit mount, as for
// the Site itself. A request needs active staff, the documentation policy grant
// and each disclosed model's existing view policy. No model data is queried.
func (s *Site) DocumentationHandler(options DocumentationOptions) (http.Handler, error) {
	if s == nil || len(options.Models) > 256 || len(options.Routes) > 4096 || len(options.Views) > 4096 || len(options.Tags) > 512 || len(options.Filters) > 512 {
		return nil, ErrDocumentation
	}
	budget := documentationBudget{bytes: 1 << 20}
	d := &documentationPage{}
	if !d.descriptors(options, &budget) {
		return nil, ErrDocumentation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine == nil || s.config.Policy == nil || !strings.HasPrefix(s.config.Prefix, "/") || !strings.HasSuffix(s.config.Prefix, "/") || s.config.Prefix != "/" && path.Clean(s.config.Prefix)+"/" != s.config.Prefix || strings.ContainsAny(s.config.Prefix, "%?#\\") {
		return nil, ErrDocumentation
	}
	for _, text := range []string{s.config.Name, s.config.Header, s.config.Title, s.config.Prefix, s.config.SiteURL, s.config.LogoutURL, s.config.PasswordChangeURL} {
		if !budget.text(text, 4096) {
			return nil, ErrDocumentation
		}
	}
	d.site = &Site{config: Config{
		Name: s.config.Name, Header: s.config.Header, Title: s.config.Title, Prefix: s.config.Prefix,
		SiteURL: s.config.SiteURL, LogoutURL: s.config.LogoutURL, PasswordChangeURL: s.config.PasswordChangeURL,
		Policy: s.config.Policy, ActorLabel: s.config.ActorLabel,
	}, engine: s.engine, cssVersion: s.cssVersion, jsVersion: s.jsVersion}
	d.path = s.config.Prefix + "doc/"
	seen := make(map[string]bool, len(options.Models))
	inspected, selected := 0, 0
	for _, selection := range options.Models {
		if !budget.text(selection.Key, 257) || selection.Key == "" || !budget.text(selection.Description, 4096) || seen[selection.Key] || len(selection.Fields) == 0 || len(selection.Fields) > 256 {
			return nil, ErrDocumentation
		}
		seen[selection.Key] = true
		registered, ok := s.models[selection.Key]
		if !ok || len(registered.Schema.Fields) > 4096 || len(registered.Schema.PrimaryKey) > 256 {
			return nil, ErrDocumentation
		}
		inspected += len(registered.Schema.Fields)
		selected += len(selection.Fields)
		if inspected > 65536 || selected > 8192 || !budget.text(registered.Schema.AppLabel, 128) || !budget.text(registered.Schema.Name, 128) {
			return nil, ErrDocumentation
		}
		model := documentationModel{app: registered.Schema.AppLabel, name: registered.Schema.Name, description: selection.Description, authorize: registered.Authorize}
		declarations := make(map[string]*models.Field, len(registered.Schema.Fields))
		for i := range registered.Schema.Fields {
			field := &registered.Schema.Fields[i]
			if field.Name == "" || !budget.text(field.Name, 128) || declarations[field.Name] != nil {
				return nil, ErrDocumentation
			}
			declarations[field.Name] = field
		}
		for _, primary := range registered.Schema.PrimaryKey {
			if !budget.text(primary, 128) || declarations[primary] == nil {
				return nil, ErrDocumentation
			}
		}
		fields := make(map[string]bool, len(selection.Fields))
		for _, selectedField := range selection.Fields {
			if selectedField.Name == "" || !budget.text(selectedField.Name, 128) || !budget.text(selectedField.Description, 4096) || fields[selectedField.Name] {
				return nil, ErrDocumentation
			}
			fields[selectedField.Name] = true
			field := declarations[selectedField.Name]
			if field == nil || !budget.text(string(field.Kind), 128) {
				return nil, ErrDocumentation
			}
			model.fields = append(model.fields, documentationField{name: field.Name, kind: string(field.Kind), description: selectedField.Description,
				nullable: field.Null, editable: field.IsEditable(), primary: field.PrimaryKey || slices.Contains(registered.Schema.PrimaryKey, field.Name), blank: field.Blank})
		}
		d.models = append(d.models, model)
	}
	csrfConfig := s.config.CSRF
	csrfConfig.TrustedOrigins = slices.Clone(csrfConfig.TrustedOrigins)
	csrf, err := security.CSRF(csrfConfig)
	if err != nil {
		return nil, ErrDocumentation
	}
	d.protected = csrf(http.HandlerFunc(d.serve))
	s.frozen = true
	return d, nil
}

func (d *documentationPage) descriptors(options DocumentationOptions, b *documentationBudget) bool {
	names := make(map[string]bool, len(options.Routes))
	methods := 0
	for _, route := range options.Routes {
		if !b.text(route.Name, 512) || !b.text(route.Pattern, 2048) || route.Pattern == "" || len(route.Methods) > 16384-methods {
			return false
		}
		methods += len(route.Methods)
		for _, method := range route.Methods {
			if method == "" || !b.text(method, 32) {
				return false
			}
		}
		if route.Name != "" {
			names[route.Name] = true
		}
		route.Methods = slices.Clone(route.Methods)
		d.routes = append(d.routes, route)
	}
	seen := make(map[string]bool, len(options.Views))
	for _, view := range options.Views {
		if !b.text(view.Route, 512) || !b.text(view.Title, 256) || !b.text(view.Description, 4096) || !names[view.Route] || seen[view.Route] {
			return false
		}
		seen[view.Route] = true
		d.views = append(d.views, view)
	}
	for i, values := range [][]DocumentationExtension{options.Tags, options.Filters} {
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if value.Name == "" || !b.text(value.Name, 128) || !b.text(value.Description, 4096) || seen[value.Name] {
				return false
			}
			seen[value.Name] = true
		}
		if i == 0 {
			d.tags = slices.Clone(values)
		} else {
			d.filters = slices.Clone(values)
		}
	}
	return true
}

func documentationContext(ctx context.Context) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if ctx == nil {
		return false
	}
	v := reflect.ValueOf(ctx)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		if v.IsNil() {
			return false
		}
	}
	return ctx.Err() == nil
}

func documentationPrincipal(ctx context.Context, original auth.Principal) (auth.Principal, error) {
	if !documentationContext(ctx) {
		return auth.Principal{}, errDocumentationUnavailable
	}
	p := auth.FromContext(ctx)
	if !documentationContext(ctx) {
		return auth.Principal{}, errDocumentationUnavailable
	}
	if !p.Authenticated || !p.Active || !p.Staff || p.ID == "" || original.ID != "" && (p.ID != original.ID || p.AuthVersion != original.AuthVersion) {
		return auth.Principal{}, auth.ErrPermissionDenied
	}
	return p, nil
}

func (d *documentationPage) grant(ctx context.Context, original auth.Principal, model *documentationModel) (err error) {
	defer func() {
		if recover() != nil || !documentationContext(ctx) {
			err = errDocumentationUnavailable
		}
	}()
	p, err := documentationPrincipal(ctx, original)
	if err != nil {
		return err
	}
	resource := auth.Resource{App: "admindocs", Model: "documentation", ID: d.site.config.Name}
	if model != nil {
		resource = auth.Resource{App: model.app, Model: model.name}
	}
	// Each policy receives its own permission slice; token ceilings remain on
	// the copied Principal and are enforced by the Site's constrained policy.
	p.Permissions = slices.Clone(p.Permissions)
	err = d.site.config.Policy.Authorize(ctx, p, "view", resource)
	if !documentationContext(ctx) {
		return errDocumentationUnavailable
	}
	if err != nil || model == nil || model.authorize == nil {
		return err
	}
	p, err = documentationPrincipal(ctx, original)
	if err != nil {
		return err
	}
	return model.authorize(ctx, p, "view", Object{})
}

func (d *documentationPage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writer := &documentationWriter{ResponseWriter: w}
	head := false
	defer func() {
		if value := recover(); value != nil {
			if writer.started || value == http.ErrAbortHandler {
				panic(http.ErrAbortHandler)
			}
			documentationWrite(writer, head, 503, "Documentation is temporarily unavailable", false)
		}
	}()
	if r == nil {
		documentationWrite(writer, false, 400, "Invalid request", false)
		return
	}
	method := r.Method
	head = method == http.MethodHead
	writer.head = head
	if r.URL == nil {
		documentationWrite(writer, head, 400, "Invalid request", false)
		return
	}
	requestedPath := r.URL.Path
	if requestedPath != d.path || r.URL.RawPath != "" {
		documentationWrite(writer, head, 404, "Not found", false)
		return
	}
	if method != http.MethodGet && !head {
		documentationWrite(writer, false, 405, "Method not allowed", false)
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Host) > 1024 || len(r.URL.RawQuery) > 4096 {
		documentationWrite(writer, head, 400, "Invalid request", false)
		return
	}
	cookies := r.Header.Values("Cookie")
	if len(cookies) > 16 {
		documentationWrite(writer, head, 400, "Invalid request", false)
		return
	}
	remaining := 8192
	for _, cookie := range cookies {
		if len(cookie) > remaining {
			documentationWrite(writer, head, 400, "Invalid request", false)
			return
		}
		remaining -= len(cookie)
	}
	// No application callback receives mutable caller request metadata. Only
	// the bounded Cookie header is needed by the Site's builtin CSRF layer.
	frozen := &http.Request{Method: method, URL: &url.URL{Path: requestedPath}, Header: http.Header{"Cookie": slices.Clone(cookies)}, Host: r.Host, Body: http.NoBody, TLS: r.TLS}
	frozen = frozen.WithContext(r.Context())
	if !documentationContext(frozen.Context()) {
		documentationWrite(writer, head, 503, "Documentation is temporarily unavailable", false)
		return
	}
	documentationHeaders(writer.Header())
	d.protected.ServeHTTP(writer, frozen)
}

func (d *documentationPage) serve(w http.ResponseWriter, r *http.Request) {
	head := r.Method == http.MethodHead
	fail := func(err error) {
		status, message := 503, "Documentation is temporarily unavailable"
		if documentationContext(r.Context()) && (err == auth.ErrPermissionDenied || err == auth.ErrUnauthenticated) {
			status, message = 403, "Permission denied"
		}
		documentationWrite(w, head, status, message, false)
	}
	p, err := documentationPrincipal(r.Context(), auth.Principal{})
	if err != nil {
		fail(err)
		return
	}
	if err := d.grant(r.Context(), p, nil); err != nil {
		fail(err)
		return
	}
	visible := make([]int, 0, len(d.models))
	rows, navigation := []any{}, []any{}
	for i := range d.models {
		model := &d.models[i]
		if err := d.grant(r.Context(), p, model); err != nil {
			if err == auth.ErrPermissionDenied || err == auth.ErrUnauthenticated {
				continue
			}
			fail(err)
			return
		}
		visible = append(visible, i)
		fields := make([]any, 0, len(model.fields))
		for _, field := range model.fields {
			fields = append(fields, templates.Context{"name": field.name, "kind": field.kind, "description": field.description, "nullable": field.nullable, "blank": field.blank, "editable": field.editable, "primary": field.primary})
		}
		rows = append(rows, templates.Context{"name": model.app + "." + model.name, "description": model.description, "fields": fields})
		navigation = append(navigation, templates.Context{"label": model.name, "app": model.app, "url": d.site.config.Prefix + url.PathEscape(model.app) + "/" + url.PathEscape(strings.ToLower(model.name)) + "/"})
	}
	actor, err := d.site.actorLabel(r.Context(), p)
	if err != nil {
		fail(err)
		return
	}
	routes := make([]any, 0, len(d.routes))
	for _, route := range d.routes {
		methods := strings.Join(route.Methods, ", ")
		if len(route.Methods) == 0 {
			methods = "Any method"
		}
		routes = append(routes, templates.Context{"name": route.Name, "pattern": route.Pattern, "methods": methods})
	}
	views := make([]any, 0, len(d.views))
	for _, view := range d.views {
		views = append(views, templates.Context{"route": view.Route, "title": view.Title, "description": view.Description})
	}
	extensions := func(values []DocumentationExtension) []any {
		result := make([]any, 0, len(values))
		for _, value := range values {
			result = append(result, templates.Context{"name": value.Name, "description": value.Description})
		}
		return result
	}
	body, err := d.site.engine.Render(r.Context(), "documentation.html", templates.Context{
		"title": "Developer reference", "header": d.site.config.Header, "site_title": d.site.config.Title,
		"prefix": d.site.config.Prefix, "css_url": d.site.config.Prefix + "assets/admin." + d.site.cssVersion + ".css",
		"js_url": d.site.config.Prefix + "assets/admin." + d.site.jsVersion + ".js", "actor": actor,
		"site_url": d.site.config.SiteURL, "logout_url": d.site.config.LogoutURL, "password_change_url": d.site.config.PasswordChangeURL,
		"csrf_token": security.CSRFToken(r), "navigation": navigation, "models": rows, "routes": routes, "views": views,
		"tags": extensions(d.tags), "filters": extensions(d.filters),
	})
	if err != nil || !documentationContext(r.Context()) {
		fail(errDocumentationUnavailable)
		return
	}
	for _, i := range visible {
		if err := d.grant(r.Context(), p, &d.models[i]); err != nil {
			fail(err)
			return
		}
	}
	if err := d.grant(r.Context(), p, nil); err != nil {
		fail(err)
		return
	}
	documentationWrite(w, head, 200, body, true)
}

type documentationWriter struct {
	http.ResponseWriter
	started bool
	head    bool
}

func (w *documentationWriter) WriteHeader(status int) {
	w.started = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *documentationWriter) Write(body []byte) (int, error) {
	if w.head {
		return len(body), nil
	}
	w.started = true
	n, err := w.ResponseWriter.Write(body)
	if err != nil || n != len(body) {
		panic(http.ErrAbortHandler)
	}
	return n, nil
}

func documentationWrite(w http.ResponseWriter, head bool, status int, body string, html bool) {
	defer func() {
		if recover() != nil {
			panic(http.ErrAbortHandler)
		}
	}()
	h := w.Header()
	documentationHeaders(h)
	h.Set("Content-Type", "text/plain; charset=utf-8")
	if html {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	if status == 405 {
		h.Set("Allow", "GET, HEAD")
	}
	w.WriteHeader(status)
	if !head {
		if n, err := w.Write([]byte(body)); err != nil || n != len(body) {
			panic(http.ErrAbortHandler)
		}
	}
}

func documentationHeaders(h http.Header) {
	h.Set("Cache-Control", "private, no-store")
	h.Set("Vary", "Cookie")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}
