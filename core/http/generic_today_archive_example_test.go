package http_test

import (
	"net/http"

	"github.com/Newton-School/gogo/core/auth"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleNewTodayArchiveView() {
	// Bootstrap supplies a registered model, opened Store, explicit output
	// Fields, permission Policy and row Scope; authentication wraps this router.
	build := func(model ghttp.ModelReadOptions) (http.Handler, error) {
		locale, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "Asia/Kolkata"})
		if err != nil {
			return nil, err
		}
		view, err := ghttp.NewTodayArchiveView(ghttp.TodayArchiveViewOptions{
			ListViewOptions: ghttp.ListViewOptions{
				ModelReadOptions: model,
				TemplateViewOptions: ghttp.TemplateViewOptions{
					ReadViewOptions: ghttp.ReadViewOptions{Authorize: func(r *http.Request) error {
						if !auth.FromContext(r.Context()).Authenticated {
							return auth.ErrUnauthenticated
						}
						return r.Context().Err()
					}},
					TemplateName: "articles/today.html",
					Templates: templates.Config{LocaleResolver: locale, Loaders: []templates.Loader{templates.MapLoader{
						"articles/today.html": `<h1>{{ day }}</h1>{% for article in object_list %}<p>{{ article.title }}</p>{% endfor %}`,
					}}},
				},
			},
			DateField: "published_at", AllowEmpty: true,
		})
		if err != nil {
			return nil, err
		}
		return urls.New(urls.Path("articles/today/", view, "article-today"))
	}
	_ = build
}
