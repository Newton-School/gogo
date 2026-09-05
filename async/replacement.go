package async

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
)

const MaxReplacementDepth = 16

// ReplacementRequest yields the current task's logical result to a canvas.
// Return it as the handler error; do not synchronously wait for its children.
type ReplacementRequest struct{ Canvas Canvas }

func (*ReplacementRequest) Error() string { return "async: task yielded to replacement" }
func Replace(canvas Canvas) error         { return &ReplacementRequest{Canvas: cloneJSON(canvas)} }

func (w *Worker) prepareReplacement(ctx context.Context, original Envelope, definition *definition, canvas Canvas) (graph Graph, err error) {
	defer func() {
		if recover() != nil {
			graph, err = Graph{}, ErrInvalid
		}
	}()
	if w.Client == nil || w.Client.config.Workflows == nil {
		return Graph{}, ErrUnavailable
	}
	if original.ReplacementDepth >= MaxReplacementDepth {
		return Graph{}, ErrInvalid
	}
	canvas = cloneJSON(canvas)
	var inherit func(*Canvas, int) error
	inherit = func(node *Canvas, depth int) error {
		if depth > 32 || len(node.Signatures) > 1000 || len(node.Steps) > 1000 {
			return ErrInvalid
		}
		signatures := make([]*Signature, 0, len(node.Signatures)+1)
		for i := range node.Signatures {
			signatures = append(signatures, &node.Signatures[i])
		}
		if node.Callback != nil {
			signatures = append(signatures, node.Callback)
		}
		for _, signature := range signatures {
			if signature.Options.Scope != "" && signature.Options.Scope != original.Scope || signature.Options.Principal != "" && signature.Options.Principal != original.Principal {
				return ErrDenied
			}
			signature.Options.Scope, signature.Options.Principal = original.Scope, original.Principal
			if !original.ExpiresAt.IsZero() && (signature.Options.ExpiresAt.IsZero() || original.ExpiresAt.Before(signature.Options.ExpiresAt)) {
				signature.Options.ExpiresAt = original.ExpiresAt
			}
		}
		for i := range node.Steps {
			if err := inherit(&node.Steps[i], depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := inherit(&canvas, 0); err != nil {
		return Graph{}, err
	}
	id := StableID(original.ID, "replacement")
	compiler := canvasCompiler{client: w.Client, hasScope: true, graph: Graph{ID: id, Kind: "dag", Scope: original.Scope, State: Running, Members: map[string]Completion{}, Revision: 1, OriginTaskID: original.ID, OriginDigest: original.Digest()}}
	root, err := compiler.compile(canvas, nil, 0)
	if err != nil {
		return Graph{}, err
	}
	if root.typ != definition.output && !(root.empty && definition.output.Kind() == reflect.Slice) {
		return Graph{}, ErrInvalid
	}
	compiler.graph.RootNode = root.id
	for i := range compiler.graph.Children {
		child := &compiler.graph.Children[i]
		child.ReplacementDepth = original.ReplacementDepth + 1
		child.RootID = original.RootID
		if child.RootID == "" {
			child.RootID = original.ID
		}
		if child.ParentID == id {
			child.ParentID = original.ID
		}
		if err := child.Validate(); err != nil {
			return Graph{}, err
		}
	}
	if err := w.Client.authorize(ctx, "enqueue", original.Scope, id); err != nil {
		return Graph{}, err
	}
	// The same payload is stored as a result-local dispatch intent. Reject
	// oversized graphs before yielding, not later in the recovery relay.
	b, err := json.Marshal(compiler.graph)
	if err != nil || len(b) > MaxWorkflowInitialBytes {
		return Graph{}, ErrInvalid
	}
	return compiler.graph, nil
}

func (w *Worker) yieldReplacement(ctx context.Context, original Envelope, claim Record, graph Graph) error {
	intent := Intent{ID: StableID(original.ID, "replacement-dispatch"), SourceID: original.ID, Kind: "replace", WorkflowID: graph.ID, Graph: &graph}
	return w.Results.Transition(ctx, Transition{ID: original.ID, Fence: claim.Fence, Owner: w.ID, State: Running, ReplacementID: graph.ID, Intents: []Intent{intent}})
}

// ReplacementCancellationIntents must be committed with the cancellation flag.
// It remains pending when cancellation precedes replacement graph creation.
func ReplacementCancellationIntents(record Record) []Intent {
	if !record.CancelRequested || record.ReplacementID == "" || record.State.Terminal() {
		return nil
	}
	return []Intent{{ID: StableID(record.Envelope.ID, "replacement-cancel"), SourceID: record.Envelope.ID, Kind: "cancel_workflow", WorkflowID: record.ReplacementID, Envelope: &record.Envelope}}
}

func replacementCompletionIntents(graph Graph) []Intent {
	if graph.OriginTaskID == "" || !graph.State.Terminal() {
		return nil
	}
	return []Intent{{ID: StableID(graph.ID, "replacement-completion"), SourceID: graph.ID, Kind: "replacement_complete", WorkflowID: graph.ID, TargetID: graph.OriginTaskID, Completion: &Completion{ID: graph.OriginTaskID, State: graph.State, Output: graph.Output, Failure: graph.Failure}}}
}

func (r *IntentRelay) createReplacement(ctx context.Context, intent Intent) error {
	if intent.Graph == nil || r.Client.config.Workflows == nil || intent.SourceID != intent.Graph.OriginTaskID || intent.WorkflowID != intent.Graph.ID {
		return ErrInvalid
	}
	original, err := r.Client.config.Results.Lookup(ctx, intent.Graph.OriginTaskID)
	if err != nil {
		return err
	}
	if original.ReplacementID != intent.Graph.ID || original.Digest != intent.Graph.OriginDigest {
		return ErrConflict
	}
	initial := cloneJSON(*intent.Graph)
	initial.CancelRequested = original.CancelRequested
	graph, intents, err := r.Client.advance(initial)
	if err != nil {
		return err
	}
	err = r.Client.config.Workflows.CreateGraph(ctx, graph, intents)
	if errors.Is(err, ErrConflict) {
		current, lookupErr := r.Client.config.Workflows.ReadGraph(ctx, graph.ID)
		if lookupErr != nil {
			return lookupErr
		}
		if current.OriginTaskID != graph.OriginTaskID || current.OriginDigest != graph.OriginDigest || current.Scope != graph.Scope {
			return ErrConflict
		}
		return nil
	}
	return err
}

func (r *IntentRelay) completeReplacement(ctx context.Context, intent Intent) error {
	store, ok := r.Client.config.Results.(ReplacementStore)
	if !ok || intent.Completion == nil || intent.TargetID != intent.Completion.ID {
		return ErrInvalid
	}
	var completed Record
	var terminal Transition
	applied, err := store.ResolveReplacement(ctx, intent.TargetID, intent.WorkflowID, func(record Record) (Transition, error) {
		completion := *intent.Completion
		if record.CancelRequested {
			completion.State, completion.Output = Revoked, nil
			completion.Failure = &Failure{Code: "REVOKED", Message: "Replacement cancellation was recorded"}
		}
		definition, err := r.Client.config.Registry.lookup(record.Envelope.Task, record.Envelope.Version)
		if err != nil {
			return Transition{}, err
		}
		if completion.State == Succeeded && validateReplacementOutput(definition, completion.Output) != nil {
			completion.State, completion.Output = Failed, nil
			completion.Failure = &Failure{Code: "REPLACEMENT_OUTPUT", Message: "Replacement output did not satisfy the original task result schema"}
		}
		transition := Transition{ID: record.Envelope.ID, State: completion.State, Output: completion.Output, Failure: completion.Failure}
		transition.Intents = CompletionIntents(record.Envelope, transition.State, transition.Output, transition.Failure)
		worker := &Worker{Registry: r.Client.config.Registry, Clock: r.Client.config.Clock}
		linked, err := worker.linkedIntents(record.Envelope, transition)
		if err != nil {
			return Transition{}, err
		}
		transition.Intents = append(transition.Intents, linked...)
		completed, terminal = record, transition
		return transition, nil
	})
	if err != nil || !applied {
		return err
	}
	definition, err := r.Client.config.Registry.lookup(completed.Envelope.Task, completed.Envelope.Version)
	if err != nil {
		return err
	}
	worker := &Worker{Registry: r.Client.config.Registry, Clock: r.Client.config.Clock, OnError: r.OnError, Events: r.Events}
	e := completed.Envelope
	tc := TaskContext{ID: e.ID, Retries: e.Retries, DeliveryCount: completed.DeliveryCount, Fence: completed.Fence, WorkflowID: e.WorkflowID, ParentID: e.ParentID, RootID: e.RootID, Scope: e.Scope, Principal: e.Principal, Headers: cloneJSON(e.Headers), Stamps: cloneJSON(e.Stamps), ReplacementDepth: e.ReplacementDepth}
	worker.event(ctx, e, "task_terminal", terminal.State)
	if terminal.State == Succeeded && definition.options.Hooks.OnSuccess != nil {
		worker.observe(func() error { return definition.options.Hooks.OnSuccess(ctx, tc, terminal.Output) })
	}
	if terminal.State != Succeeded && definition.options.Hooks.OnFailure != nil {
		failure := terminal.Failure
		if failure == nil {
			failure = &Failure{Code: string(terminal.State), Message: "Replacement did not succeed"}
		}
		worker.observe(func() error { return definition.options.Hooks.OnFailure(ctx, tc, *failure) })
	}
	if definition.options.Hooks.AfterReturn != nil {
		worker.observe(func() error { return definition.options.Hooks.AfterReturn(ctx, tc, terminal.State) })
	}
	return nil
}

func validateReplacementOutput(definition *definition, output json.RawMessage) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
	}()
	return definition.validateOutput(output)
}
