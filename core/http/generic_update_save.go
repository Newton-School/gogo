package http

import (
	"context"
	"net/http"
	"net/url"
	"reflect"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type genericUpdateInvalid struct{ response Response }

func (*genericUpdateInvalid) Error() string { return "http: invalid update form" }

func (v *genericUpdate) writeGrant(ctx context.Context, row genericModelRow) error {
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	record, err := v.form.policyRecord(row, true)
	if err != nil {
		return err
	}
	err = v.form.options.ValidateWrite(ctx, auth.FromContext(ctx), record)
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	return v.objectGrant(ctx, row)
}

func (v *genericUpdate) checkIdentity(ctx context.Context, record models.Record, identity map[string]any) error {
	if !genericContextOK(ctx) || record.State() == nil || !record.State().Persisted || record.State().Database != v.form.model.store.Backend.Alias() || len(record.State().Deferred) != 0 || !v.form.matches(record) {
		return ErrUnavailable
	}
	row, err := v.form.snapshot(record)
	if err != nil {
		return err
	}
	current, err := v.form.identity(row)
	if err != nil || !reflect.DeepEqual(current, identity) || !genericContextOK(ctx) {
		return ErrUnavailable
	}
	return nil
}

func (v *genericUpdate) matchStored(ctx context.Context, query orm.Query[*models.MapRecord], expected genericModelRow) error {
	row, err := v.load(ctx, query)
	if err != nil {
		if err == ErrNotFound {
			return auth.ErrPermissionDenied
		}
		return err
	}
	if !reflect.DeepEqual(row.values, expected.values) {
		return auth.ErrPermissionDenied
	}
	return nil
}

func (v *genericUpdate) save(call *readViewCall, query orm.Query[*models.MapRecord], identity map[string]any, data url.Values) (response Response, err error) {
	ctx := call.base.Context()
	committed, prepared := false, false
	var invalid *genericUpdateInvalid
	defer func() {
		if recover() != nil {
			if committed {
				response, err = Text(503, "Update committed, but post-commit processing failed. Do not resubmit.\n"), nil
			} else {
				response, err = Text(503, "Update outcome is unknown. Reconcile before retrying.\n"), nil
			}
		}
	}()
	if db.InTransaction(ctx, v.form.model.store.Backend.Alias()) || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	query = query.SelectForUpdate(false, false)
	err = db.Atomic(ctx, v.form.model.store.Backend, db.AtomicOptions{Durable: true}, func(txCtx context.Context) (txErr error) {
		defer func() {
			if recover() != nil {
				txErr = ErrUnavailable
			}
		}()
		// Register before every application callback. Only this outer commit
		// callback is evidence of commit, not an error returned by user code.
		if err := db.OnCommit(txCtx, v.form.model.store.Backend.Alias(), func(context.Context) error { committed = true; return nil }, false); err != nil {
			return err
		}
		txCall := &readViewCall{base: call.base.WithContext(txCtx), routeParams: call.routeParams}
		if err := v.authorize(txCall); err != nil {
			return err
		}
		before, err := v.load(txCtx, query)
		if err != nil {
			return err
		}
		loadedID, err := v.form.identity(before)
		if err != nil || !reflect.DeepEqual(loadedID, identity) {
			return ErrUnavailable
		}
		if err := v.objectGrant(txCtx, before); err != nil {
			return err
		}
		model, record, err := v.hydrate(txCtx, before)
		if err != nil {
			return err
		}
		form, err := v.modelForm(txCtx, record, before, data, true)
		if err != nil {
			return err
		}
		valid := form.IsValid()
		if form.Err() != nil || !genericContextOK(txCtx) {
			return ErrUnavailable
		}
		if !valid {
			// Rendering remains inside the rollback boundary, so trusted model
			// or template hooks cannot accidentally commit invalid-form effects.
			response, err := v.renderForm(txCall, form, before, true)
			if err != nil {
				return err
			}
			invalid = &genericUpdateInvalid{response: response}
			return invalid
		}
		if _, err := form.Save(false); err != nil {
			return ErrUnavailable
		}
		if err := v.checkIdentity(txCtx, record, identity); err != nil {
			return err
		}
		if v.form.options.Prepare != nil {
			if err := v.form.options.Prepare(txCtx, record); err != nil {
				return err
			}
		}
		if err := v.checkIdentity(txCtx, record, identity); err != nil {
			return err
		}
		store := *v.form.model.store
		var expected genericModelRow
		capture := func(ctx context.Context, event orm.SaveEvent) error {
			if err := v.checkIdentity(ctx, event.Record, identity); err != nil {
				return err
			}
			// Read actual stored data before application after-hooks. Trigger
			// normalization is part of the row; later same-transaction drift is not.
			var err error
			expected, err = v.load(ctx, query)
			return err
		}
		store.AfterSave = append([]orm.SaveReceiver{capture}, store.AfterSave...)
		err = store.Save(txCtx, model, orm.SaveOptions{
			ForceUpdate: true, UpdateFields: v.updateFields,
			Prepare: func(ctx context.Context, current models.Record) error {
				if err := v.checkIdentity(ctx, current, identity); err != nil {
					return err
				}
				return models.FullClean(ctx, current, models.CleanOptions{}, &store)
			},
			Guard: func(ctx context.Context, current models.Record) error {
				if err := v.checkIdentity(ctx, current, identity); err != nil {
					return err
				}
				proposed, err := v.form.snapshot(current)
				if err != nil {
					return err
				}
				if err := v.writeGrant(ctx, proposed); err != nil {
					return err
				}
				after, err := v.form.snapshot(current)
				if err != nil || !reflect.DeepEqual(after.values, proposed.values) {
					return ErrUnavailable
				}
				if err := v.checkIdentity(ctx, current, identity); err != nil {
					return err
				}
				// A row lock cannot stop a hook from changing that same row on
				// this transaction. Fence those effects before the UPDATE too.
				return v.matchStored(ctx, query, before)
			},
		})
		if err != nil {
			return err
		}
		if expected.values == nil || v.checkIdentity(txCtx, record, identity) != nil {
			return ErrUnavailable
		}
		if err := v.matchStored(txCtx, query, expected); err != nil {
			return err
		}
		if err := v.writeGrant(txCtx, expected); err != nil {
			return err
		}
		locationID, err := v.form.identity(expected)
		if err != nil || !reflect.DeepEqual(locationID, identity) {
			return ErrUnavailable
		}
		location, err := v.form.options.SuccessURL(txCtx, locationID)
		if err != nil || !genericContextOK(txCtx) {
			return ErrUnavailable
		}
		location, absolute, err := genericRedirectTarget(location, "")
		if err != nil || absolute {
			return ErrUnavailable
		}
		if err := v.authorize(txCall); err != nil {
			return err
		}
		if err := v.writeGrant(txCtx, expected); err != nil {
			return err
		}
		if err := v.checkIdentity(txCtx, record, identity); err != nil {
			return err
		}
		// All application callbacks have completed. Never run another grant,
		// URL or template hook after this exact scoped stored-state fence.
		if err := v.matchStored(txCtx, query, expected); err != nil {
			return err
		}
		if !genericContextOK(txCtx) {
			return ErrUnavailable
		}
		response = Response{Status: http.StatusSeeOther, Headers: http.Header{"Location": {location}}}
		prepared = true
		return nil
	})
	if committed {
		if err != nil || !prepared {
			return Text(503, "Update committed, but post-commit processing failed. Do not resubmit.\n"), nil
		}
		return response, nil
	}
	response = Response{}
	if invalid != nil && err == invalid {
		return invalid.response, nil // Exact identity also proves clean rollback.
	}
	if prepared {
		if failure, ok := err.(*db.Error); ok && failure != nil && failure.Cause == nil {
			switch failure.Code {
			case db.UniqueViolation, db.ForeignKeyViolation, db.CheckViolation, db.NotNullViolation:
				return genericStatusResponse(422), nil
			case db.SerializationFailure, db.Deadlock:
				return genericStatusResponse(503), nil
			}
		}
		return Text(503, "Update outcome is unknown. Reconcile before retrying.\n"), nil
	}
	if err == orm.ErrNotUpdated {
		return genericStatusResponse(409), nil
	}
	if failure, ok := err.(*db.Error); ok && failure != nil && failure.Cause == nil {
		switch failure.Code {
		case db.UniqueViolation, db.ForeignKeyViolation, db.CheckViolation, db.NotNullViolation:
			return genericStatusResponse(422), nil
		}
	}
	if models.IsValidationOnly(err) {
		return genericStatusResponse(422), nil
	}
	return response, err
}
