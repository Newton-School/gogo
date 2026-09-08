package urls

import (
	"errors"
	"net/http"
	"reflect"
)

// WithNotFound returns a new router with an explicit final route-miss handler.
// It does not intercept a matched view's 404, method denials, or automatic
// OPTIONS replies. Routing and converter decoding still happen only once.
// The fallback receives empty route-match metadata; other context is preserved.
// Use this boundary for site-bound redirects or other explicit fallbacks.
// The original router is unchanged; handler internals remain caller-owned.
func (r *Router) WithNotFound(handler http.Handler) (*Router, error) {
	if r == nil || handler == nil {
		return nil, errors.New("URL router and not-found handler required")
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, errors.New("URL not-found handler must not be nil")
		}
	}
	copy := *r
	copy.notFound = handler
	return &copy, nil
}
