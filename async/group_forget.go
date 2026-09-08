package async

import (
	"context"
	"math"
	"reflect"
)

// ForgetGroupPayload is the pure, bounded transition used by workflow payload
// adapters. It accepts only terminal flat groups and returns a detached graph.
// Adapters must separately enforce exact identity, scope and expected revision
// atomically. It grants no caller authority and does not release task pins.
func ForgetGroupPayload(graph Graph) (Graph, error) {
	graph, err := groupRecord(graph, graph.ID)
	if err != nil {
		return Graph{}, err
	}
	if graph.Kind != "group" {
		return Graph{}, ErrInvalid
	}
	if !graph.State.Terminal() {
		return Graph{}, ErrPinned
	}
	if graph.PayloadForgotten {
		return graph, nil
	}
	if graph.Revision == math.MaxUint64 {
		return Graph{}, ErrConflict
	}
	graph.PayloadForgotten = true
	graph.Output = nil
	for id, member := range graph.Members {
		member.Output = nil
		graph.Members[id] = member
	}
	graph.Revision++
	if !withinGraphBudget(graph, nil, MaxWorkflowDurableBytes) {
		return Graph{}, ErrUnavailable
	}
	return graph, nil
}

// Forget releases a terminal flat group's child output/progress and graph
// output copies. States, failures, identities, coordination pins and replay
// tombstones remain. It never waits for tasks, drains intents or revokes work.
//
// The stores are independent: errors may follow partial or complete deletions.
// Retrying the same group is safe, but is not an all-or-nothing transaction or
// proof that this invocation performed a prior deletion. Nil confirms all
// required replies, including an already forgotten/absent child observation.
// Other canvas kinds and providers without WorkflowPayloadStore are refused
// before any child mutation. After success, joins and iteration return
// ErrResultExpired; Snapshot and status methods retain terminal metadata.
func (g *GroupResult) Forget(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	op, err := g.operation(ctx)
	if err != nil {
		return err
	}
	graph, err := op.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := op.authorize(ctx, "forget", graph.Scope); err != nil {
		return err
	}
	if graph.Kind != "group" {
		return ErrInvalid
	}
	if !graph.State.Terminal() {
		return ErrPinned
	}
	store, ok := op.client.config.Workflows.(WorkflowPayloadStore)
	if !ok || store == nil {
		return ErrUnavailable
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return ErrUnavailable
		}
	}
	// Refuse known transition/encoded-size failures before deleting children.
	// Adding an explicit marker still consumes wire space in a thin graph.
	if _, err := ForgetGroupPayload(graph); err != nil {
		return err
	}
	var layout string
	if err := matchGroupLayout(&layout, graph); err != nil {
		return err
	}
	// Preflight every affected identity and grant before the first write.
	// Records are processed individually, not retained as an unbounded batch.
	for _, child := range graph.Children {
		if _, err := op.forgetChild(ctx, graph, child); err != nil {
			return err
		}
		if err := op.authorizeID(ctx, "forget", graph.Scope, child.ID); err != nil {
			return err
		}
	}
	for _, child := range graph.Children {
		if err := op.authorize(ctx, "read", graph.Scope); err != nil {
			return err
		}
		if err := op.authorize(ctx, "forget", graph.Scope); err != nil {
			return err
		}
		absent, err := op.forgetChild(ctx, graph, child)
		if err != nil {
			return err
		}
		if err := op.authorizeID(ctx, "forget", graph.Scope, child.ID); err != nil {
			return err
		}
		if absent {
			continue
		}
		if err := groupContextError(ctx); err != nil {
			return err
		}
		err = groupMutationError(op.client.config.Results.Forget(ctx, child.ID))
		if err != nil && err != ErrNotFound {
			return err
		}
	}
	// The graph is released only after child confirmations. Freeze this fresh
	// observation before the final grants; its revision guards the graph CAS.
	fresh, err := op.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := matchGroupLayout(&layout, fresh); err != nil {
		return err
	}
	if fresh.State != graph.State {
		return ErrUnavailable
	}
	for _, child := range graph.Children {
		if fresh.Members[child.ID].State != graph.Members[child.ID].State {
			return ErrUnavailable
		}
		if err := op.authorizeID(ctx, "read", graph.Scope, child.ID); err != nil {
			return err
		}
		if err := op.authorizeID(ctx, "forget", graph.Scope, child.ID); err != nil {
			return err
		}
	}
	if err := op.authorize(ctx, "forget", graph.Scope); err != nil {
		return err
	}
	if err := groupContextError(ctx); err != nil {
		return err
	}
	// No context callback after a confirmed reply: cancellation cannot turn a
	// completed final CAS into a claim that the payload is still retained.
	return groupMutationError(store.ForgetGraphPayload(ctx, op.id, graph.Scope, fresh.Revision))
}

// forgetChild verifies the exact graph-owned identity against current result
// metadata. An exact missing reply can mean legitimate retention cleanup; its
// authority still comes from the already validated graph plus child grants.
func (op groupOperation) forgetChild(ctx context.Context, graph Graph, child Envelope) (bool, error) {
	if err := groupContextError(ctx); err != nil {
		return false, err
	}
	record, err := op.client.config.Results.Lookup(ctx, child.ID)
	absent := err == ErrNotFound
	if err != nil && !absent {
		if ctxErr := groupContextError(ctx); ctxErr != nil {
			return false, ctxErr
		}
		return false, resultReadError(err)
	}
	if !absent {
		record, err = resultRecord(record, child.ID)
		if err != nil {
			return false, err
		}
		e := record.Envelope
		if e.Scope != graph.Scope || e.WorkflowID != graph.ID || record.Digest != child.Digest() || record.State != graph.Members[child.ID].State {
			return false, ErrUnavailable
		}
	}
	if ctxErr := groupContextError(ctx); ctxErr != nil {
		return false, ctxErr
	}
	if err := op.authorizeID(ctx, "read", graph.Scope, child.ID); err != nil {
		return false, err
	}
	if !absent && (record.Pinned || !record.State.Terminal()) {
		return false, ErrPinned
	}
	return absent, nil
}

func (op groupOperation) authorizeID(ctx context.Context, action, scope, id string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if ctxErr := groupContextError(ctx); ctxErr != nil {
			err = ctxErr
		}
	}()
	if err := groupContextError(ctx); err != nil {
		return err
	}
	return op.client.authorize(ctx, action, scope, id)
}

func groupContextError(ctx context.Context) error {
	switch err := publishContextError(ctx); err {
	case nil, ErrInvalid, ErrUnavailable, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}

func groupMutationError(err error) error {
	switch err {
	case nil, ErrNotFound, ErrPinned, ErrDenied, ErrCanceled, ErrBusy, ErrConflict, ErrLeaseLost, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}
