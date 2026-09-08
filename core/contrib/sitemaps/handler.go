package sitemaps

import (
	"context"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/urls"
)

var sectionName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var pageKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type registeredSection struct {
	name     string
	provider Provider
}

type coordinator struct {
	renderer Renderer
	sections []registeredSection
	options  HandlerOptions
}

// Routes registers sitemap_index and sitemap_page beneath Config.Directory.
// Mount without adding a path prefix; a namespace-only urls.Include is safe.
// The configured directory also bounds primary document locations. Source
// manifests are read afresh and authorized before conditional HTTP responses.
func (r *Renderer) Routes(sections []Section, options HandlerOptions) ([]urls.Route, error) {
	if r == nil || r.urls == nil || options.Authorize == nil || len(sections) == 0 || len(sections) > 128 || r.policy.Entry == nil || r.policy.Index == nil {
		return nil, ErrInvalid
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.Timeout < time.Millisecond || options.Timeout > 5*time.Minute {
		return nil, ErrInvalid
	}
	c := &coordinator{renderer: *r, options: options}
	seen := map[string]bool{}
	for _, section := range sections {
		if !sectionName.MatchString(section.Name) || seen[section.Name] || nilProvider(section.Provider) {
			return nil, ErrInvalid
		}
		seen[section.Name] = true
		c.sections = append(c.sections, registeredSection{section.Name, section.Provider})
	}
	return []urls.Route{
		urls.Path(r.urls.directory+"sitemap.xml", c.handler(false), "sitemap_index"),
		urls.Path(r.urls.directory+"sitemap-<slug:section>.<slug:page>.xml", c.handler(true), "sitemap_page"),
	}, nil
}

// Handler is a ready-to-mount router for Routes. Put it behind the project's
// host/security middleware; URLs never derive authority from request.Host.
func (r *Renderer) Handler(sections []Section, options HandlerOptions) (http.Handler, error) {
	routes, err := r.Routes(sections, options)
	if err != nil {
		return nil, err
	}
	return urls.New(routes...)
}

func nilProvider(provider Provider) bool {
	if provider == nil {
		return true
	}
	v := reflect.ValueOf(provider)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func (c *coordinator) handler(page bool) http.Handler {
	return ghttp.Adapt(func(request *http.Request) (response ghttp.Response, err error) {
		defer func() {
			if recover() != nil {
				response, err = ghttp.Response{}, ErrUnavailable
			}
			if err != nil {
				status, code := http.StatusServiceUnavailable, "SITEMAP_UNAVAILABLE"
				if err == ErrNotPublic {
					status, code = http.StatusForbidden, "SITEMAP_NOT_PUBLIC"
				}
				response, err = ghttp.JSON(status, struct {
					Code      string `json:"code"`
					Detail    string `json:"detail"`
					RequestID string `json:"request_id,omitempty"`
				}{code, "Sitemap unavailable", ghttp.RequestID(request)})
				response.Headers.Set("Cache-Control", "no-store")
			}
			if response.Headers == nil {
				response.Headers = http.Header{}
			}
			response.Headers.Set("X-Content-Type-Options", "nosniff")
		}()
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			return sitemapStatus(http.StatusMethodNotAllowed, true), nil
		}
		if request.URL == nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.ContentLength != 0 || len(request.TransferEncoding) != 0 || request.Body != nil && request.Body != http.NoBody || request.Header.Get("Content-Encoding") != "" {
			return sitemapStatus(http.StatusBadRequest, false), nil
		}
		ctx, cancel := context.WithTimeout(request.Context(), c.options.Timeout)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return ghttp.Response{}, err
		}
		var doc Document
		if page {
			section, _ := urls.Param(request, "section").(string)
			key, _ := urls.Param(request, "page").(string)
			if !sectionName.MatchString(section) || !pageKey.MatchString(key) {
				return sitemapStatus(http.StatusBadRequest, false), nil
			}
			selected := c.section(section)
			if selected == nil {
				return sitemapStatus(http.StatusNotFound, false), nil
			}
			var found bool
			doc, found, err = c.page(ctx, *selected, key)
			if err == nil && !found {
				return sitemapStatus(http.StatusNotFound, false), nil
			}
		} else {
			doc, err = c.index(ctx)
		}
		if ctx.Err() != nil {
			return ghttp.Response{}, ctx.Err()
		}
		if err != nil {
			return ghttp.Response{}, err
		}
		cacheControl := "private, no-cache"
		if c.options.PublicCache {
			cacheControl = "public, max-age=0, must-revalidate"
		}
		return ghttp.Response{Status: http.StatusOK, Headers: http.Header{
			"Content-Type": {doc.ContentType}, "Content-Length": {strconv.Itoa(len(doc.Body))}, "Cache-Control": {cacheControl},
		}, Body: doc.Body, ETag: doc.ETag}, nil
	})
}

