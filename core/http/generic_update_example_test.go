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

func ExampleNewUpdateView() {
	// Compile-only bootstrap example: supply a migrated backend with transaction
	// and row-lock support. Authentication and TLS/trusted proxy setup wrap this
	// router; the destination detail view applies its own current authorization.
	build := func(backend db.Backend) (http.Handler, error) {
		schema := models.Schema{AppLabel: "notes", Name: "Note", Fields: []models.Field{
			models.BigAutoField("id"), models.TextField("text"), models.TextField("owner_id"),
		}}
		registry := &models.Registry{}
		if err := registry.Register(schema); err != nil {
			return nil, err
		}
		view, err := ghttp.NewUpdateView(ghttp.UpdateViewOptions{
			TemplateViewOptions: ghttp.TemplateViewOptions{
				ReadViewOptions: ghttp.ReadViewOptions{Authorize: func(r *http.Request) error {
					if !auth.FromContext(r.Context()).Authenticated {
						return auth.ErrUnauthenticated
					}
					return r.Context().Err()
				}},
				TemplateName: "notes/update.html",
				Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
					"notes/update.html": `<h1>Edit note</h1><form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">{{ form.html }}<button type="submit">Save changes</button></form>`,
				}}},
			},
			Store: orm.New(backend, registry), Model: schema.Key(), Fields: []string{"text"},
			Policy:  auth.ModelPolicy{}, // Requires the verified notes.change_note grant.
			Factory: func() models.Model { record, _ := models.NewRecord(schema); return record },
			Key:     func(r *http.Request) (map[string]any, error) { return map[string]any{"id": urls.Param(r, "id")}, nil },
			Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
				return orm.Q("owner_id", p.ID), nil
			},
			ValidateWrite: func(_ context.Context, p auth.Principal, record models.Record) error {
				owner, err := record.Get("owner_id")
				if err != nil || owner != p.ID {
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
		return urls.New(urls.Path("notes/<int:id>/edit/", view, "note-update"))
	}
	_ = build
}
