package http

import (
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
)

// RedirectViewOptions configures a read-only redirect. URL is literal, without
// template or percent interpolation. Use PatternName with Router.Reverse, or
// Target, for a destination derived from route parameters or authorized data.
// At most one of URL, PatternName and Target may be set. No source, or an empty
// successful Target result, returns Gone after the ordinary access checks.
type RedirectViewOptions struct {
	ReadViewOptions
	URL         string
	PatternName string
	Reverse     func(string, map[string]any, url.Values) (string, error)
	Target      func(*http.Request) (string, error)
	Permanent   bool
	// QueryString appends the original nonempty raw query to the target query,
	// before its fragment. Existing and incoming member order is preserved.
	QueryString bool
	// AuthorizeExternal is mandatory for every absolute HTTP(S) destination,
	// including a destination with the same host as the request. It receives
	// the final validated Location twice. Host headers never confer authority.
	// All hooks are trusted read-only, cooperative application callbacks.
	AuthorizeExternal func(*http.Request, string) error
}

// NewRedirectView constructs a GET/HEAD handler with a mandatory access policy.
// Permanent selects 301 instead of the default 302. Targets are bounded to 2048
// raw and encoded URI bytes, root-relative or absolute HTTP(S), and never fetched.
// Constructor options are copied; each hook receives fresh request metadata.
func NewRedirectView(options RedirectViewOptions) (http.Handler, error) {
	selected := 0
	if options.URL != "" {
		selected++
	}
	if options.PatternName != "" {
		selected++
	}
	if options.Target != nil {
		selected++
	}
	if selected > 1 || (options.PatternName == "") != (options.Reverse == nil) {
		return nil, ErrGenericConfiguration
	}
	if options.PatternName != "" && !genericRedirectPattern(options.PatternName) {
		return nil, ErrGenericConfiguration
	}
	if options.URL != "" {
		target, absolute, err := genericRedirectTarget(options.URL, "")
		if err != nil || absolute && options.AuthorizeExternal == nil {
			return nil, ErrGenericConfiguration
		}
		options.URL = target
	}
	// The closure owns this value; changing the caller's options cannot change
	// the target source, status, query forwarding or authorization in flight.
	return newReadView(options.ReadViewOptions, func(call *readViewCall) (readViewResult, error) {
		target := options.URL
		var err error
		if options.Target != nil {
			target, err = options.Target(call.request())
		} else if options.PatternName != "" {
			target, err = options.Reverse(options.PatternName, call.params(), nil)
			if err == nil && target == "" {
				// A named route never reverses to no route. Do not turn a
				// malformed provider result into an intentional Gone page.
				err = ErrUnavailable
			}
		}
		if err != nil {
			return readViewResult{}, err
		}
		if call.request().Context().Err() != nil {
			return readViewResult{}, ErrUnavailable
		}
		if target == "" {
			return readViewResult{response: Response{Status: http.StatusGone}}, nil
		}
		query := ""
		if options.QueryString {
			query = call.rawQuery()
		}
		location, absolute, err := genericRedirectTarget(target, query)
		if err != nil {
			return readViewResult{}, ErrUnavailable
		}
		var finalize func(*readViewCall) error
		if absolute {
			if call.request().Context().Err() != nil {
				return readViewResult{}, ErrUnavailable
			}
			if options.AuthorizeExternal == nil {
				return readViewResult{}, auth.ErrPermissionDenied
			}
			if err := options.AuthorizeExternal(call.request(), location); err != nil {
				return readViewResult{}, err
			}
			finalize = func(current *readViewCall) error {
				return options.AuthorizeExternal(current.request(), location)
			}
		}
		status := http.StatusFound
		if options.Permanent {
			status = http.StatusMovedPermanently
		}
		return readViewResult{
			response: Response{Status: status, Headers: http.Header{"Location": {location}}},
			finalize: finalize,
		}, nil
	})
}

func genericRedirectPattern(value string) bool {
	if len(value) > 255 || !utf8.ValidString(value) {
		return false
	}
	for _, segment := range strings.Split(value, ":") {
		if segment == "" {
			return false
		}
		for _, c := range segment {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}
