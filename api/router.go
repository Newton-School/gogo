package api

import (
	"context"
	"fmt"
	nethttp "net/http"
	"sort"
	"strings"

	frameworkhttp "github.com/Newton-School/gogo/http"
)

// RouterOption configures an API router.
type RouterOption func(*Router)

// Router stores generated API routes and resolves requests to API views.
type Router struct {
	prefix           string
	trailingSlash    bool
	exceptionHandler ExceptionHandler
	routes           []Route
	byName           map[string]struct{}
}

// Route stores generated API route metadata.
type Route struct {
	Name        string
	Pattern     string
	Methods     []string
	Action      string
	Detail      bool
	View        View
	HTTPHandler nethttp.Handler
	Metadata    OperationMetadata

	compiled frameworkhttp.Pattern
}

// NewRouter creates an API router.
func NewRouter(options ...RouterOption) *Router {
	router := &Router{
		trailingSlash: true,
		byName:        map[string]struct{}{},
	}
	for _, option := range options {
		option(router)
	}
	return router
}

// WithAPIPrefix configures a path prefix for routes registered on the router.
func WithAPIPrefix(prefix string) RouterOption {
	return func(router *Router) {
		router.prefix = prefix
	}
}

// WithTrailingSlash enables or disables generated trailing slashes.
func WithTrailingSlash(enabled bool) RouterOption {
	return func(router *Router) {
		router.trailingSlash = enabled
	}
}

// WithExceptionHandler configures router-level exception handling for route
// misses, method errors, write failures, and uncaught API view panics.
func WithExceptionHandler(handler ExceptionHandler) RouterOption {
	return func(router *Router) {
		router.exceptionHandler = handler
	}
}

// Register registers all standard routes and custom actions for a viewset.
func (r *Router) Register(prefix, basename string, viewset *ModelViewSet) error {
	if viewset == nil {
		return fmt.Errorf("%w: nil viewset", ErrRouteConflict)
	}
	base := joinRoutePath(r.prefix, prefix)
	lookup := viewset.LookupParam
	if lookup == "" {
		lookup = "id"
	}
	standardRoutes := []struct {
		nameAction string
		action     string
		pattern    string
		methods    []string
		detail     bool
	}{
		{nameAction: "list", action: "list", pattern: r.withSlash(base), methods: []string{nethttp.MethodGet}},
		{nameAction: "create", action: "create", pattern: r.withSlash(base), methods: []string{nethttp.MethodPost}},
		{nameAction: "detail", action: "retrieve", pattern: r.withSlash(joinRoutePath(base, "<str:"+lookup+">")), methods: []string{nethttp.MethodGet}, detail: true},
		{nameAction: "update", action: "update", pattern: r.withSlash(joinRoutePath(base, "<str:"+lookup+">")), methods: []string{nethttp.MethodPut}, detail: true},
		{nameAction: "partial_update", action: "partial_update", pattern: r.withSlash(joinRoutePath(base, "<str:"+lookup+">")), methods: []string{nethttp.MethodPatch}, detail: true},
		{nameAction: "destroy", action: "destroy", pattern: r.withSlash(joinRoutePath(base, "<str:"+lookup+">")), methods: []string{nethttp.MethodDelete}, detail: true},
	}
	for _, route := range standardRoutes {
		if err := r.addRoute(Route{
			Name:    routeName(basename, route.nameAction),
			Pattern: route.pattern,
			Action:  route.action,
			Detail:  route.detail,
			View:    viewset.AsView(route.action),
		}, route.methods...); err != nil {
			return err
		}
	}

	for _, actionName := range sortedActionNames(viewset.Actions) {
		action := viewset.Actions[actionName]
		actionPath := strings.ReplaceAll(actionName, "_", "-")
		pattern := r.withSlash(joinRoutePath(base, actionPath))
		if action.Detail {
			pattern = r.withSlash(joinRoutePath(base, "<str:"+lookup+">", actionPath))
		}
		methods := action.Methods
		if len(methods) == 0 {
			methods = []string{nethttp.MethodGet}
		}
		if err := r.addRoute(Route{
			Name:    routeName(basename, actionName),
			Pattern: pattern,
			Action:  actionName,
			Detail:  action.Detail,
			View:    viewset.AsView(actionName),
		}, methods...); err != nil {
			return err
		}
	}
	return nil
}

