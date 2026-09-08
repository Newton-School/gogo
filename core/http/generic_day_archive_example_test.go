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

func ExampleNewDayArchiveView() {
	// This constructor example compiles without opening a service. Bootstrap
	// supplies the opened backend and applies migrations before serving.
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
		view, err := ghttp.NewDayArchiveView(ghttp.DayArchiveViewOptions{
			ListViewOptions: ghttp.ListViewOptions{
				ModelReadOptions: ghttp.ModelReadOptions{
					Store: orm.New(backend, registry), Model: schema.Key(), Fields: []string{"title"}, Policy: auth.ModelPolicy{},
					Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
						// Verified user IDs in this application are UUIDs. The date
						// URL never grants visibility or publication by itself.
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
					TemplateName: "articles/day.html",
					Templates: templates.Config{LocaleResolver: locale, Loaders: []templates.Loader{templates.MapLoader{
						"articles/day.html": `<h1>{{ day }}</h1>{% for article in object_list %}<p>{{ article.title }}</p>{% endfor %}{% if previous_day %}<a href="/articles/{{ previous_day }}/">Previous day</a>{% endif %}`,
					}}},
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
		// Verified authentication middleware must supply the principal and
		// news.view_article grant. The template's explicit path matches this
		// route; no router or request is auto-injected into template context.
		return urls.New(urls.Path("articles/<str:date>/", view, "article-day"))
	}
	_ = build
}
