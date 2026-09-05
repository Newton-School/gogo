package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"sync/atomic"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
)

func sameListObject(object Object, expected listEditRowToken) error {
	current, err := listRowToken(object)
	if err != nil || current != expected {
		return ErrConflict
	}
	return nil
}

func (s *Site) currentListScope(ctx context.Context, p auth.Principal, options ModelAdmin) (ScopedStore, error) {
	store, err := s.config.Store.Scope(ctx, p, s.config.Name, options.Schema)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("admin: list scope unavailable")
	}
	return store, ctx.Err()
}

// All callbacks are read-only except the existing explicit save hooks. Each
// callback stage is followed by a full batch fence, not only a check of its
// current row: a later callback cannot silently replace an earlier sibling.
func checkListBatch(ctx context.Context, store ScopedStore, expected []listEditRowToken) error {
	for _, row := range expected {
		current, err := store.Get(ctx, row.ID, true)
		if err != nil {
			return err
		}
		if err := sameListObject(current, row); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *Site) saveListEdit(r *http.Request, p auth.Principal, options ModelAdmin, transactionStore ScopedStore, manifest listEditToken) (*listEditState, error) {
	var state *listEditState
	var witnessed atomic.Bool
	var bodyFailure error
	bodyStarted := false
	run := transactionStore.Atomic
	if observed, ok := transactionStore.(ObservedAtomicStore); ok {
		run = func(ctx context.Context, fn func(context.Context) error) error {
			return observed.AtomicObserved(ctx, fn, func() { witnessed.Store(true) })
		}
	}
	err := run(r.Context(), func(ctx context.Context) (bodyErr error) {
		bodyStarted = true
		defer func() {
			if recover() != nil {
				bodyErr = errors.New("admin: list mutation callback failed")
			}
			bodyFailure = bodyErr
		}()
		store, err := s.currentListScope(ctx, p, options)
		if err != nil {
			return err
		}
		// Lock by stable object key, regardless of client ordering, to minimize
		// opposite-order batch deadlocks. A backend deadlock still rolls back;
		// no part of this operation is implicitly replayed.
		order := make([]int, len(manifest.Rows))
		for index := range order {
			order[index] = index
		}
		sort.Slice(order, func(i, j int) bool { return manifest.Rows[order[i]].ID < manifest.Rows[order[j]].ID })
		objects := make([]Object, len(order))
		for _, index := range order {
			object, err := store.Get(ctx, manifest.Rows[index].ID, true)
			if err != nil {
				return err
			}
			if err := sameListObject(object, manifest.Rows[index]); err != nil {
				return err
			}
			objects[index] = object
		}
		state, err = s.bindListEdit(ctx, p, options, objects, r.PostForm)
		if err != nil {
			return err
		}
		// Validation may invoke model/checker callbacks. Recompute trusted row
		// scope and check every original DB snapshot before the first write.
		store, err = s.currentListScope(ctx, p, options)
		if err != nil {
			return err
		}
		if err := checkListBatch(ctx, store, manifest.Rows); err != nil {
			return err
		}
		if !state.valid {
			return errInvalidForm
		}
		expected := slices.Clone(manifest.Rows)
		changed := make([]bool, len(objects))
		for index, object := range state.objects {
			if !state.writable[index] || !state.forms[index].HasChanged() {
				continue
			}
			if err := s.allowed(ctx, p, "change", options, object); err != nil {
				return err
			}
			if err := state.checkProposals(); err != nil {
				return err
			}
			store, err = s.currentListScope(ctx, p, options)
			if err != nil {
				return err
			}
			if err := checkListBatch(ctx, store, expected); err != nil {
				return err
			}
			beforeObject, err := store.Get(ctx, object.ID, true)
			if err != nil {
				return err
			}
			before, err := snapshot(options, beforeObject)
			if err != nil {
				return err
			}
			if err := state.checkProposals(); err != nil {
				return err
			}
			var saved Object
			if options.SaveModel != nil {
				saved, err = options.SaveModel(ctx, store, object)
			} else {
				saved, err = store.Save(ctx, object)
			}
			if err != nil {
				return err
			}
			if saved.ID != manifest.Rows[index].ID || saved.Record == nil || saved.Record.Schema().Key() != options.Schema.Key() {
				return auth.ErrPermissionDenied
			}
			if options.SaveRelated != nil {
				if err := options.SaveRelated(ctx, store, saved, r); err != nil {
					return err
				}
			}
			saved, err = store.Get(ctx, saved.ID, true)
			if err != nil {
				return err
			}
			expected[index], err = listRowToken(saved)
			if err != nil {
				return err
			}
			state.objects[index], changed[index] = saved, true
			state.proposed[index] = expected[index]
			after, err := snapshot(options, saved)
			if err != nil {
				return err
			}
			if err := store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ObjectID: saved.ID, ObjectLabel: saved.Label, Action: "change", Changes: diff(before, after), At: time.Now().UTC()}); err != nil {
				return err
			}
			state.saved++
		}
		// First run ALL final view/change callbacks on the current scoped
		// records. Then resolve scope once more and compare ALL saved and
		// untouched rows after every save/related/audit/policy callback ended.
		store, err = s.currentListScope(ctx, p, options)
		if err != nil {
			return err
		}
		for index, row := range expected {
			current, err := store.Get(ctx, row.ID, true)
			if err != nil {
				return err
			}
			readonly, writable, err := s.listEditableFields(ctx, p, options, current)
			if err != nil {
				return err
			}
			if changed[index] {
				if !writable {
					return auth.ErrPermissionDenied
				}
				for _, name := range state.forms[index].ChangedData() {
					if slices.Contains(readonly, name) {
						return auth.ErrPermissionDenied
					}
				}
			}
		}
		if err := state.checkProposals(); err != nil {
			return err
		}
		store, err = s.currentListScope(ctx, p, options)
		if err != nil {
			return err
		}
		return checkListBatch(ctx, store, expected)
	})
	if state == nil {
		state = &listEditState{}
	}
	state.outcome = "unchanged"
	if witnessed.Load() {
		if state.saved > 0 {
			state.outcome = "changed"
		}
	} else if bodyFailure != nil || !bodyStarted {
		// A foreign typed error from fn is not this transaction's outcome.
		// The trusted Store must roll back when its body returns an error.
	} else if err == nil {
		if _, observed := transactionStore.(ObservedAtomicStore); observed {
			state.outcome, err = "unknown", errListMutationUnknown
		} else if state.saved > 0 {
			state.outcome = "changed"
		}
	} else {
		var committed *db.CommittedCallbackError
		if errors.As(err, &committed) || db.IsCode(err, db.UnknownCommit) {
			state.outcome = "unknown"
		}
	}
	return state, err
}
