package http_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleNewCreateView() {
	// Compile-only bootstrap example. Supply an opened backend and apply the
	// corresponding model migration before serving this handler. Surround the
	// router with trusted authentication middleware and correct TLS/proxy setup.
	build := func(backend db.Backend) (http.Handler, error) {
		schema := models.Schema{AppLabel: "notes", Name: "Note", Fields: []models.Field{
			models.BigAutoField("id"), models.TextField("text"), models.TextField("owner_id"),
		}}
		registry := &models.Registry{}
		if err := registry.Register(schema); err != nil {
			return nil, err
		}
		view, err := ghttp.NewCreateView(ghttp.CreateViewOptions{
			TemplateViewOptions: ghttp.TemplateViewOptions{
				ReadViewOptions: ghttp.ReadViewOptions{Authorize: func(r *http.Request) error {
					if !auth.FromContext(r.Context()).Authenticated {
						return auth.ErrUnauthenticated
					}
					return r.Context().Err()
				}},
				TemplateName: "notes/create.html",
				Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
					"notes/create.html": `<h1>Create a note</h1><form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">{{ form.html }}<button type="submit">Create note</button></form>`,
				}}},
			},
			Store: orm.New(backend, registry), Model: schema.Key(), Fields: []string{"text"},
			Policy:  auth.ModelPolicy{}, // Requires the verified notes.add_note grant.
			Factory: func() models.Model { record, _ := models.NewRecord(schema); return record },
			Scope: func(_ context.Context, principal auth.Principal, _ models.Schema) (db.Predicate, error) {
				return orm.Q("owner_id", principal.ID), nil
			},
			Prepare: func(ctx context.Context, record models.Record) error {
				return record.Set("owner_id", auth.FromContext(ctx).ID)
			},
			ValidateWrite: func(_ context.Context, principal auth.Principal, record models.Record) error {
				owner, err := record.Get("owner_id")
				if err != nil || owner != principal.ID {
					return auth.ErrPermissionDenied
				}
				return nil
			},
			SuccessURL: func(_ context.Context, identity map[string]any) (string, error) {
				return fmt.Sprintf("/notes/%v/", identity["id"]), nil
			},
		})
		if err != nil {
			return nil, err
		}
		// The application's separately scoped detail view owns /notes/<id>/.
		return urls.New(urls.Path("notes/new/", view, "note-create"))
	}
	_ = build
}
