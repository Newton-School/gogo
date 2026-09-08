package db

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

type transactionKey struct{}
type commitCallback struct {
	run    func(context.Context) error
	robust bool
}
type transactionContext struct {
	alias        string
	tx           Transaction
	callbacks    []commitCallback
	parent       *transactionContext
	rollbackOnly bool
}
type AtomicOptions struct {
	TxOptions
	Durable bool
}

var savepointSequence atomic.Uint64

func current(ctx context.Context, alias string) *transactionContext {
	tx, _ := ctx.Value(transactionKey{}).(*transactionContext)
	for tx != nil {
		if tx.alias == alias {
			return tx
		}
		tx = tx.parent
	}
	return nil
}
func InTransaction(ctx context.Context, alias string) bool { return current(ctx, alias) != nil }
func ExecutorFor(ctx context.Context, backend Backend) Executor {
	// A routed handle owns admission and the exact ambient transaction witness.
	// Returning a raw same-alias transaction here would bypass that boundary.
	if routed, ok := backend.(*routeBackend); ok {
		return routed
	}
	if tx := current(ctx, backend.Alias()); tx != nil {
		return tx.tx
	}
	return backend
}
func MarkRollback(ctx context.Context, alias string) {
	if tx := current(ctx, alias); tx != nil {
		tx.rollbackOnly = true
	}
}
func OnCommit(ctx context.Context, alias string, callback func(context.Context) error, robust bool) error {
	if callback == nil {
		return errors.New("db: nil commit callback")
	}
	if tx := current(ctx, alias); tx != nil {
		tx.callbacks = append(tx.callbacks, commitCallback{run: callback, robust: robust})
		return nil
	}
	return callback(ctx)
}

// Atomic pins one transaction to an alias. Context must not be used concurrently:
// sql transactions and ordered callbacks represent one serial unit of work.
func Atomic(ctx context.Context, backend Backend, options AtomicOptions, fn func(context.Context) error) (err error) {
	if fn == nil {
		return errors.New("db: nil atomic callback")
	}
	if backend == nil {
		return errors.New("db: nil backend")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	parent := current(ctx, backend.Alias())
	if parent != nil && options.Durable {
		return errors.New("db: durable Atomic must be outermost")
	}
	// The ambient chain and the same-alias savepoint owner are different: in
	// A -> B -> A, the innermost A must retain B for routing and callbacks.
	ambient, _ := ctx.Value(transactionKey{}).(*transactionContext)
	state := &transactionContext{alias: backend.Alias(), parent: ambient}
	savepoint := ""
	if parent != nil {
		state.tx = parent.tx
		savepoint = fmt.Sprintf("gogo_sp_%d", savepointSequence.Add(1))
		if _, err = state.tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
			parent.rollbackOnly = true
			return err
		}
	} else {
		state.tx, err = backend.BeginTx(ctx, options.TxOptions)
		if err != nil {
			return err
		}
	}
	childCtx := context.WithValue(ctx, transactionKey{}, state)
	committed := false
	defer func() {
		panicValue := recover()
		if !committed && (panicValue != nil || err != nil || state.rollbackOnly) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			var cleanupErr error
			if savepoint != "" {
				_, cleanupErr = state.tx.Exec(cleanupCtx, "ROLLBACK TO SAVEPOINT "+savepoint)
				if cleanupErr == nil {
					_, cleanupErr = state.tx.Exec(cleanupCtx, "RELEASE SAVEPOINT "+savepoint)
				}
			} else {
				cleanupErr = state.tx.Rollback()
			}
			if cleanupErr != nil {
				if parent != nil {
					parent.rollbackOnly = true
				}
				err = errors.Join(err, cleanupErr)
			}
			if err == nil && state.rollbackOnly {
				err = errors.New("db: transaction marked for rollback")
			}
			if panicValue != nil {
				panic(panicValue)
			}
		}
		if committed && panicValue != nil {
			panic(panicValue)
		}
	}()
	if err = fn(childCtx); err != nil {
		return err
	}
	if state.rollbackOnly {
		return errors.New("db: transaction marked for rollback")
	}
	if savepoint != "" {
		if _, err = state.tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
			parent.rollbackOnly = true
			return err
		}
		parent.callbacks = append(parent.callbacks, state.callbacks...)
		return nil
	}
	if err = state.tx.Commit(); err != nil {
		return err
	}
	committed = true
	// Commit succeeded: callback errors cannot roll back this transaction.
	callbacks := state.callbacks
	state.callbacks = nil
	var callbackErrors []error
	for _, callback := range callbacks {
		if callbackErr := callback.run(ctx); callbackErr != nil {
			callbackErrors = append(callbackErrors, callbackErr)
			if !callback.robust {
				break
			}
		}
	}
	if len(callbackErrors) > 0 {
		return &CommittedCallbackError{Errors: callbackErrors}
	}
	return nil
}

// CommittedCallbackError reports effects that failed after a durable commit.
type CommittedCallbackError struct{ Errors []error }

func (e *CommittedCallbackError) Error() string {
	return "db: transaction committed but after-commit callback failed"
}
func (e *CommittedCallbackError) Unwrap() []error { return e.Errors }
