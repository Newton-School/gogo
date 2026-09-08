// Package admindocs builds optional, staff-authorized reference pages from
// explicit registered-model selections and public route/template descriptors.
package admindocs

import (
	"net/http"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

type Options struct {
	Models        []admin.DocumentationModel
	Router        *urls.Router
	Templates     *templates.Engine
	Views         []admin.DocumentationView
	Tags, Filters []admin.DocumentationExtension
}

// New freezes a reference page at the configured admin.Config.Prefix + "doc/". Mount the result
// explicitly before any encompassing Site route, inside the same session and
// auth middleware. Omitting that mount disables documentation entirely.
// Tags/Filters supply optional plain descriptions of names actually present in
// Templates; Views describes existing named routes. No handler, converter,
// loader, processor, model factory, tag or filter is executed by this builder.
func New(site *admin.Site, options Options) (http.Handler, error) {
	if site == nil || options.Router == nil || options.Templates == nil {
		return nil, admin.ErrDocumentation
	}
	routes, err := options.Router.Describe(4096)
	if err != nil {
		return nil, admin.ErrDocumentation
	}
	extensions, err := options.Templates.Describe(4096)
	if err != nil {
		return nil, admin.ErrDocumentation
	}
	tags, ok := describeExtensions(extensions.Tags, options.Tags)
	if !ok {
		return nil, admin.ErrDocumentation
	}
	filters, ok := describeExtensions(extensions.Filters, options.Filters)
	if !ok {
		return nil, admin.ErrDocumentation
	}
	data := admin.DocumentationOptions{Models: options.Models, Views: options.Views, Tags: tags, Filters: filters}
	data.Routes = make([]admin.DocumentationRoute, len(routes.Routes))
	for i, route := range routes.Routes {
		// Handler identities and converter functions are deliberately dropped.
		data.Routes[i] = admin.DocumentationRoute{Name: route.Name, Pattern: route.Pattern, Methods: route.Methods}
	}
	return site.DocumentationHandler(data)
}

func describeExtensions(names []string, descriptions []admin.DocumentationExtension) ([]admin.DocumentationExtension, bool) {
	if len(names) > 512 || len(descriptions) > 512 {
		return nil, false
	}
	index := make(map[string]int, len(names))
	result := make([]admin.DocumentationExtension, len(names))
	for i, name := range names {
		index[name] = i
		result[i].Name = name
	}
	seen := make(map[string]bool, len(descriptions))
	for _, value := range descriptions {
		if len(value.Name) > 128 || len(value.Description) > 4096 {
			return nil, false
		}
		i, ok := index[value.Name]
		if !ok || seen[value.Name] {
			return nil, false
		}
		seen[value.Name] = true
		result[i].Description = value.Description
	}
	return result, true
}
