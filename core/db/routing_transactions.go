package db

import (
	"context"
	"errors"
	"sync/atomic"
)

type routingTransactionKey struct{}
type routingTransactionWitness struct {
	state       *routerState
	source      *routerSource
	transaction *transactionContext
	parent      *routingTransactionWitness
	active      atomic.Bool
}

// ambient refuses unmanaged transactions even if their aliases match. Legacy
// Atomic remains unchanged, including its independent A -> B -> A chain.
func (r *routerState) ambient(ctx context.Context) (*routingTransactionWitness, error) {
	transaction, _ := ctx.Value(transactionKey{}).(*transactionContext)
	witness, _ := ctx.Value(routingTransactionKey{}).(*routingTransactionWitness)
	first := witness
	for transaction != nil {
		if witness == nil || !witness.active.Load() || witness.state != r || witness.transaction != transaction || transaction.alias != witness.source.alias || first.source != witness.source {
			return nil, routingFailure(RouteRequest{}, "", "unmanaged or cross-alias transaction")
		}
		transaction, witness = transaction.parent, witness.parent
	}
	if witness != nil {
		return nil, routingFailure(RouteRequest{}, "", "expired transaction")
	}
	return first, nil
}

// Atomic opens only a selected primary and installs an actual transaction
// witness. Cross-alias or unmanaged ambient transactions are refused before
// provider callbacks. Nested witnessed same-alias calls use existing savepoints.
// It retains Atomic's callback/error/panic and commit semantics; it never retries.
func (b *RouteBinding) Atomic(ctx context.Context, options AtomicOptions, fn func(context.Context) error) error {
	if b == nil || b.backend == nil || fn == nil || b.backend.request.Operation == RouteRead {
		return routingFailure(RouteRequest{}, "", "transaction configuration")
	}
	backend := b.backend
	if backend.source.alias != backend.source.primary {
		return routingFailure(backend.request, backend.source.alias, "primary transaction required")
	}
	operation, parent, e := backend.operation(ctx, true)
	if e != nil {
		return e
	}
	if e := backend.capabilities.Require("transactions"); e != nil {
		return e
	}
	// Alias is immutable for Atomic's entire lifecycle; actual provider pools
	// remain owned by the application. Executor/transaction values are genuine.
	selected := &routingAtomicBackend{Backend: backend.source.backend, alias: backend.source.alias}
	return Atomic(operation, selected, options, func(txctx context.Context) error {
		transaction, _ := txctx.Value(transactionKey{}).(*transactionContext)
		witness := &routingTransactionWitness{state: backend.state, source: backend.source, transaction: transaction, parent: parent}
		witness.active.Store(true)
		defer witness.active.Store(false)
		return fn(context.WithValue(txctx, routingTransactionKey{}, witness))
	})
}

type routingAtomicBackend struct {
	Backend
	alias string
}

func (b *routingAtomicBackend) Alias() string { return b.alias }

func (b *routingAtomicBackend) BeginTx(ctx context.Context, options TxOptions) (Transaction, error) {
	transaction, err := b.Backend.BeginTx(ctx, options)
	if err == nil && !routingNil(transaction) {
		return transaction, nil
	}
	if err == nil {
		err = routingFailure(RouteRequest{Operation: RouteTransaction}, b.alias, "missing transaction")
	}
	if !routingNil(transaction) {
		cleanup := func() (failure error) {
			defer func() {
				if recover() != nil {
					failure = routingFailure(RouteRequest{Operation: RouteTransaction}, b.alias, "transaction cleanup")
				}
			}()
			return transaction.Rollback()
		}
		err = errors.Join(err, cleanup())
	}
	return nil, err
}

// Atomic resolves one explicit/default primary for a routed transaction. Every
// model operation inside it must still resolve and pass all its routing rules.
func (r *Router) Atomic(ctx context.Context, alias string, options AtomicOptions, fn func(context.Context, *RouteBinding) error) error {
	if fn == nil {
		return routingFailure(RouteRequest{}, "", "transaction configuration")
	}
	binding, e := r.Resolve(ctx, RouteRequest{Alias: alias, Operation: RouteTransaction, PrimaryRequired: true})
	if e != nil {
		return e
	}
	return binding.Atomic(binding.Context(), options, func(txctx context.Context) error { return fn(txctx, binding) })
}