func sitemapStatus(status int, allow bool) ghttp.Response {
	r := ghttp.Response{Status: status, Headers: http.Header{"Cache-Control": {"no-store"}}}
	if allow {
		r.Headers.Set("Allow", "GET, HEAD")
	}
	return r
}

func (c *coordinator) section(name string) *registeredSection {
	for i := range c.sections {
		if c.sections[i].name == name {
			return &c.sections[i]
		}
	}
	return nil
}

func (c *coordinator) authorize(ctx context.Context, section string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := c.options.Authorize(ctx, section)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || err == ErrNotPublic {
		return err
	}
	return ErrUnavailable
}

func (c *coordinator) manifest(ctx context.Context, section registeredSection, limit int) ([]PageRef, error) {
	if err := c.authorize(ctx, section.name); err != nil {
		return nil, err
	}
	refs, err := section.provider.Manifest(ctx, limit+1)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(refs) > limit {
		return nil, ErrLimit
	}
	result := make([]PageRef, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if !pageKey.MatchString(ref.Key) || seen[ref.Key] || ref.Revision == "" || len(ref.Revision) > 1024 || !utf8.ValidString(ref.Revision) || strings.ContainsAny(ref.Revision, "\x00\r\n") {
			return nil, ErrInvalid
		}
		if _, err := lastModValue(ref.LastMod); err != nil {
			return nil, err
		}
		seen[ref.Key] = true
		ref.LastMod = cloneLastModified(ref.LastMod)
		result = append(result, ref)
	}
	return result, nil
}

func (c *coordinator) index(ctx context.Context) (Document, error) {
	var entries []IndexEntry
	limit := min(c.renderer.maxPages, c.renderer.limits.MaxItems)
	for _, section := range c.sections {
		refs, err := c.manifest(ctx, section, limit-len(entries))
		if err != nil {
			return Document{}, err
		}
		for _, ref := range refs {
			entries = append(entries, IndexEntry{Loc: c.renderer.urls.directory + "sitemap-" + section.name + "." + ref.Key + ".xml", LastMod: ref.LastMod})
		}
	}
	return c.renderer.RenderIndex(ctx, entries)
}

func (c *coordinator) page(ctx context.Context, section registeredSection, key string) (Document, bool, error) {
	refs, err := c.manifest(ctx, section, min(c.renderer.maxPages, c.renderer.limits.MaxItems))
	if err != nil {
		return Document{}, false, err
	}
	var expected *PageRef
	for _, ref := range refs {
		if ref.Key == key {
			copy := ref
			expected = &copy
			break
		}
	}
	if expected == nil {
		return Document{}, false, nil
	}
	if err := c.authorize(ctx, section.name); err != nil {
		return Document{}, false, err
	}
	view := *expected
	view.LastMod = cloneLastModified(view.LastMod)
	page, err := section.provider.LoadPage(ctx, view, c.renderer.limits)
	if ctx.Err() != nil {
		return Document{}, false, ctx.Err()
	}
	if err != nil || !reflect.DeepEqual(view, *expected) {
		return Document{}, false, ErrUnavailable
	}
	if _, err := lastModValue(page.Ref.LastMod); err != nil {
		return Document{}, false, err
	}
	page.Ref.LastMod = cloneLastModified(page.Ref.LastMod)
	if !reflect.DeepEqual(page.Ref, *expected) {
		return Document{}, false, ErrStale
	}
	// A later policy callback may retain the provider's output through its
	// closure. Freeze that output before invoking any further application code,
	// so the representation cannot change after its revision has been checked.
	entries, err := freezePageEntries(page.Entries, c.renderer.limits.MaxItems, c.renderer.maxBuildBytes)
	if err != nil {
		return Document{}, false, err
	}
	if err := c.authorize(ctx, section.name); err != nil {
		return Document{}, false, err
	}
	doc, err := c.renderer.RenderPage(ctx, entries)
	return doc, true, err
}

func freezePageEntries(entries []Entry, maxItems, maxInputBytes int) ([]Entry, error) {
	if len(entries) > maxItems {
		return nil, ErrLimit
	}
	remaining := maxInputBytes
	for _, entry := range entries {
		size, err := entryInputSize(entry)
		if err != nil {
			return nil, err
		}
		// Cloning normalizes timestamps to UTC. Validate the original civil
		// year/offset before that conversion can erase invalid source metadata.
		if _, err := lastModValue(entry.LastMod); err != nil {
			return nil, err
		}
		if size > remaining {
			return nil, ErrLimit
		}
		remaining -= size
	}
	owned := make([]Entry, len(entries))
	for i, entry := range entries {
		owned[i] = cloneEntry(entry)
	}
	return owned, nil
}
