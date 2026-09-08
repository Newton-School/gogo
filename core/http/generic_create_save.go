package http

import (
	"context"
	"net/http"
	"reflect"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func (v *genericCreate) writeGrant(ctx context.Context, row genericModelRow, persisted bool) error {
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	record, err := v.policyRecord(row, persisted)
	if err != nil {
		return err
	}
	record.State().Persisted = persisted
	err = v.options.ValidateWrite(ctx, auth.FromContext(ctx), record)
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	// Neither callback can alter the other's proposed record or identity.
	record, err = v.policyRecord(row, persisted)
	if err != nil {
		return err
	}
	record.State().Persisted = persisted
	var identity map[string]any
	if persisted {
		identity, err = v.identity(row)
		if err != nil {
			return err
		}
	}
	err = v.model.policy.Authorize(ctx, auth.FromContext(ctx), "add", auth.Resource{App: v.model.schema.AppLabel, Model: v.model.schema.Name, ID: identity, Object: record})
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	return err
}

// save's private marker is the only proof of a successful outer Commit. A
// callback returning a CommittedCallbackError cannot manufacture that proof.
func (v *genericCreate) save(call *readViewCall, model models.Model, record models.Record, query orm.Query[*models.MapRecord]) (response Response, err error) {
	ctx := call.base.Context()
	committed, prepared := false, false
	defer func() {
		if recover() != nil {
			if committed {
				response, err = genericCreateOutcome("Creation committed, but post-commit processing failed. Do not resubmit."), nil
			} else {
				response, err = genericCreateOutcome("Creation outcome is unknown. Reconcile before retrying."), nil
			}
		}
	}()
	if db.InTransaction(ctx, v.model.store.Backend.Alias()) || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	err = db.Atomic(ctx, v.model.store.Backend, db.AtomicOptions{Durable: true}, func(txCtx context.Context) (txErr error) {
		// Callback panics inside the transaction become rollback-triggering safe
		// errors. Panics in Commit/cleanup remain an uncertain outer outcome.
		defer func() {
			if recover() != nil {
				txErr = ErrUnavailable
			}
		}()
		if err := db.OnCommit(txCtx, v.model.store.Backend.Alias(), func(context.Context) error { committed = true; return nil }, false); err != nil {
			return err
		}
		txCall := &readViewCall{base: call.base.WithContext(txCtx), routeParams: call.routeParams}
		if err := v.authorize(txCall); err != nil {
			return err
		}
		if v.options.Prepare != nil {
			if err := v.options.Prepare(txCtx, record); err != nil {
				return err
			}
		}
		if !genericContextOK(txCtx) || !v.matches(record) || record.State().Persisted {
			return ErrUnavailable
		}
		store := *v.model.store
		var expected genericModelRow
		// Capture the exact post-RETURNING record before any application after
		// hook can mutate its ID or values. This callback is request-local.
		capture := func(_ context.Context, event orm.SaveEvent) error {
			var err error
			expected, err = v.snapshot(event.Record)
			return err
		}
		store.AfterSave = append([]orm.SaveReceiver{capture}, store.AfterSave...)
		err := store.Save(txCtx, model, orm.SaveOptions{
			ForceInsert: true,
			Prepare: func(ctx context.Context, current models.Record) error {
				if !v.matches(current) {
					return ErrUnavailable
				}
				exclude := []string{}
				for _, field := range v.model.schema.Fields {
					if field.IsAuto() || field.DBDefault != "" {
						value, err := current.Get(field.Name)
						if err != nil {
							return ErrUnavailable
						}
						if models.IsEmptyValue(value) {
							exclude = append(exclude, field.Name)
						}
					}
				}
				return models.FullClean(ctx, current, models.CleanOptions{Exclude: exclude}, &store)
			},
			Guard: func(ctx context.Context, current models.Record) error {
				before, err := v.snapshot(current)
				if err != nil {
					return err
				}
				if err := v.writeGrant(ctx, before, false); err != nil {
					return err
				}
				after, err := v.snapshot(current)
				if err != nil || !v.matches(current) || !reflect.DeepEqual(before.values, after.values) {
					return ErrUnavailable
				}
				return nil
			},
		})
		if err != nil {
			return err
		}
		if expected.values == nil || !genericContextOK(txCtx) {
			return ErrUnavailable
		}
		if err := v.persisted(txCtx, query, expected); err != nil {
			return err
		}
		if err := v.writeGrant(txCtx, expected, true); err != nil {
			return err
		}
		identity, err := v.identity(expected)
		if err != nil {
			return err
		}
		location, err := v.options.SuccessURL(txCtx, identity)
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
		if err := v.writeGrant(txCtx, expected, true); err != nil {
			return err
		}
		// Last application callback has finished. Query the original frozen
		// scope and identity again; do not re-run user grants after this fence.
		if err := v.persisted(txCtx, query, expected); err != nil {
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
			return genericCreateOutcome("Creation committed, but post-commit processing failed. Do not resubmit."), nil
		}
		return response, nil // Late cancellation does not undo a known commit.
	}
	response = Response{}
	if prepared {
		if failure, ok := err.(*db.Error); ok && failure != nil && failure.Cause == nil {
			switch failure.Code {
			case db.UniqueViolation, db.ForeignKeyViolation, db.CheckViolation, db.NotNullViolation:
				return genericStatusResponse(422), nil
			case db.SerializationFailure, db.Deadlock:
				return genericStatusResponse(503), nil
			}
		}
		return genericCreateOutcome("Creation outcome is unknown. Reconcile before retrying."), nil
	}
	if models.IsValidationOnly(err) {
		return genericStatusResponse(422), nil
	}
	return response, err
}

func genericCreateOutcome(message string) Response { return Text(503, message+"\n") }
