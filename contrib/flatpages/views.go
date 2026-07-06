package flatpages

import (
	"html"
	"net/http"
	"strings"

	"github.com/Newton-School/gogo/contrib/sites"
)

type Options struct {
	Templates     map[string]string
	Authenticated func(*http.Request) bool
}

func View(store Store, siteStore sites.Store, options Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site, err := sites.CurrentSite(r.Context(), r, siteStore, sites.Settings{})
		if err != nil {
			http.NotFound(w, r)
			return
		}
		page, ok := store.Find(r.URL.Path, site.ID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if page.RegistrationRequired && (options.Authenticated == nil || !options.Authenticated(r)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderPage(page, options.Templates)))
	})
}

func renderPage(page FlatPage, templates map[string]string) string {
	template := DefaultTemplate
	if page.TemplateName != "" && templates != nil {
		if custom := templates[page.TemplateName]; custom != "" {
			template = custom
		}
	}
	output := strings.ReplaceAll(template, "{{title}}", html.EscapeString(page.Title))
	output = strings.ReplaceAll(output, "{{content}}", html.EscapeString(page.Content))
	return output
}
