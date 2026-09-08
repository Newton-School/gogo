package files_test

import (
	"context"
	"io"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/files"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func ExampleNewService() {
	// Compile-only composition: inject an opened backend, private Storage and
	// real application authority. Apply files.Migrations separately at deploy.
	configure := func(backend db.Backend, storage files.Storage, scope orm.QueryScope, authorize func(context.Context, files.OwnerAction, files.OwnerSnapshot) error) (*files.Service, error) {
		registry := &models.Registry{}
		document := models.Schema{AppLabel: "documents", Name: "Document", Fields: []models.Field{
			models.BigIntegerField("id", models.Primary),
			models.TextField("tenant"),
			models.FileField("attachment", models.Optional, models.WithMaxLength(32)),
		}}
		for _, schema := range []models.Schema{document, (&files.File{}).Schema()} {
			if err := registry.Register(schema); err != nil {
				return nil, err
			}
		}
		if err := registry.Freeze(); err != nil {
			return nil, err
		}
		return files.NewService(files.ServiceConfig{Backend: backend, Registry: registry, StorageAlias: "private", Storage: storage, Bindings: []files.OwnerBinding{{Name: "attachment", Model: document.Key(), Field: "attachment", PolicyFields: []string{"tenant"}, Scope: scope, Authorize: authorize}}})
	}
	_ = configure
}

func ExampleService_StoreValidated() {
	// The identity is durably retained by the caller before invoking this
	// closure. The input reader and canonical MIME have already been approved
	// by the application's upload-validation boundary.
	bind := func(ctx context.Context, service *files.Service, identity files.UploadIdentity, approved io.Reader) (files.Info, error) {
		return service.StoreValidated(ctx, files.StoreInput{Identity: identity, Owner: files.OwnerInput{Binding: "attachment", Key: map[string]any{"id": int64(42)}}, ContentType: "text/plain; charset=utf-8"}, approved)
	}
	_ = bind
}
