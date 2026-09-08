package serialization_test

import (
	"context"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/serialization"
)

// ExampleNew is compile-only: inject an explicitly opened backend and a tenant
// selected by trusted application authority. No connection is created here.
func ExampleNew() {
	configure := func(backend db.Backend, authorizedTenant string) (*serialization.Fixtures, error) {
		if authorizedTenant == "" {
			return nil, serialization.ErrConfiguration
		}
		schema := models.Schema{AppLabel: "notes", Name: "Note", Fields: []models.Field{
			{Name: "id", Kind: models.BigInteger, PrimaryKey: true},
			{Name: "tenant", Kind: models.Char, MaxLength: 128},
			{Name: "title", Kind: models.Text},
		}}
		return serialization.New(serialization.Config{Backend: backend, Profiles: []serialization.ModelProfile{{
			Schema: schema, Fields: []string{"id", "tenant", "title"}, Import: true,
			Scope: func(context.Context, models.Schema) (db.Predicate, error) {
				return orm.Q("tenant", authorizedTenant), nil
			},
			Authorize: func(_ context.Context, _ serialization.Action, row serialization.Record) error {
				if row.Fields["tenant"] != authorizedTenant {
					return serialization.ErrForbidden
				}
				return nil
			},
		}}})
	}
	_ = configure
	// After explicit migrations/configuration:
	// source.Dump(ctx, writer, serialization.DumpOptions{
	//     Format: serialization.JSONL, Models: []string{"notes.Note"},
	// })
	// target.Load(ctx, reader, serialization.LoadOptions{
	//     Format: serialization.JSONL, Models: []string{"notes.Note"}, DryRun: true,
	// })
	// A real load uses a separate clean target or nonconflicting caller-known IDs.
}
