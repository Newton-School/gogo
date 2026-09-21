package auth

import (
	"context"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// Capture the database-assigned ID before any application AfterSave receiver.
// The caller's final checks then reject an after-hook that retargets the row.
// BeforeSave/Guard still see an empty ID and reject explicit-ID injection.
func (a *Accounts) saveIdentity(ctx context.Context, model models.Model, options orm.SaveOptions, expectedID *string) error {
	if !options.ForceInsert || *expectedID != "" {
		return a.store.Save(ctx, model, options)
	}
	store := *a.store
	store.AfterSave = append([]orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		value, err := event.Record.Get("id")
		id, ok := value.(string)
		if err != nil || !ok || !event.Created || !a.models.validID(id) {
			return ErrPermissionDenied
		}
		*expectedID = id
		return nil
	}}, store.AfterSave...)
	return store.Save(ctx, model, options)
}
