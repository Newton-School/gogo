package async

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type Canvas struct {
	Kind       string
	Signatures []Signature
	Callback   *Signature
}

func Chain(signatures ...Signature) Canvas {
	return Canvas{Kind: "chain", Signatures: cloneJSON(signatures)}
}
func Group(signatures ...Signature) Canvas {
	return Canvas{Kind: "group", Signatures: cloneJSON(signatures)}
}
func Chord(header Canvas, body Signature) (Canvas, error) {
	if header.Kind != "group" {
		return Canvas{}, ErrInvalid
	}
	return Canvas{Kind: "chord", Signatures: cloneJSON(header.Signatures), Callback: ptrSignature(body.Clone())}, nil
}
func ptrSignature(s Signature) *Signature { return &s }

func inputBindingType(d *definition, s Signature) (reflect.Type, error) {
	if s.ParentField == "" {
		return d.input, nil
	}
	typ := d.input
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, ErrInvalid
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = f.Name
		}
		if f.IsExported() && name == s.ParentField {
			return f.Type, nil
		}
	}
	return nil, ErrInvalid
}

func (c *Client) validateCanvas(canvas Canvas) error {
	if len(canvas.Signatures) > 1000 {
		return ErrInvalid
	}
	if canvas.Kind != "chain" && canvas.Kind != "group" && canvas.Kind != "chord" {
		return ErrInvalid
	}
	for i, s := range canvas.Signatures {
		d, err := c.config.Registry.lookup(s.Task, s.Version)
		if err != nil {
			return err
		}
		if err := d.validate(s.Args); err != nil {
			return err
		}
		if canvas.Kind == "chain" && i > 0 && !s.Immutable {
			prior, _ := c.config.Registry.lookup(canvas.Signatures[i-1].Task, canvas.Signatures[i-1].Version)
			target, err := inputBindingType(d, s)
			if err != nil || target != prior.output {
				return fmt.Errorf("%w: incompatible chain input", ErrInvalid)
			}
		}
	}
	if canvas.Kind == "chord" {
		if canvas.Callback == nil {
			return ErrInvalid
		}
		d, err := c.config.Registry.lookup(canvas.Callback.Task, canvas.Callback.Version)
		if err != nil {
			return err
		}
		if !canvas.Callback.Immutable {
			target, err := inputBindingType(d, *canvas.Callback)
			if err != nil || target.Kind() != reflect.Slice {
				return fmt.Errorf("%w: chord body requires slice input", ErrInvalid)
			}
			for _, s := range canvas.Signatures {
				source, _ := c.config.Registry.lookup(s.Task, s.Version)
				if source.output != target.Elem() {
					return fmt.Errorf("%w: incompatible chord output", ErrInvalid)
				}
			}
		}
	}
	return nil
}

func (c *Client) ApplyCanvas(ctx context.Context, canvas Canvas) (*GroupResult, error) {
	if c.config.Workflows == nil {
		return nil, ErrUnavailable
	}
	if err := c.validateCanvas(canvas); err != nil {
		return nil, err
	}
	id, err := c.config.NewID()
	if err != nil {
		return nil, err
	}
	graph := Graph{ID: id, Kind: canvas.Kind, Signatures: cloneJSON(canvas.Signatures), Callback: canvas.Callback, Members: map[string]Completion{}, State: Running, Revision: 1}
	for i, s := range canvas.Signatures {
		s = s.Set(WithID(StableID(id, fmt.Sprintf("child-%d", i))))
		e, err := c.prepare(s)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			graph.Scope = e.Scope
		} else if graph.Scope != e.Scope {
			return nil, ErrDenied
		}
		e.WorkflowID = id
		e.RootID = id
		e.ParentID = id
		if canvas.Kind == "chain" && i > 0 {
			e.ParentID = graph.Children[i-1].ID
		}
		graph.Children = append(graph.Children, e)
	}
	if canvas.Callback != nil {
		graph.CallbackID = StableID(id, "body")
		if len(graph.Children) == 0 {
			graph.Scope = canvas.Callback.Options.Scope
		}
		if canvas.Callback.Options.Scope != graph.Scope {
			return nil, ErrDenied
		}
	}
	if err := c.authorize(ctx, "enqueue", graph.Scope, id); err != nil {
		return nil, err
	}
	var intents []Intent
	if canvas.Kind == "chain" {
		if len(graph.Children) > 0 {
			intents = append(intents, dispatchIntent(graph.ID, graph.Children[0]))
		}
	} else {
		for _, e := range graph.Children {
			intents = append(intents, dispatchIntent(graph.ID, e))
		}
	}
	if len(graph.Children) == 0 {
		graph, intents, err = c.advance(graph)
		if err != nil {
			return nil, err
		}
	}
	if err := c.config.Workflows.CreateGraph(ctx, graph, intents); err != nil {
		return nil, err
	}
	return &GroupResult{ID: id, client: c}, nil
}
func dispatchIntent(source string, e Envelope) Intent {
	return Intent{ID: StableID(source, "dispatch-"+e.ID), SourceID: source, Kind: "publish", Envelope: &e}
}

