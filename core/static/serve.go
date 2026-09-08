package static

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path"
	"strings"

	ghttp "github.com/Newton-School/gogo/core/http"
)

// DevHandler serves unhashed public source files only when debug is explicitly
// true at construction. Mount it deliberately; collectstatic never modifies a
// router or starts a listener. Production hashed files belong to the configured
// web server/CDN. There are no directory listings, upload roots or cookies.
func (c *Collector) DevHandler(debug bool) (http.Handler, error) {
	if !debug {
		return nil, ErrNotDebug
	}
	if c == nil || len(c.config.Sources) == 0 {
		return nil, ErrInvalid
	}
	owned := *c
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || r.URL == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Capture the exact request selection and conditional metadata before
		// any context or filesystem-provider callback can mutate the caller.
		method, requestPath := r.Method, r.URL.Path
		etags, validCondition := staticConditionalLines(r.Header.Values("If-None-Match"))
		modified := r.Header.Get("If-Modified-Since")
		ctx := r.Context()
		request := (&http.Request{Method: method, Header: http.Header{"If-None-Match": etags, "If-Modified-Since": {modified}}}).WithContext(ctx)
		response := func() ghttp.Response {
			if len(requestPath) > maxPathBytes+len(owned.prefix.path) || !validCondition || len(modified) > 128 {
				return staticText(http.StatusBadRequest, "Invalid static request")
			}
			if method != http.MethodGet && method != http.MethodHead {
				result := staticText(http.StatusMethodNotAllowed, "Method not allowed")
				result.Headers.Set("Allow", "GET, HEAD")
				return result
			}
			if !strings.HasPrefix(requestPath, owned.prefix.path) {
				return staticText(http.StatusNotFound, "Not found")
			}
			name := strings.TrimPrefix(requestPath, owned.prefix.path)
			if !validPath(name) || ignoredPath(name) {
				return staticText(http.StatusNotFound, "Not found")
			}
			data, err := owned.developmentAsset(ctx, name)
			if errors.Is(err, ErrNotFound) {
				return staticText(http.StatusNotFound, "Not found")
			}
			if err != nil {
				return staticText(http.StatusServiceUnavailable, "Static asset unavailable")
			}
			contentType := mime.TypeByExtension(path.Ext(name))
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			return ghttp.Response{Status: http.StatusOK, Headers: http.Header{"Content-Type": {contentType}, "Cache-Control": {"no-cache"}, "X-Content-Type-Options": {"nosniff"}}, Body: data, ETag: `"` + digest(data) + `"`}
		}()
		func() {
			defer func() {
				if recover() != nil {
					panic(http.ErrAbortHandler)
				}
			}()
			if err := response.Write(w, request); err != nil {
				panic(http.ErrAbortHandler)
			}
		}()
	}), nil
}

func staticConditionalLines(lines []string) ([]string, bool) {
	if len(lines) > 32 {
		return nil, false
	}
	remaining := 8192
	for _, line := range lines {
		if len(line) > remaining {
			return nil, false
		}
		remaining -= len(line)
	}
	return append([]string(nil), lines...), true
}
func staticText(status int, message string) ghttp.Response {
	response := ghttp.Text(status, message)
	response.Headers.Set("Cache-Control", "no-store")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
	return response
}
func (c Collector) developmentAsset(ctx context.Context, name string) (data []byte, err error) {
	defer func() {
		if recover() != nil {
			data = nil
			err = fail(ErrSource, contextError(ctx))
		}
	}()
	result, e := c.discover(ctx, false)
	if e != nil {
		return nil, e
	}
	for _, match := range result.matches {
		if match.Path != name || !match.Selected {
			continue
		}
		root, e := openSource(c.config.Sources[match.Source])
		if e != nil {
			return nil, e
		}
		defer func() {
			err = errors.Join(err, root.close())
			if err != nil {
				data = nil
			}
		}()
		return readAsset(ctx, root.fs, name, c.config.Limits.MaxFileBytes)
	}
	return nil, ErrNotFound
}
