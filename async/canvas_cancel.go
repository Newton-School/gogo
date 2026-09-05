package async

import (
	"context"
	"errors"
	"time"
)

func graphUnpins(graph Graph) []Intent {
	var intents []Intent
	for _, child := range graph.Children {
		if _, done := graph.Members[child.ID]; done {
			intents = append(intents, Intent{ID: StableID(graph.ID, "unpin-"+child.ID), SourceID: graph.ID, Kind: "unpin", TargetID: child.ID})
		}
	}
	if graph.CallbackID != "" {
		if _, done := graph.Members[graph.CallbackID]; done {
			intents = append(intents, Intent{ID: StableID(graph.ID, "unpin-"+graph.CallbackID), SourceID: graph.ID, Kind: "unpin", TargetID: graph.CallbackID})
		}
	}
	return intents
}

func cancellationIntent(source string, envelope Envelope, dispatched bool) Intent {
	intent := Intent{ID: StableID(source, "cancel-"+envelope.ID), SourceID: source, Kind: "cancel", Envelope: &envelope}
	if !dispatched {
		intent.Kind = "fail"
		intent.State = Revoked
		intent.Failure = &Failure{Code: "REVOKED", Message: "Workflow task was canceled before dispatch"}
	}
	return intent
}

func (c *Client) advanceCanceled(graph Graph) (Graph, []Intent, error) {
	var intents []Intent
	finished := true
	for index, child := range graph.Children {
		if _, done := graph.Members[child.ID]; done {
			continue
		}
		finished = false
		dispatched := graph.Kind != "chain" || index <= graph.Next
		intents = append(intents, cancellationIntent(graph.ID, child, dispatched))
	}
	if graph.CallbackClaimed {
		if _, done := graph.Members[graph.CallbackID]; !done {
			finished = false
			if graph.CallbackEnvelope == nil {
				return graph, nil, ErrUnavailable
			}
			intents = append(intents, cancellationIntent(graph.ID, *graph.CallbackEnvelope, true))
		}
	}
	if finished {
		graph.State = Revoked
		graph.Output = nil
		graph.Failure = &Failure{Code: "REVOKED", Message: "Workflow cancellation was recorded"}
		intents = append(intents, graphUnpins(graph)...)
	}
	return graph, intents, nil
}

func (r *IntentRelay) cancelTask(ctx context.Context, intent Intent, lease time.Duration) error {
	if intent.Envelope == nil {
		return ErrInvalid
	}
	envelope := *intent.Envelope
	record, err := r.Client.config.Results.Lookup(ctx, envelope.ID)
	if errors.Is(err, ErrNotFound) {
		if err := r.Client.config.Results.Register(ctx, envelope, Queued); err != nil {
			return err
		}
		record, err = r.Client.config.Results.Lookup(ctx, envelope.ID)
	}
	if err != nil {
		return err
	}
	if record.State.Terminal() {
		return nil
	}
	if err := r.Client.config.Results.RequestCancel(ctx, envelope.ID, envelope.Scope); err != nil {
		return err
	}
	// Claim the authoritative attempt, not the graph's initial retries=0
	// envelope. A live running lease is never overwritten by cancellation.
	record, err = r.Client.config.Results.Lookup(ctx, envelope.ID)
	if err != nil {
		return err
	}
	claim, err := r.Client.config.Results.Claim(ctx, record.Envelope, r.ID, lease)
	if err != nil {
		return err
	}
	if !claim.Acquired {
		return nil
	}
	failure := &Failure{Code: "REVOKED", Message: "Workflow task cancellation was recorded"}
	return r.Client.config.Results.Transition(ctx, Transition{ID: envelope.ID, Fence: claim.Record.Fence, Owner: r.ID, State: Revoked, Failure: failure, Intents: CompletionIntents(record.Envelope, Revoked, nil, failure)})
}
