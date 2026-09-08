package db

import (
	"context"
	"sync"
)

type routingConsistencyKey struct{ state *routerState }
type routingConsistency struct {
	mu       sync.Mutex
	families map[string]string
	dirty    map[string]bool
}

// WithConsistency starts a fresh request-owned routing context. Share it across
// that request's sequential operations; never reuse it for another request.
// Pins are monotonic, without TTL/replica-lag guesses. Reads admitted before a
// concurrent write's pin may finish stale; no concurrent linearizability is
// promised. Mutexes protect only bookkeeping, never callbacks or database I/O.
func (r *Router) WithConsistency(ctx context.Context) (context.Context, error) {
	if r == nil || r.state == nil {
		return nil, routingFailure(RouteRequest{}, "", "configuration")
	}
	state := r.state
	if e := routingContext(ctx); e != nil {
		return nil, e
	}
	return context.WithValue(ctx, routingConsistencyKey{state}, &routingConsistency{families: map[string]string{}, dirty: map[string]bool{}}), nil
}
func (r *routerState) consistency(ctx context.Context) *routingConsistency {
	value, _ := ctx.Value(routingConsistencyKey{r}).(*routingConsistency)
	return value
}
func (c *routingConsistency) primaryRequired(primary string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty[primary]
}
func (b *routeBackend) admit(ctx context.Context, write bool) (err error) {
	_, _, err = b.operation(ctx, write)
	return err
}

// routingOperationContext freezes only private routing/transaction observations.
// Other application context values remain opaque and cooperative. In particular,
// legacy Atomic and providers cannot obtain a different ambient transaction by
// consulting the caller's Value method again after admission.
type routingOperationContext struct {
	context.Context
	state       *routerState
	transaction *transactionContext
	witness     *routingTransactionWitness
	consistency *routingConsistency
}

func (c *routingOperationContext) Value(key any) any {
	switch k := key.(type) {
	case transactionKey:
		return c.transaction
	case routingTransactionKey:
		return c.witness
	case routingConsistencyKey:
		if k.state == c.state {
			return c.consistency
		}
	}
	return c.Context.Value(key)
}
func (c *routingOperationContext) Err() error { return routingContext(c.Context) }
func (r *routerState) snapshotContext(ctx context.Context) (operation context.Context, err error) {
	defer func() {
		if recover() != nil {
			operation, err = nil, routingFailure(RouteRequest{}, "", "context")
		}
	}()
	if routingNil(ctx) {
		return nil, routingFailure(RouteRequest{}, "", "context")
	}
	c := &routingOperationContext{Context: ctx, state: r}
	c.transaction, _ = ctx.Value(transactionKey{}).(*transactionContext)
	c.witness, _ = ctx.Value(routingTransactionKey{}).(*routingTransactionWitness)
	c.consistency = r.consistency(ctx)
	if e := routingContext(c); e != nil {
		return nil, e
	}
	return c, nil
}
func (b *routeBackend) operation(ctx context.Context, write bool) (operation context.Context, witness *routingTransactionWitness, err error) {
	defer func() {
		if recover() != nil {
			operation, witness, err = nil, nil, routingFailure(b.request, b.source.alias, "context")
		}
	}()
	ctx, err = b.state.snapshotContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	if b.state.consistency(ctx) != b.consistency {
		return nil, nil, routingFailure(b.request, b.source.alias, "operation context changed")
	}
	witness, err = b.state.ambient(ctx)
	if err != nil {
		return nil, nil, err
	}
	if witness != nil && witness.source != b.source {
		return nil, nil, routingFailure(b.request, b.source.alias, "transaction mismatch")
	}
	if write && b.source.alias != b.source.primary {
		return nil, nil, routingFailure(b.request, b.source.alias, "primary required")
	}
	if b.consistency == nil {
		return ctx, witness, nil
	}
	c := b.consistency
	c.mu.Lock()
	defer c.mu.Unlock()
	if b.source.alias != b.source.primary && c.dirty[b.source.primary] {
		return nil, nil, routingFailure(b.request, b.source.alias, "primary required")
	}
	if model := b.request.Model; model != "" {
		if primary := c.families[model]; primary != "" && primary != b.source.primary {
			return nil, nil, routingFailure(b.request, b.source.alias, "model family changed")
		}
		if c.families[model] == "" {
			if len(c.families) >= 1024 {
				return nil, nil, routingFailure(b.request, b.source.alias, "request model limit")
			}
			c.families[model] = b.source.primary
		}
	}
	if write {
		c.dirty[b.source.primary] = true
	}
	return ctx, witness, nil
}