// Handle registers one custom API route.
func (r *Router) Handle(name, pattern string, view View, methods ...string) error {
	return r.addRoute(Route{
		Name:    name,
		Pattern: r.withSlash(joinRoutePath(r.prefix, pattern)),
		View:    view,
	}, methods...)
}

// HandleHTTP registers one raw standard-library API route with documentation metadata.
func (r *Router) HandleHTTP(name, pattern string, handler nethttp.Handler, metadata OperationMetadata, methods ...string) error {
	if handler == nil {
		return fmt.Errorf("%w: nil http handler", ErrRouteConflict)
	}
	return r.addRoute(Route{
		Name:        name,
		Pattern:     r.withSlash(joinRoutePath(r.prefix, pattern)),
		HTTPHandler: handler,
		Metadata:    metadata,
	}, methods...)
}

// Include includes another API router under a nested prefix.
func (r *Router) Include(prefix string, subrouter *Router) error {
	if subrouter == nil {
		return nil
	}
	for _, route := range subrouter.Routes() {
		pattern := joinRoutePath(r.prefix, prefix, route.Pattern)
		route.Pattern = pattern
		if err := r.addRoute(route, route.Methods...); err != nil {
			return err
		}
	}
	return nil
}

// Routes returns a copy of registered route metadata.
func (r *Router) Routes() []Route {
	routes := make([]Route, len(r.routes))
	copy(routes, r.routes)
	for index := range routes {
		routes[index].Methods = append([]string(nil), r.routes[index].Methods...)
		routes[index].Metadata = cloneOperationMetadata(r.routes[index].Metadata)
	}
	return routes
}

// Resolve matches one request and runs the route view.
func (r *Router) Resolve(ctx context.Context, request *Request) (response Response) {
	if request == nil || request.Raw() == nil {
		return r.exception(ctx, request, ErrInternal)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			response = r.exception(ctx, request, fmt.Errorf("%w: %v", ErrInternal, recovered))
		}
	}()

	route, params, allowed := r.match(request.Raw())
	if route != nil {
		attachAPIPathParams(request, params)
		if route.HTTPHandler != nil || route.View == nil {
			return r.exception(ctx, request, ErrMethodNotAllowed)
		}
		return route.View(ctx, request)
	}
	if len(allowed) > 0 {
		return r.exception(ctx, request, ErrMethodNotAllowed)
	}
	return r.exception(ctx, request, ErrNotFound)
}

// ServeHTTP serves API routes directly as a standard HTTP handler.
func (r *Router) ServeHTTP(w nethttp.ResponseWriter, raw *nethttp.Request) {
	request := NewRequest(raw)
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = r.exception(raw.Context(), request, fmt.Errorf("%w: %v", ErrInternal, recovered)).Write(w)
		}
	}()

	route, params, allowed := r.match(raw)
	if route == nil {
		err := ErrNotFound
		if len(allowed) > 0 {
			err = ErrMethodNotAllowed
		}
		_ = r.exception(raw.Context(), request, err).Write(w)
		return
	}
	attachAPIPathParams(request, params)
	if route.HTTPHandler != nil {
		route.HTTPHandler.ServeHTTP(w, raw)
		return
	}
	response := route.View(raw.Context(), request)
	if err := response.Write(w); err != nil {
		_ = r.exception(raw.Context(), request, ErrInternal).Write(w)
	}
}

