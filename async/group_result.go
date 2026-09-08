package async

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

type groupOperation struct {
	client *Client
	id     string
}

func (g *GroupResult) operation(ctx context.Context) (groupOperation, error) {
	if ctx == nil || g == nil || g.client == nil || !idPattern.MatchString(g.ID) {
		return groupOperation{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return groupOperation{}, err
	}
	if g.client.config.Workflows == nil {
		return groupOperation{}, ErrUnavailable
	}
	// Client has immutable private configuration and no mutex. Copy the value
	// so replacing the public client object cannot swap ports or grants midway
	// through a command/wait. Provider internals remain the provider's contract.
	client := *g.client
	return groupOperation{client: &client, id: g.ID}, nil
}

func (op groupOperation) authorize(ctx context.Context, action, scope string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return op.client.authorize(ctx, action, scope, op.id)
}

func (op groupOperation) snapshot(ctx context.Context) (out Graph, err error) {
	defer func() {
		if recover() != nil {
			out, err = Graph{}, ErrUnavailable
		}
		if ctx.Err() != nil {
			out, err = Graph{}, ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return Graph{}, err
	}
	graph, err := op.client.config.Workflows.ReadGraph(ctx, op.id)
	if err != nil {
		return Graph{}, resultReadError(err)
	}
	if ctx.Err() != nil {
		return Graph{}, ctx.Err()
	}
	graph, err = groupRecord(graph, op.id)
	if err != nil {
		return Graph{}, err
	}
	if err := op.authorize(ctx, "read", graph.Scope); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

// Snapshot returns a bounded, detached observation of exactly this workflow.
// Invalid/provider-error/canceled observations expose no partial graph.
func (g *GroupResult) Snapshot(ctx context.Context) (Graph, error) {
	op, err := g.operation(ctx)
	if err != nil {
		return Graph{}, err
	}
	return op.snapshot(ctx)
}

// Revoke requests cancellation for the identity authorized at call entry.
// A provider error can follow an applied request; it never proves no change.
func (g *GroupResult) Revoke(ctx context.Context) (err error) {
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
	if err := op.authorize(ctx, "revoke", graph.Scope); err != nil {
		return err
	}
	err = op.client.config.Workflows.CancelGraph(ctx, op.id, graph.Scope, op.client.advance)
	// Preserve confirmed replies even if the caller cancels afterward. Exact
	// sentinels retain meaning; wrapped/joined errors are ambiguous failures.
	switch err {
	case nil, ErrNotFound, ErrPinned, ErrDenied, ErrCanceled, ErrBusy, ErrConflict, ErrLeaseLost, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}

// Join waits for one frozen workflow and propagates its terminal failure.
// It never treats an absent retained payload as a successful nil result.
func (g *GroupResult) Join(ctx context.Context) (out []json.RawMessage, err error) {
	defer func() {
		if ctx != nil && ctx.Err() != nil {
			out, err = nil, ctx.Err()
		}
	}()
	op, err := g.operation(ctx)
	if err != nil {
		return nil, err
	}
	if ctx.Value(workerContextKey{}) != nil {
		return nil, ErrWorkerJoin
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var layout string
	for {
		graph, err := op.snapshot(ctx)
		if err != nil {
			return nil, err
		}
		if err := matchGroupLayout(&layout, graph); err != nil {
			return nil, err
		}
		if graph.State.Terminal() {
			return joinedGroupOutput(graph)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func joinedGroupOutput(graph Graph) ([]json.RawMessage, error) {
	if graph.Failure != nil {
		return nil, *graph.Failure
	}
	if graph.State != Succeeded {
		return nil, Failure{Code: string(graph.State), Message: "Workflow did not succeed"}
	}
	if len(graph.Output) == 0 {
		return nil, ErrResultExpired
	}
	count := -1
	if graph.Kind == "group" {
		count = len(graph.Children)
	}
	if graph.Kind == "dag" {
		for _, node := range graph.Plan {
			if node.ID == graph.RootNode && node.Kind == "collect" {
				count = len(node.Dependencies)
			}
		}
	}
	if count < 0 {
		return []json.RawMessage{graph.Output}, nil
	}
	if raw := bytes.TrimSpace(graph.Output); len(raw) == 0 || raw[0] != '[' {
		return nil, ErrUnavailable
	}
	var outputs []json.RawMessage
	if json.Unmarshal(graph.Output, &outputs) != nil || len(outputs) != count {
		return nil, ErrUnavailable
	}
	return outputs, nil
}

// Execution state and bound child arguments can advance, but a wait may not
// silently move to another scope, member set or compiled canvas topology.
func matchGroupLayout(expected *string, graph Graph) error {
	type node struct {
		ID, Kind              string
		Dependencies, WaitFor []string
	}
	layout := struct {
		Scope, Kind, Root, Callback string
		Children                    []string
		Nodes                       []node
	}{Scope: graph.Scope, Kind: graph.Kind, Root: graph.RootNode, Callback: graph.CallbackID}
	for _, child := range graph.Children {
		layout.Children = append(layout.Children, child.ID)
	}
	for _, item := range graph.Plan {
		layout.Nodes = append(layout.Nodes, node{item.ID, item.Kind, item.Dependencies, item.WaitFor})
	}
	encoded, err := json.Marshal(layout)
	if err != nil {
		return ErrUnavailable
	}
	if *expected == "" {
		*expected = string(encoded)
	} else if *expected != string(encoded) {
		return ErrUnavailable
	}
	return nil
}
