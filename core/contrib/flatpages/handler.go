package flatpages

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/sites"
)

type pageHandler struct {
	current func(*http.Request) (sites.Info, error)
	lookup  func(context.Context, LookupInput) (Info, bool, error)
	render  func(context.Context, Info, sites.Info) (string, error)
	options HandlerOptions
}

// NewHandler serves an exact site-bound path through an explicit route, or as
// Router.WithNotFound's final fallback. Query strings do not select pages. No
// response interception, URL rewriting, mutation, or slash retry is performed.
// Configure Sites and Store for the same logical site database and put verified
// authentication/host/security middleware outside the handler.
func NewHandler(store *Store, options HandlerOptions) (http.Handler, error) {
	if store == nil || store.state == nil || options.Sites == nil || options.Authorize == nil {
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
	owned, selector := *store, *options.Sites
	renderer, err := newPageRenderer(options.Render)
	if err != nil {
		return nil, err
	}
	options.Sites = nil
	options.Render = RenderOptions{}
	return &pageHandler{current: selector.Current, lookup: owned.Lookup, render: renderer.render, options: options}, nil
}

type pageResponse struct {
	status   int
	body     string
	fallback bool
}

func (h *pageHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request == nil {
		writePageResponse(w, pageResponse{status: http.StatusBadRequest}, false)
		return
	}
	owned := *request
	if request.URL != nil {
		u := *request.URL
		owned.URL = &u
	}
	owned.Header = request.Header.Clone()
	owned.TransferEncoding = append([]string(nil), request.TransferEncoding...)
	state := *h
	head := owned.Method == http.MethodHead
	response := state.response(&owned)
	if response.fallback {
		if head {
			w = pageHeadWriter{w}
		}
		if state.options.NotFound != nil {
			state.options.NotFound.ServeHTTP(w, &owned)
		} else {
			http.NotFound(w, &owned)
		}
		return
	}
	writePageResponse(w, response, head)
}

type pageHeadWriter struct{ http.ResponseWriter }

func (w pageHeadWriter) Write(data []byte) (int, error) { return len(data), nil }

func writePageResponse(w http.ResponseWriter, response pageResponse, head bool) {
	w.Header().Del("Location")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(response.body)))
	if response.status == http.StatusOK {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if response.status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "GET, HEAD")
	}
	w.WriteHeader(response.status)
	if !head && response.body != "" {
		if n, err := w.Write([]byte(response.body)); err != nil || n != len(response.body) {
			panic(http.ErrAbortHandler)
		}
	}
}

func (h *pageHandler) response(request *http.Request) (response pageResponse) {
	response.status = http.StatusServiceUnavailable
	defer func() {
		if recover() != nil {
			response = pageResponse{status: http.StatusServiceUnavailable}
		}
	}()
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return pageResponse{status: http.StatusMethodNotAllowed}
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 || request.Body != nil && request.Body != http.NoBody || request.Header.Get("Content-Encoding") != "" {
		return pageResponse{status: http.StatusBadRequest}
	}
	key, err := pageRequestPath(request)
	if err != nil {
		return pageResponse{status: http.StatusBadRequest}
	}
	if contextError(request.Context()) != nil {
		return response
	}
	// Principal is verified by outer middleware, not parsed from request input.
	// A detached identity snapshot cannot be changed by rendering callbacks.
	principal := auth.FromContext(request.Context())
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
			return pageResponse{status: http.StatusBadRequest}
		case sites.ErrSiteNotConfigured:
			return pageResponse{fallback: true}
		default:
			return response
		}
	}
	page, found, err := h.lookup(ctx, LookupInput{SiteID: info.ID, URL: key})
	if contextError(ctx) != nil {
		return response
	}
	if err != nil {
		if err == ErrNotFound || err == ErrSiteNotConfigured {
			return pageResponse{fallback: true}
		}
		return response
	}
	if !found {
		return pageResponse{fallback: true}
	}
	if page.RegistrationRequired && (!principal.Authenticated || !principal.Active || principal.ID == "") {
		return pageResponse{status: http.StatusNotFound}
	}
	if err := h.authorize(ctx, info, page); err != nil {
		return pagePolicyResponse(err)
	}
	body, err := h.render(ctx, page, info)
	if contextError(ctx) != nil {
		return response
	}
	if err != nil {
		return pagePolicyResponse(err)
	}
	if err := h.authorize(ctx, info, page); err != nil {
		return pagePolicyResponse(err)
	}
	if contextError(ctx) != nil {
		return response
	}
	return pageResponse{status: http.StatusOK, body: body}
}

func (h *pageHandler) authorize(ctx context.Context, site sites.Info, page Info) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	err := h.options.Authorize(ctx, site, page)
	if canceled := contextError(ctx); canceled != nil {
		return canceled
	}
	return err
}
func pagePolicyResponse(err error) pageResponse {
	if err == ErrForbidden {
		return pageResponse{status: http.StatusNotFound}
	}
	return pageResponse{status: http.StatusServiceUnavailable}
}

func pageRequestPath(request *http.Request) (string, error) {
	u := request.URL
	if u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.OmitHost {
		return "", ErrInvalid
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalid
	}
	if u.Host != "" && !strings.EqualFold(u.Host, request.Host) {
		return "", ErrInvalid
	}
	// Reject oversized inputs before decoding or constructing escaped copies.
	if len(u.Path) > MaxURLBytes || len(u.RawPath) > MaxURLBytes {
		return "", ErrInvalid
	}
	if u.RawPath != "" {
		decoded, err := url.PathUnescape(u.RawPath)
		if err != nil || decoded != u.Path || u.EscapedPath() != u.RawPath {
			return "", ErrInvalid
		}
	}
	key := u.EscapedPath()
	if key == "" {
		key = "/"
	}
	return normalizePath(key)
}
