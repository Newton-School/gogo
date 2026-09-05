package async

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Canvas wraps one signature for composition with nested chains and groups.
func (s Signature) Canvas() Canvas {
	return Canvas{Kind: "task", Signatures: []Signature{s.Clone()}}
}

// Then passes this canvas's logical output to the next canvas. If next is a
// parallel group, each non-immutable child receives that output. Group outputs
// remain ordered by declaration, not by completion time.
func (c Canvas) Then(next Canvas) Canvas {
	return Canvas{Kind: "sequence", Steps: []Canvas{cloneJSON(c), cloneJSON(next)}}
}

// Parallel accepts complete nested canvases, including chains and chords. A
// following Then acts as a chord barrier over their logical outputs.
func Parallel(canvases ...Canvas) Canvas {
	return Canvas{Kind: "parallel", Steps: cloneJSON(canvases)}
}

type valueReference struct {
	id    string
	typ   reflect.Type
	empty bool
}
type canvasCompiler struct {
	client   *Client
	graph    Graph
	hasScope bool
}

func (c *Client) applyNested(ctx context.Context, canvas Canvas) (*GroupResult, error) {
	id, err := c.config.NewID()
	if err != nil {
		return nil, err
	}
	compiler := canvasCompiler{client: c, graph: Graph{ID: id, Kind: "dag", State: Running, Members: map[string]Completion{}, Revision: 1}}
	root, err := compiler.compile(canvas, nil, 0)
	if err != nil {
		return nil, err
	}
	compiler.graph.RootNode = root.id
	if err := c.authorize(ctx, "enqueue", compiler.graph.Scope, id); err != nil {
		return nil, err
	}
	graph, intents, err := c.advanceNested(compiler.graph)
	if err != nil {
		return nil, err
	}
	if err := c.config.Workflows.CreateGraph(ctx, graph, intents); err != nil {
		return nil, err
	}
	return &GroupResult{ID: id, client: c}, nil
}

func (b *canvasCompiler) compile(canvas Canvas, input *valueReference, depth int) (valueReference, error) {
	if depth > 32 || len(b.graph.Plan) > 2000 || len(b.graph.Children) > 1000 {
		return valueReference{}, ErrInvalid
	}
	switch canvas.Kind {
	case "task":
		if len(canvas.Signatures) != 1 || len(canvas.Steps) != 0 || canvas.Callback != nil {
			return valueReference{}, ErrInvalid
		}
		return b.task(canvas.Signatures[0], input)
	case "chain":
		if len(canvas.Steps) != 0 || canvas.Callback != nil || len(canvas.Signatures) > 1000 {
			return valueReference{}, ErrInvalid
		}
		previous := input
		for _, s := range canvas.Signatures {
			value, err := b.task(s, previous)
			if err != nil {
				return valueReference{}, err
			}
			previous = &value
		}
		if previous == nil {
			return b.collect(nil), nil
		}
		return *previous, nil
	case "group", "chord":
		if len(canvas.Steps) != 0 || len(canvas.Signatures) > 1000 {
			return valueReference{}, ErrInvalid
		}
		values := make([]valueReference, 0, len(canvas.Signatures))
		for _, s := range canvas.Signatures {
			value, err := b.task(s, input)
			if err != nil {
				return valueReference{}, err
			}
			values = append(values, value)
		}
		group := b.collect(values)
		if canvas.Kind == "group" {
			if canvas.Callback != nil {
				return valueReference{}, ErrInvalid
			}
			return group, nil
		}
		if canvas.Callback == nil {
			return valueReference{}, ErrInvalid
		}
		return b.task(*canvas.Callback, &group)
	case "sequence", "parallel":
		if len(canvas.Signatures) != 0 || canvas.Callback != nil || len(canvas.Steps) > 1000 {
			return valueReference{}, ErrInvalid
		}
		if canvas.Kind == "sequence" {
			previous := input
			for _, step := range canvas.Steps {
				value, err := b.compile(step, previous, depth+1)
				if err != nil {
					return valueReference{}, err
				}
				previous = &value
			}
			if previous == nil {
				return b.collect(nil), nil
			}
			return *previous, nil
		}
		values := make([]valueReference, 0, len(canvas.Steps))
		for _, step := range canvas.Steps {
			value, err := b.compile(step, input, depth+1)
			if err != nil {
				return valueReference{}, err
			}
			values = append(values, value)
		}
		return b.collect(values), nil
	default:
		return valueReference{}, ErrInvalid
	}
}

