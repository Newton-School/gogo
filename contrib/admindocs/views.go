package admindocs

import (
	"html"
	"net/http"
	"strings"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/auth"
)

type RouteDoc struct {
	Name string
	Path string
}

type DocConfig struct {
	Registry        *admin.Registry
	Routes          []RouteDoc
	TemplateTags    []string
	TemplateFilters []string
	Settings        []string
	Commands        []string
}

type Docs struct {
	Models   []string
	Admins   []string
	Routes   []string
	Tags     []string
	Filters  []string
	Settings []string
	Commands []string
}

func Generate(config DocConfig) Docs {
	docs := Docs{
		Tags:     append([]string(nil), config.TemplateTags...),
		Filters:  append([]string(nil), config.TemplateFilters...),
		Settings: append([]string(nil), config.Settings...),
		Commands: append([]string(nil), config.Commands...),
	}
	if config.Registry != nil {
		for _, label := range config.Registry.RegisteredModels() {
			docs.Models = append(docs.Models, label)
			if modelAdmin, ok := config.Registry.GetAdmin(label); ok {
				docs.Admins = append(docs.Admins, modelAdmin.Handler)
			}
		}
	}
	for _, route := range config.Routes {
		if route.Name != "" {
			docs.Routes = append(docs.Routes, route.Name)
		}
		if route.Path != "" {
			docs.Routes = append(docs.Routes, route.Path)
		}
	}
	return docs
}

func Handler(config DocConfig, userForRequest func(*http.Request) auth.User) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := auth.User{}
		if userForRequest != nil {
			user = userForRequest(r)
		}
		if !user.IsActive || !user.IsStaff {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(Render(Generate(config))))
	})
}

func Render(docs Docs) string {
	var builder strings.Builder
	builder.WriteString("<!doctype html><html><body><h1>Admin documentation</h1>")
	writeList(&builder, "Models", docs.Models)
	writeList(&builder, "Admin classes", docs.Admins)
	writeList(&builder, "Routes", docs.Routes)
	writeList(&builder, "Template tags", docs.Tags)
	writeList(&builder, "Template filters", docs.Filters)
	writeList(&builder, "Settings", docs.Settings)
	writeList(&builder, "Commands", docs.Commands)
	builder.WriteString("</body></html>")
	return builder.String()
}

func writeList(builder *strings.Builder, title string, values []string) {
	builder.WriteString("<h2>" + html.EscapeString(title) + "</h2><ul>")
	for _, value := range values {
		if value == "" {
			continue
		}
		builder.WriteString("<li>" + html.EscapeString(value) + "</li>")
	}
	builder.WriteString("</ul>")
}
