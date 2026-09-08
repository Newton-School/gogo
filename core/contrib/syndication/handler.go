package syndication

import (
	"context"
	"net/http"
	"strconv"
	"time"

	ghttp "github.com/Newton-School/gogo/core/http"
)

// Source must query published public items in a stable order, within limit.
// The handler requests MaxItems+1 so a source can report overflow without
// silently publishing a truncated feed. It must propagate provider errors.
type Source func(context.Context, int) (Feed, error)

type HandlerOptions struct {
	// Authorize checks access to the feed metadata before invoking Source.
	// Item/enclosure publication authority is separately checked by Renderer.
	Authorize func(context.Context) error
	Timeout   time.Duration
	// PublicCache is opt-in and requires representation data to be public and
	// independent of cookies/authorization. Revalidation is always mandatory.
	PublicCache bool
}

// Handler snapshots renderer/options at registration. Mount it on a named
// application route behind host/security middleware. GET and HEAD re-run all
// policies, even for matching validators; a 304 never bypasses authorization.
func (r *Renderer) Handler(source Source, options HandlerOptions) (http.Handler, error) {
	if r == nil || r.origin == nil || source == nil || options.Authorize == nil {
		return nil, ErrInvalidFeed
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.Timeout < time.Millisecond || options.Timeout > 5*time.Minute {
		return nil, ErrInvalidFeed
	}
	operation := *r
	cacheControl := "private, no-cache"
	if options.PublicCache {
		cacheControl = "public, max-age=0, must-revalidate"
	}
	return ghttp.Adapt(func(request *http.Request) (response ghttp.Response, err error) {
		defer func() {
			if recover() != nil {
				response = ghttp.Response{}
				err = feedHTTPError(ErrUnavailable)
			}
			if err != nil {
				// Keep all failures on the same no-store response path,
				// including callback panics and provider errors.
				public := ghttp.PublicError(err)
				response, err = ghttp.JSON(public.Status, struct {
					Code      string `json:"code"`
					Detail    string `json:"detail"`
					RequestID string `json:"request_id,omitempty"`
				}{public.Code, public.Message, ghttp.RequestID(request)})
				response.Headers.Set("Cache-Control", "no-store")
			}
			if response.Headers == nil {
				response.Headers = http.Header{}
			}
			response.Headers.Set("X-Content-Type-Options", "nosniff")
		}()
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			return ghttp.Response{Status: http.StatusMethodNotAllowed, Headers: http.Header{"Allow": {"GET, HEAD"}, "Cache-Control": {"no-store"}}}, nil
		}
		if request.URL == nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.ContentLength != 0 || len(request.TransferEncoding) != 0 || request.Body != nil && request.Body != http.NoBody || request.Header.Get("Content-Encoding") != "" {
			return ghttp.Response{Status: http.StatusBadRequest, Headers: http.Header{"Cache-Control": {"no-store"}}}, nil
		}
		ctx, cancel := context.WithTimeout(request.Context(), options.Timeout)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return ghttp.Response{}, feedHTTPError(err)
		}
		if err := options.Authorize(ctx); err != nil {
			return ghttp.Response{}, feedHTTPError(policyError(ctx, err))
		}
		if err := ctx.Err(); err != nil {
			return ghttp.Response{}, feedHTTPError(err)
		}
		feed, err := source(ctx, operation.maxItems+1)
		if ctx.Err() != nil {
			return ghttp.Response{}, feedHTTPError(ctx.Err())
		}
		if err != nil {
			return ghttp.Response{}, feedHTTPError(policyError(ctx, err))
		}
		document, err := operation.Render(ctx, feed)
		if err != nil {
			return ghttp.Response{}, feedHTTPError(err)
		}
		// LastModified is informational in Document, not an HTTP validator:
		// max(item timestamps) cannot prove that removals/visibility changes
		// leave a cached representation unchanged. The exact-body ETag can.
		return ghttp.Response{Status: http.StatusOK, Headers: http.Header{
			"Content-Type": {document.ContentType}, "Content-Length": {strconv.Itoa(len(document.Body))}, "Cache-Control": {cacheControl}, "X-Content-Type-Options": {"nosniff"},
		}, Body: document.Body, ETag: document.ETag}, nil
	}), nil
}

func feedHTTPError(err error) error {
	if err == ErrNotPublic {
		return &ghttp.Error{Status: http.StatusForbidden, Code: "FEED_NOT_PUBLIC", Message: "Feed unavailable"}
	}
	return &ghttp.Error{Status: http.StatusServiceUnavailable, Code: "FEED_UNAVAILABLE", Message: "Feed unavailable"}
}
