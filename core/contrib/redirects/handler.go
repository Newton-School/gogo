package redirects

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/security"
)

type redirectHandler struct {
	current func(*http.Request) (sites.Info, error)
	lookup  func(context.Context, LookupInput) (Match, bool, error)
	options HandlerOptions
}

// NewHandler constructs an optional final route-miss handler. Attach it with
// urls.Router.WithNotFound behind the project's host/security middleware. It
// does not intercept an application view's 404, re-run routing, follow remote
// URLs, read request bodies, or redirect methods other than GET and HEAD.
func NewHandler(resolver *Resolver, options HandlerOptions) (http.Handler, error) {
	if resolver == nil || resolver.state == nil || options.Sites == nil || options.Authorize == nil {
		return nil, ErrConfiguration
	}
	if options.NotFound != nil && nilValue(options.NotFound) {
		return nil, ErrConfiguration
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if options.Timeout < time.Millisecond || options.Timeout > time.Minute {
		return nil, ErrConfiguration
	}
	// Capture receiver values, not method values bound to caller-owned pointers.
	// Replacing either original resolver later must not retarget this handler.
	owned, selector := *resolver, *options.Sites
	options.Sites = nil
	return &redirectHandler{current: selector.Current, lookup: owned.Lookup, options: options}, nil
}

type redirectResponse struct {
	status   int
	location string
	fallback bool
}

func (h *redirectHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request == nil {
		writeRedirectResponse(w, redirectResponse{status: http.StatusBadRequest})
		return
	}
	// Snapshot transport input before any application-supplied context, site
	// provider or authorization callback. Bodies are never read or copied.
	owned := *request
	if request.URL != nil {
		u := *request.URL
		owned.URL = &u
	}
	owned.Header = request.Header.Clone()
	owned.TransferEncoding = append([]string(nil), request.TransferEncoding...)
	state := *h
	if owned.Method == http.MethodHead {
		w = redirectHeadWriter{w}
	}
	response := state.response(&owned)
	if response.fallback {
		if state.options.NotFound != nil {
			state.options.NotFound.ServeHTTP(w, &owned)
		} else {
			http.NotFound(w, &owned)
		}
		return
	}
	writeRedirectResponse(w, response)
}

type redirectHeadWriter struct{ http.ResponseWriter }

func (w redirectHeadWriter) Write(data []byte) (int, error) { return len(data), nil }

func writeRedirectResponse(w http.ResponseWriter, response redirectResponse) {
	w.Header().Del("Location")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", "0")
	if response.location != "" {
		w.Header().Set("Location", response.location)
	}
	w.WriteHeader(response.status)
}

func (h *redirectHandler) response(request *http.Request) (response redirectResponse) {
	response.status = http.StatusServiceUnavailable
	defer func() {
		if recover() != nil {
			response = redirectResponse{status: http.StatusServiceUnavailable}
		}
	}()
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return redirectResponse{fallback: true}
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 || request.Body != nil && request.Body != http.NoBody || request.Header.Get("Content-Encoding") != "" {
		return redirectResponse{status: http.StatusBadRequest}
	}
	if contextError(request.Context()) != nil {
		return response
	}
	uri, origin, err := redirectRequestURI(request)
	if err != nil {
		return redirectResponse{status: http.StatusBadRequest}
	}
	ctx, cancel := context.WithTimeout(request.Context(), h.options.Timeout)
	defer cancel()
	if contextError(ctx) != nil {
		return response
	}
	info, err := h.current(request.WithContext(ctx))
	if contextError(ctx) != nil {
		return response
	}
	if err != nil {
		switch err {
		case sites.ErrInvalidHost:
			return redirectResponse{status: http.StatusBadRequest}
		case sites.ErrSiteNotConfigured:
			return redirectResponse{fallback: true}
		default:
			return response
		}
	}
	if err = h.authorize(ctx, info); err != nil {
		return policyResponse(err)
	}
	match, found, err := h.lookup(ctx, LookupInput{SiteID: info.ID, URI: uri, Origin: origin})
	if contextError(ctx) != nil {
		return response
	}
	if err != nil {
		if err == ErrSiteNotConfigured {
			return redirectResponse{fallback: true}
		}
		return response
	}
	if !found {
		return redirectResponse{fallback: true}
	}
	if match.External {
		if h.options.AllowExternal == nil {
			return policyResponse(ErrForbidden)
		}
		err = h.options.AllowExternal(ctx, info, match.Location)
		if contextError(ctx) != nil {
			return response
		}
		if err != nil {
			return policyResponse(err)
		}
	}
	if err = h.authorize(ctx, info); err != nil {
		return policyResponse(err)
	}
	if contextError(ctx) != nil {
		return response
	}
	if match.Location == "" {
		return redirectResponse{status: http.StatusGone}
	}
	status := http.StatusFound
	if match.Permanent {
		status = http.StatusMovedPermanently
	}
	return redirectResponse{status: status, location: match.Location}
}

func (h *redirectHandler) authorize(ctx context.Context, info sites.Info) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	err := h.options.Authorize(ctx, info)
	if canceled := contextError(ctx); canceled != nil {
		return canceled
	}
	return err
}

func policyResponse(err error) redirectResponse {
	if err == ErrForbidden {
		return redirectResponse{status: http.StatusForbidden}
	}
	return redirectResponse{status: http.StatusServiceUnavailable}
}

func redirectRequestURI(request *http.Request) (string, string, error) {
	u := request.URL
	if u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.OmitHost {
		return "", "", ErrInvalid
	}
	if u.RawPath != "" {
		decoded, err := url.PathUnescape(u.RawPath)
		if err != nil || decoded != u.Path || u.EscapedPath() != u.RawPath {
			return "", "", ErrInvalid
		}
	}
	key := u.EscapedPath()
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	key, err := parseOldPath(key)
	if err != nil {
		return "", "", err
	}
	// IsSecure consumes only Core's established trusted-proxy context or TLS.
	// Raw Forwarded and X-Forwarded-* request headers are never consulted here.
	scheme := "http"
	if security.IsSecure(request) {
		scheme = "https"
	}
	origin, err := normalizeOrigin(scheme + "://" + request.Host)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "" && u.Scheme != scheme {
		return "", "", ErrInvalid
	}
	if u.Host != "" {
		absoluteOrigin, err := normalizeOrigin(scheme + "://" + u.Host)
		if err != nil || absoluteOrigin != origin {
			return "", "", ErrInvalid
		}
	}
	return key, origin, nil
}