func (c *Client) advance(graph Graph) (Graph, []Intent, error) {
	if graph.State.Terminal() {
		return graph, nil, nil
	}
	var intents []Intent
	finish := func(state State, output json.RawMessage) (Graph, []Intent, error) {
		graph.State = state
		graph.Output = output
		for id := range graph.Members {
			intents = append(intents, Intent{ID: StableID(graph.ID, "unpin-"+id), SourceID: graph.ID, Kind: "unpin", TargetID: id})
		}
		return graph, intents, nil
	}
	if graph.Kind == "chain" {
		if len(graph.Children) == 0 {
			return finish(Succeeded, json.RawMessage(`[]`))
		}
		for graph.Next < len(graph.Children) {
			completed, ok := graph.Members[graph.Children[graph.Next].ID]
			if !ok {
				return graph, nil, nil
			}
			if completed.State != Succeeded {
				graph.Failure = &Failure{Code: "CHAIN_FAILED", Message: "A chain task did not succeed"}
				return finish(Failed, nil)
			}
			graph.Next++
			if graph.Next == len(graph.Children) {
				return finish(Succeeded, completed.Output)
			}
			s, err := graph.Signatures[graph.Next].bind(completed.Output)
			if err != nil {
				graph.Failure = &Failure{Code: "CHAIN_INPUT", Message: "A chain successor could not accept the previous result"}
				return finish(Failed, nil)
			}
			d, err := c.config.Registry.lookup(s.Task, s.Version)
			if err != nil {
				return graph, nil, err
			}
			if err := d.validate(s.Args); err != nil {
				graph.Children[graph.Next].Args = s.Args
				intent := dispatchIntent(graph.ID, graph.Children[graph.Next])
				intent.Kind = "fail"
				return graph, []Intent{intent}, nil
			}
			graph.Children[graph.Next].Args = s.Args
			return graph, []Intent{dispatchIntent(graph.ID, graph.Children[graph.Next])}, nil
		}
	}
	if graph.CallbackClaimed {
		body, ok := graph.Members[graph.CallbackID]
		if !ok {
			return graph, nil, nil
		}
		if body.State != Succeeded {
			graph.Failure = body.Failure
		}
		return finish(body.State, body.Output)
	}
	outputs := make([]json.RawMessage, 0, len(graph.Children))
	failed := false
	for _, child := range graph.Children {
		member, ok := graph.Members[child.ID]
		if !ok {
			return graph, nil, nil
		}
		if member.State != Succeeded {
			failed = true
		}
		outputs = append(outputs, member.Output)
	}
	if failed {
		graph.Failure = &Failure{Code: "GROUP_FAILED", Message: "One or more workflow members did not succeed"}
		return finish(Failed, nil)
	}
	combined, err := json.Marshal(outputs)
	if err != nil {
		return graph, nil, err
	}
	if graph.Kind == "group" {
		return finish(Succeeded, combined)
	}
	if graph.Kind == "chord" {
		s, err := graph.Callback.bind(combined)
		if err != nil {
			graph.Failure = &Failure{Code: "CHORD_INPUT", Message: "The chord callback could not accept member results"}
			return finish(Failed, nil)
		}
		s = s.Set(WithID(graph.CallbackID))
		e, err := c.prepare(s)
		if err != nil {
			graph.Failure = &Failure{Code: "CHORD_INPUT", Message: "The chord callback could not accept member results"}
			return finish(Failed, nil)
		}
		e.WorkflowID = graph.ID
		e.RootID = graph.ID
		e.ParentID = graph.ID
		graph.CallbackClaimed = true
		return graph, []Intent{dispatchIntent(graph.ID, e)}, nil
	}
	return graph, nil, ErrInvalid
}

type GroupResult struct {
	ID     string
	client *Client
}