func (b *canvasCompiler) task(signature Signature, input *valueReference) (valueReference, error) {
	if len(b.graph.Children) >= 1000 || len(b.graph.Plan) >= 2000 {
		return valueReference{}, ErrInvalid
	}
	d, err := b.client.config.Registry.lookup(signature.Task, signature.Version)
	if err != nil {
		return valueReference{}, err
	}
	if input != nil && !signature.Immutable {
		target, err := inputBindingType(d, signature)
		if err != nil || input.typ != target && !(input.empty && target.Kind() == reflect.Slice) {
			return valueReference{}, fmt.Errorf("%w: incompatible nested canvas input", ErrInvalid)
		}
	}
	id := StableID(b.graph.ID, fmt.Sprintf("task-%d", len(b.graph.Children)))
	signature = signature.Set(WithID(id))
	envelope, err := b.client.prepare(signature)
	if err != nil {
		return valueReference{}, err
	}
	if b.hasScope && envelope.Scope != b.graph.Scope {
		return valueReference{}, ErrDenied
	}
	b.hasScope = true
	b.graph.Scope = envelope.Scope
	envelope.WorkflowID = b.graph.ID
	envelope.RootID = b.graph.ID
	envelope.ParentID = b.graph.ID
	node := CanvasNode{ID: id, Kind: "task", Signature: ptrSignature(signature.Clone())}
	if input != nil {
		node.Dependencies = []string{input.id}
		envelope.ParentID = input.id
	}
	b.graph.Children = append(b.graph.Children, envelope)
	b.graph.Plan = append(b.graph.Plan, node)
	return valueReference{id: id, typ: d.output}, nil
}

func (b *canvasCompiler) collect(values []valueReference) valueReference {
	id := StableID(b.graph.ID, fmt.Sprintf("collect-%d", len(b.graph.Plan)))
	node := CanvasNode{ID: id, Kind: "collect"}
	var element reflect.Type
	homogeneous := true
	for _, value := range values {
		node.Dependencies = append(node.Dependencies, value.id)
		if element == nil {
			element = value.typ
		} else if element != value.typ {
			homogeneous = false
		}
	}
	output := reflect.TypeFor[[]json.RawMessage]()
	if homogeneous && element != nil {
		output = reflect.SliceOf(element)
	}
	b.graph.Plan = append(b.graph.Plan, node)
	return valueReference{id: id, typ: output, empty: len(values) == 0}
}

func (c *Client) advanceNested(graph Graph) (Graph, []Intent, error) {
	if graph.State.Terminal() {
		return graph, nil, nil
	}
	if len(graph.Plan) > 2000 || len(graph.Children) > 1000 || graph.RootNode == "" {
		return graph, nil, ErrInvalid
	}
	children := map[string]int{}
	for i, child := range graph.Children {
		children[child.ID] = i
	}
	var intents []Intent
	for turn := 0; turn <= len(graph.Plan); turn++ {
		progress := false
		for i := range graph.Plan {
			node := &graph.Plan[i]
			if _, done := graph.Members[node.ID]; done || node.Dispatched {
				continue
			}
			ready, failed := true, false
			outputs := make([]json.RawMessage, 0, len(node.Dependencies))
			for _, id := range node.Dependencies {
				member, exists := graph.Members[id]
				if !exists {
					ready = false
					break
				}
				failed = failed || member.State != Succeeded
				outputs = append(outputs, member.Output)
			}
			if !ready {
				continue
			}
			if node.Kind == "collect" {
				member := Completion{ID: node.ID, State: Succeeded}
				if failed {
					member.State = Failed
					member.Failure = &Failure{Code: "GROUP_FAILED", Message: "One or more workflow members did not succeed"}
				} else {
					member.Output, _ = json.Marshal(outputs)
				}
				graph.Members[node.ID] = member
				progress = true
				continue
			}
			index, exists := children[node.ID]
			if node.Kind != "task" || node.Signature == nil || !exists || len(outputs) > 1 {
				return graph, nil, ErrInvalid
			}
			envelope := graph.Children[index]
			var failure *Failure
			if failed {
				failure = &Failure{Code: "PARENT_FAILED", Message: "A required upstream task did not succeed"}
			} else if len(outputs) == 1 && !node.Signature.Immutable {
				bound, err := node.Signature.bind(outputs[0])
				if err == nil {
					var definition *definition
					definition, err = c.config.Registry.lookup(bound.Task, bound.Version)
					if err == nil {
						err = definition.validate(bound.Args)
					}
				}
				if err == nil {
					candidate := envelope
					candidate.Args = bound.Args
					err = candidate.Validate()
					if err == nil {
						envelope = candidate
					}
				}
				if err != nil {
					failure = &Failure{Code: "WORKFLOW_INPUT", Message: "A workflow task could not accept the upstream result"}
				}
			}
			intent := dispatchIntent(graph.ID, envelope)
			if failure != nil {
				intent.Kind = "fail"
				intent.Failure = failure
			}
			graph.Children[index] = envelope
			node.Dispatched = true
			intents = append(intents, intent)
			progress = true
		}
		if !progress {
			break
		}
	}
	if root, done := graph.Members[graph.RootNode]; done {
		graph.State = root.State
		graph.Output = root.Output
		graph.Failure = root.Failure
		for _, child := range graph.Children {
			if _, complete := graph.Members[child.ID]; complete {
				intents = append(intents, Intent{ID: StableID(graph.ID, "unpin-"+child.ID), SourceID: graph.ID, Kind: "unpin", TargetID: child.ID})
			}
		}
	}
	return graph, intents, nil
}
