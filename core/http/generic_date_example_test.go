package http_test

import (
	"context"
	"net/http"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleNewDateDetailView() {
	// Bootstrap supplies an opened backend and applies migrations separately.
	// This constructor example compiles but does not open a database or serve.
	build := func(backend db.Backend) (http.Handler, error) {
		registry := &models.Registry{}
		schema := models.Schema{AppLabel: "news", Name: "Article", Fields: []models.Field{
			models.UUIDField("id", models.Primary), models.UUIDField("owner_id"),
			models.TextField("title"), models.BooleanField("published"), models.DateTimeField("published_at"),
		}}
		if err := registry.Register(schema); err != nil {
			return nil, err
		}
		if err := registry.Freeze(); err != nil {
			return nil, err
		}
		locale, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "Asia/Kolkata"})
		if err != nil {
			return nil, err
		}
		view, err := ghttp.NewDateDetailView(ghttp.DateDetailViewOptions{
			DetailViewOptions: ghttp.DetailViewOptions{
				ModelReadOptions: ghttp.ModelReadOptions{
					Store: orm.New(backend, registry), Model: schema.Key(), Fields: []string{"title", "published_at"},
					Policy: auth.ModelPolicy{},
					Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
						// The date URL narrows visibility; it does not grant publication
						// or ownership. Verified user IDs in this app are UUIDs.
						return orm.And(orm.Q("owner_id", p.ID), orm.Q("published", true)), nil
					},
				},
				TemplateViewOptions: ghttp.TemplateViewOptions{
					ReadViewOptions: ghttp.ReadViewOptions{Authorize: func(r *http.Request) error {
						if !auth.FromContext(r.Context()).Authenticated {
							return auth.ErrUnauthenticated
						}
						return r.Context().Err()
					}},
					TemplateName: "articles/detail.html",
					Templates: templates.Config{LocaleResolver: locale, Loaders: []templates.Loader{templates.MapLoader{
						"articles/detail.html": `<h1>{{ object.title }}</h1><time>{{ object.published_at|date:"c" }}</time>`,
					}}},
				},
				Key: func(r *http.Request) (map[string]any, error) {
					return map[string]any{"id": urls.Param(r, "id")}, nil
				},
			},
			DateField: "published_at",
			Date: func(r *http.Request) (string, error) {
				value, ok := urls.Param(r, "date").(string)
				if !ok {
					return "", ghttp.ErrInvalidLookup
				}
				return value, nil
			},
		})
		if err != nil {
			return nil, err
		}
		// Authentication middleware must install the verified principal and
		// news.view_article grant before dispatching this router.
		return urls.New(urls.Path("articles/<str:date>/<uuid:id>/", view, "article-date-detail"))
	}
	_ = build
}