func RestoreGroup(c *Client, id string) *GroupResult { return &GroupResult{ID: id, client: c} }
func (g *GroupResult) Snapshot(ctx context.Context) (Graph, error) {
	if g.client.config.Workflows == nil {
		return Graph{}, ErrUnavailable
	}
	graph, err := g.client.config.Workflows.ReadGraph(ctx, g.ID)
	if err != nil {
		return graph, err
	}
	if err := g.client.authorize(ctx, "read", graph.Scope, g.ID); err != nil {
		return Graph{}, err
	}
	return graph, nil
}
func (g *GroupResult) Join(ctx context.Context) ([]json.RawMessage, error) {
	if ctx.Value(workerContextKey{}) != nil {
		return nil, ErrWorkerJoin
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		graph, err := g.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		if graph.State.Terminal() {
			if graph.Failure != nil {
				return nil, *graph.Failure
			}
			var outputs []json.RawMessage
			if graph.Kind == "chain" || graph.Kind == "chord" {
				return []json.RawMessage{graph.Output}, nil
			}
			if err := json.Unmarshal(graph.Output, &outputs); err != nil {
				return nil, err
			}
			return outputs, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// IntentRelay makes cross-store delivery explicit: target acceptance occurs
// before source acknowledgement. A crash can repeat the same target identity.
type IntentRelay struct {
	Client    *Client
	Sources   []IntentStore
	ID        string
	BatchSize int
	Lease     time.Duration
}

func (r *IntentRelay) Tick(ctx context.Context) error {
	if r.Client == nil || r.ID == "" {
		return ErrInvalid
	}
	limit := r.BatchSize
	if limit == 0 {
		limit = 100
	}
	lease := r.Lease
	if lease == 0 {
		lease = 30 * time.Second
	}
	for _, source := range r.Sources {
		intents, err := source.ListIntents(ctx, limit)
		if err != nil {
			return err
		}
		for _, pending := range intents {
			intent, err := source.ClaimIntent(ctx, pending.SourceID, pending.ID, r.ID, lease)
			if errors.Is(err, ErrBusy) {
				continue
			}
			if err != nil {
				return err
			}
			if intent.Delivered {
				continue
			}
			switch intent.Kind {
			case "fail":
				if intent.Envelope == nil {
					return ErrInvalid
				}
				e := *intent.Envelope
				e.ETA = time.Time{}
				if err = r.Client.config.Results.Register(ctx, e, Queued); err == nil {
					var claim Claim
					claim, err = r.Client.config.Results.Claim(ctx, e, r.ID, lease)
					if err == nil && claim.Acquired {
						failure := &Failure{Code: "CALLBACK_INPUT", Message: "Callback input validation failed"}
						err = r.Client.config.Results.Transition(ctx, Transition{ID: e.ID, Fence: claim.Record.Fence, Owner: r.ID, State: Failed, Failure: failure, Intents: CompletionIntents(e, Failed, nil, failure)})
					} else if err == nil && !claim.Duplicate {
						err = ErrBusy
					}
				}
			case "publish":
				if intent.Envelope == nil {
					return ErrInvalid
				}
				_, err = r.Client.publish(ctx, *intent.Envelope)
			case "completion":
				if intent.Completion == nil || r.Client.config.Workflows == nil {
					return ErrInvalid
				}
				err = r.Client.config.Workflows.RecordMember(ctx, intent.WorkflowID, *intent.Completion, r.Client.advance)
			case "unpin":
				err = r.Client.config.Results.ReleasePin(ctx, intent.TargetID)
			default:
				return ErrInvalid
			}
			if err != nil {
				return err
			}
			if err := source.MarkIntentDelivered(ctx, intent.SourceID, intent.ID, intent.Fence, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *IntentRelay) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type DelayedDispatcher struct {
	Client    *Client
	ID        string
	BatchSize int
	Lease     time.Duration
}

func (d *DelayedDispatcher) Tick(ctx context.Context) error {
	if d.Client == nil || d.Client.config.Schedules == nil || d.ID == "" {
		return ErrInvalid
	}
	limit := d.BatchSize
	if limit == 0 {
		limit = 100
	}
	lease := d.Lease
	if lease == 0 {
		lease = 30 * time.Second
	}
	items, err := d.Client.config.Schedules.LeaseDue(ctx, d.ID, limit, lease)
	if err != nil {
		return err
	}
	for _, item := range items {
		record, err := d.Client.config.Results.Lookup(ctx, item.Envelope.ID)
		if errors.Is(err, ErrNotFound) {
			if err := d.Client.config.Results.Register(ctx, item.Envelope, Scheduled); err != nil {
				return err
			}
			record, err = d.Client.config.Results.Lookup(ctx, item.Envelope.ID)
		}
		if err != nil {
			return err
		}
		terminal := record.CancelRequested || (!item.Envelope.ExpiresAt.IsZero() && !d.Client.config.Clock().Before(item.Envelope.ExpiresAt))
		if !record.State.Terminal() && terminal {
			claim, err := d.Client.config.Results.Claim(ctx, item.Envelope, d.ID, lease)
			if err != nil {
				return err
			}
			if !claim.Acquired && !claim.Duplicate {
				return ErrBusy
			}
			if claim.Acquired {
				state := Expired
				if record.CancelRequested {
					state = Revoked
				}
				failure := &Failure{Code: string(state), Message: "Task was not executed"}
				transition := Transition{ID: item.Envelope.ID, Fence: claim.Record.Fence, Owner: d.ID, State: state, Failure: failure, Intents: CompletionIntents(item.Envelope, state, nil, failure)}
				if err := d.Client.config.Results.Transition(ctx, transition); err != nil {
					return err
				}
			}
		} else if !record.State.Terminal() {
			if err := d.Client.config.Broker.Publish(ctx, item.Envelope); err != nil {
				return err
			}
		}
		if err := d.Client.config.Schedules.CommitFire(ctx, item); err != nil {
			return err
		}
	}
	return nil
}
