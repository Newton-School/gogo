package flatpages

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/templates"
)

const defaultPageTemplate = "flatpages/default.html"

type pageRenderer struct {
	engine   *templates.Engine
	name     string
	allowed  map[string]bool
	sanitize func(context.Context, Info) (templates.SafeHTML, error)
	maxBytes int
}

func newPageRenderer(options RenderOptions) (*pageRenderer, error) {
	if options.DefaultTemplate == "" {
		options.DefaultTemplate = defaultPageTemplate
	}
	if !validTemplateName(options.DefaultTemplate) || len(options.AllowedTemplates) > 64 || options.MaxOutputBytes < 0 || options.MaxOutputBytes > 16<<20 {
		return nil, ErrConfiguration
	}
	if options.MaxOutputBytes == 0 {
		options.MaxOutputBytes = 8 << 20
	}
	allowed := map[string]bool{}
	if options.AllowedTemplates == nil {
		allowed[options.DefaultTemplate] = true
	} else {
		for _, name := range options.AllowedTemplates {
			if !validTemplateName(name) || allowed[name] {
				return nil, ErrConfiguration
			}
			allowed[name] = true
		}
		if !allowed[options.DefaultTemplate] {
			return nil, ErrConfiguration
		}
	}
	// Engine owns copies of its extension collections and its mutex. Also copy
	// the built-in mutable map loader; arbitrary application loaders are trusted
	// providers and retain responsibility for their own concurrency and sources.
	config := options.Templates
	config.Loaders = append([]templates.Loader(nil), config.Loaders...)
	for i, loader := range config.Loaders {
		if nilValue(loader) {
			return nil, ErrConfiguration
		}
		var source templates.MapLoader
		switch value := loader.(type) {
		case templates.MapLoader:
			source = value
		case *templates.MapLoader:
			source = *value
		default:
			continue
		}
		copy := templates.MapLoader{}
		for name, content := range source {
			copy[name] = content
		}
		config.Loaders[i] = copy
	}
	return &pageRenderer{engine: templates.New(config), name: options.DefaultTemplate, allowed: allowed, sanitize: options.Sanitize, maxBytes: options.MaxOutputBytes}, nil
}

// render is private because its caller owns the reader grant. Stored content is
// always a value, never template source. Trusted templates/extensions may opt
// out of escaping; this helper is not a sandbox for application code.
func (r *pageRenderer) render(ctx context.Context, page Info, site sites.Info) (output string, err error) {
	if r == nil {
		return "", ErrConfiguration
	}
	operation := *r // Snapshot before invoking any Context method or callback.
	defer func() {
		if recover() != nil {
			output, err = "", ErrUnavailable
		}
		if contextErr := contextError(ctx); contextErr != nil {
			output, err = "", contextErr
		}
		if err != nil {
			output = ""
		}
	}()
	if err := contextError(ctx); err != nil {
		return "", err
	}
	draft := Draft(page)
	validated, err := validateDraft(draft)
	if err != nil || validated != draft {
		return "", ErrInvalid
	}
	if id, err := pageID(page.ID); err != nil || id != page.ID {
		return "", ErrInvalid
	}
	if !validRenderSite(site) {
		return "", ErrInvalid
	}
	name := page.TemplateName
	if name == "" {
		name = operation.name
	}
	if operation.engine == nil || !operation.allowed[name] || operation.maxBytes <= 0 || operation.maxBytes > 16<<20 {
		return "", ErrUnavailable
	}
	var content any = page.Content
	if operation.sanitize != nil {
		safe, err := operation.sanitize(ctx, page)
		if err != nil {
			if err == ErrForbidden {
				return "", ErrForbidden
			}
			return "", ErrUnavailable
		}
		if err := contextError(ctx); err != nil {
			return "", err
		}
		if len(safe) > MaxContentBytes || !utf8.ValidString(string(safe)) || strings.ContainsRune(string(safe), 0) {
			return "", ErrUnavailable
		}
		content = safe
	}
	values := templates.Context{
		"flatpage": templates.Context{"id": page.ID, "url": page.URL, "title": page.Title, "content": content, "template_name": page.TemplateName, "registration_required": page.RegistrationRequired},
		"site":     templates.Context{"id": site.ID, "domain": site.Domain, "display_name": site.DisplayName, "active": site.Active},
	}
	output, err = operation.engine.Render(ctx, name, values)
	if err != nil {
		return "", ErrUnavailable
	}
	if len(output) > operation.maxBytes || !utf8.ValidString(output) || strings.ContainsRune(output, 0) {
		return "", ErrUnavailable
	}
	return output, nil
}

func validRenderSite(site sites.Info) bool {
	if !site.Active || len(site.Domain) > 253 || len(site.DisplayName) == 0 || len(site.DisplayName) > 4*255 || !utf8.ValidString(site.DisplayName) || utf8.RuneCountInString(site.DisplayName) > 255 {
		return false
	}
	if id, err := pageID(site.ID); err != nil || id != site.ID {
		return false
	}
	if domain, err := sites.NormalizeDomain(site.Domain); err != nil || domain != site.Domain {
		return false
	}
	for _, r := range site.DisplayName {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