// MountHTTP registers API routes on a framework HTTP router.
func (r *Router) MountHTTP(router *frameworkhttp.Router) error {
	if router == nil {
		return fmt.Errorf("%w: nil http router", ErrRouteConflict)
	}
	for _, route := range r.Routes() {
		var err error
		if route.HTTPHandler != nil {
			err = router.HandleHTTP(route.Name, route.Pattern, route.HTTPHandler, route.Methods...)
		} else {
			err = router.Handle(route.Name, route.Pattern, func(ctx context.Context, request *frameworkhttp.Request) frameworkhttp.Response {
				return r.Resolve(ctx, NewRequest(request.Raw())).HTTP()
			}, route.Methods...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Reverse resolves a route name into a URL path.
func (r *Router) Reverse(name string, args map[string]any) (string, error) {
	proxy := frameworkhttp.NewRouter()
	for _, route := range r.routes {
		err := proxy.Handle(route.Name, route.Pattern, func(context.Context, *frameworkhttp.Request) frameworkhttp.Response {
			return frameworkhttp.Text(nethttp.StatusNoContent, "")
		}, route.Methods...)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrReverse, err)
		}
	}
	value, err := proxy.Reverse(name, args)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrReverse, err)
	}
	return value, nil
}

func (r *Router) addRoute(route Route, methods ...string) error {
	if route.View == nil && route.HTTPHandler == nil {
		return fmt.Errorf("%w: route handler is required", ErrRouteConflict)
	}
	if _, exists := r.byName[route.Name]; exists {
		return fmt.Errorf("%w: route name %q", ErrRouteConflict, route.Name)
	}
	normalizedMethods := normalizeAPIMethods(methods)
	for _, existing := range r.routes {
		if existing.Pattern != route.Pattern {
			continue
		}
		for _, method := range normalizedMethods {
			if stringIn(existing.Methods, method) {
				return fmt.Errorf("%w: %s %s", ErrRouteConflict, method, route.Pattern)
			}
		}
	}
	compiled, err := frameworkhttp.CompilePattern(route.Pattern)
	if err != nil {
		return err
	}
	route.Methods = normalizedMethods
	route.Metadata = cloneOperationMetadata(route.Metadata)
	route.compiled = compiled
	r.byName[route.Name] = struct{}{}
	r.routes = append(r.routes, route)
	return nil
}

func (r *Router) match(raw *nethttp.Request) (*Route, map[string]string, []string) {
	var allowed []string
	for index := range r.routes {
		route := &r.routes[index]
		params, ok := route.compiled.Match(raw.URL.Path)
		if !ok {
			continue
		}
		if !stringIn(route.Methods, raw.Method) {
			allowed = append(allowed, route.Methods...)
			continue
		}
		return route, params, nil
	}
	return nil, nil, allowed
}

func (r *Router) exception(ctx context.Context, request *Request, err error) Response {
	if r.exceptionHandler != nil {
		return r.exceptionHandler(ctx, request, err)
	}
	return DefaultExceptionHandler(ctx, request, err)
}

func attachAPIPathParams(request *Request, params map[string]string) {
	for name, value := range params {
		request.WithPathParam(name, value)
		if request.Raw() != nil {
			request.Raw().SetPathValue(name, value)
		}
	}
}

func (r *Router) withSlash(pattern string) string {
	if pattern == "" {
		pattern = "/"
	}
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/" + pattern
	}
	pattern = strings.TrimRight(pattern, "/")
	if pattern == "" {
		pattern = "/"
	}
	if r.trailingSlash && pattern != "/" {
		return pattern + "/"
	}
	return pattern
}

func joinRoutePath(parts ...string) string {
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}

func routeName(basename, action string) string {
	return strings.Trim(basename, "-") + "-" + strings.ReplaceAll(action, "_", "-")
}

func sortedActionNames(actions map[string]ViewSetAction) []string {
	names := make([]string, 0, len(actions))
	for name := range actions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeAPIMethods(methods []string) []string {
	if len(methods) == 0 {
		return []string{nethttp.MethodGet}
	}
	normalized := make([]string, 0, len(methods))
	for _, method := range methods {
		normalized = append(normalized, strings.ToUpper(method))
	}
	sort.Strings(normalized)
	return normalized
}

func stringIn(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
