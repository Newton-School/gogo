package async

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
)

var ErrWorkflowStateGap = errors.New("async: required workflow task state unavailable")

func repairProviderError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	return controlProviderError(ctx, err)
}

type WorkflowRepairOptions struct {
	// Retain the previous report cursor to avoid starving later children when
	// earlier tasks stay active. Empty starts a fresh pass over this workflow.
	AfterTaskID string
	BatchSize   int
}
type WorkflowStateGap struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}
type WorkflowRepairReport struct {
	WorkflowID string `json:"workflow_id"`
	State      State  `json:"state"`
	Checked    int    `json:"checked"`
	// Converged counts authoritative terminal outcomes now represented in the
	// barrier, including concurrent idempotent progress by the ordinary relay.
	Converged  int                `json:"converged"`
	Active     int                `json:"active"`
	Gaps       []WorkflowStateGap `json:"gaps,omitempty"`
	NextTaskID string             `json:"next_task_id,omitempty"`
}

func (c *Client) authorizeWorkflowRepair(ctx context.Context, scope, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.config.Authorize == nil {
		return ErrDenied
	}
	err := c.config.Authorize(ctx, "reconcile", scope, id)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	return ErrUnavailable
}

// repairGraph validates the durable shape used by the ordinary transition
// builder. This is not a second graph compiler and does not execute handlers.
func repairGraph(graph Graph, id string) ([]Envelope, error) {
	if graph.ID != id || !idPattern.MatchString(id) || graph.Revision == 0 || len(graph.Scope) > 256 || graph.Members == nil || len(graph.Children) > 1000 || len(graph.Plan) > 2000 || len(graph.Members) > 2001 || !withinGraphBudget(graph, nil, MaxWorkflowDurableBytes) {
		return nil, ErrUnavailable
	}
	if graph.State != Running && !graph.State.Terminal() {
		return nil, ErrUnavailable
	}
	children := make(map[string]Envelope, len(graph.Children))
	for _, child := range graph.Children {
		if child.Validate() != nil || child.WorkflowID != id || child.Scope != graph.Scope {
			return nil, ErrUnavailable
		}
		if _, exists := children[child.ID]; exists {
			return nil, ErrUnavailable
		}
		children[child.ID] = child
	}
	allowedMembers := map[string]bool{}
	for id := range children {
		allowedMembers[id] = true
	}
	candidates := []Envelope{}
	switch graph.Kind {
	case "chain":
		if graph.Next < 0 || graph.Next > len(graph.Children) || len(graph.Signatures) != len(graph.Children) {
			return nil, ErrUnavailable
		}
		for index, child := range graph.Children {
			signature := graph.Signatures[index]
			if signature.Task != child.Task || signature.Version != child.Version || signature.Options.Scope != child.Scope || signature.Options.Principal != child.Principal || !json.Valid(signature.Args) {
				return nil, ErrUnavailable
			}
			if index <= graph.Next || graph.CancelRequested || graph.AbortFailure != nil {
				candidates = append(candidates, child)
			}
		}
	case "group", "chord":
		candidates = append(candidates, graph.Children...)
		if graph.Kind == "chord" {
			if graph.Callback == nil || !idPattern.MatchString(graph.CallbackID) || graph.Callback.Options.Scope != graph.Scope || !namePattern.MatchString(graph.Callback.Task) || graph.Callback.Version < 1 || !json.Valid(graph.Callback.Args) {
				return nil, ErrUnavailable
			}
			if _, exists := children[graph.CallbackID]; exists || !graph.CallbackClaimed && graph.CallbackEnvelope != nil {
				return nil, ErrUnavailable
			}
			if graph.CallbackClaimed {
				allowedMembers[graph.CallbackID] = true
				if graph.CallbackEnvelope == nil {
					return nil, ErrUnavailable
				}
				body := *graph.CallbackEnvelope
				if body.Validate() != nil || body.ID != graph.CallbackID || body.WorkflowID != id || body.Scope != graph.Scope || body.Task != graph.Callback.Task || body.Version != graph.Callback.Version || body.Principal != graph.Callback.Options.Principal {
					return nil, ErrUnavailable
				}
				candidates = append(candidates, body)
			}
		}
	case "dag":
		nodes := map[string]bool{}
		taskCount := 0
		for _, node := range graph.Plan {
			if !idPattern.MatchString(node.ID) || nodes[node.ID] || len(node.Dependencies) > 1000 || len(node.WaitFor) > 1000 {
				return nil, ErrUnavailable
			}
			for _, dependency := range append(slices.Clone(node.Dependencies), node.WaitFor...) {
				if !nodes[dependency] {
					return nil, ErrUnavailable
				}
			}
			nodes[node.ID] = true
			allowedMembers[node.ID] = true
			switch node.Kind {
			case "collect":
				if _, exists := children[node.ID]; exists {
					return nil, ErrUnavailable
				}
			case "task":
				child, exists := children[node.ID]
				if !exists || node.Signature == nil || len(node.Dependencies) > 1 || node.Signature.Task != child.Task || node.Signature.Version != child.Version || node.Signature.Options.Scope != child.Scope || node.Signature.Options.Principal != child.Principal || !json.Valid(node.Signature.Args) {
					return nil, ErrUnavailable
				}
				taskCount++
				if node.Dispatched {
					candidates = append(candidates, child)
				}
			default:
				return nil, ErrUnavailable
			}
		}
		if taskCount != len(children) || !nodes[graph.RootNode] {
			return nil, ErrUnavailable
		}
	default:
		return nil, ErrUnavailable
	}
	for key, member := range graph.Members {
		if !allowedMembers[key] || member.ID != key || !member.State.Terminal() || len(member.Output) > 0 && !json.Valid(member.Output) {
			return nil, ErrUnavailable
		}
	}
	if graph.State.Terminal() {
		return nil, nil
	}
	candidates = slices.DeleteFunc(candidates, func(child Envelope) bool { _, exists := graph.Members[child.ID]; return exists })
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	return candidates, nil
}

// ReconcileWorkflow repairs only missing barrier completions for already
// dispatched tasks whose exact authoritative terminal result still exists.
// It never invokes a task, republishes an absent result, fabricates completion,
// releases an active pin, or uses event/heartbeat state as execution authority.
// Ordinary graph CAS records the completion and any successor intents together.
//
// The explicit reconcile grant is required for the workflow and each task.
// Partial reports accompany errors. A missing record can mean pending publish
// or lost data; retry normal relay/outbox recovery before operator escalation.
// Keep NextTaskID between bounded passes; an empty cursor ends this pass, not
// necessarily the workflow. No cross-store atomicity or zero-loss is promised.
func (c *Client) ReconcileWorkflow(ctx context.Context, id string, options WorkflowRepairOptions) (WorkflowRepairReport, error) {
	if ctx == nil || c == nil || !idPattern.MatchString(id) || options.AfterTaskID != "" && !idPattern.MatchString(options.AfterTaskID) || options.BatchSize < 0 || options.BatchSize > 1000 {
		return WorkflowRepairReport{}, ErrInvalid
	}
	if c.config.Workflows == nil {
		return WorkflowRepairReport{}, ErrUnavailable
	}
	batch := options.BatchSize
	if batch == 0 {
		batch = 100
	}
	graph, err := c.config.Workflows.ReadGraph(ctx, id)
	if err != nil {
		return WorkflowRepairReport{}, repairProviderError(ctx, err)
	}
	if err := c.authorizeWorkflowRepair(ctx, graph.Scope, id); err != nil {
		return WorkflowRepairReport{}, err
	}
	candidates, err := repairGraph(graph, id)
	if err != nil {
		return WorkflowRepairReport{}, err
	}
	report := WorkflowRepairReport{WorkflowID: id, State: graph.State, NextTaskID: options.AfterTaskID}
	start := sort.Search(len(candidates), func(index int) bool { return candidates[index].ID > options.AfterTaskID })
	end := min(start+batch, len(candidates))
	for _, expected := range candidates[start:end] {
		if err := c.authorizeWorkflowRepair(ctx, graph.Scope, expected.ID); err != nil {
			return report, err
		}
		record, err := c.config.Results.Lookup(ctx, expected.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return report, repairProviderError(ctx, err)
		}
		if err == nil && (record.Envelope.Validate() != nil || record.Digest != expected.Digest() || record.Envelope.Digest() != record.Digest) {
			return report, ErrUnavailable
		}
		if err := c.authorizeWorkflowRepair(ctx, graph.Scope, expected.ID); err != nil {
			return report, err
		}
		report.Checked++
		gap := ""
		if errors.Is(err, ErrNotFound) {
			gap = "missing_record"
		} else if record.State == Succeeded && len(record.Output) == 0 {
			gap = "missing_output"
		}
		if gap != "" {
			// A normal relay may have converged while this result expired or
			// was removed. Do not report a gap already closed in the graph.
			fresh, readErr := c.config.Workflows.ReadGraph(ctx, id)
			if readErr != nil {
				return report, repairProviderError(ctx, readErr)
			}
			if fresh.Scope != graph.Scope {
				return report, ErrDenied
			}
			if _, readErr = repairGraph(fresh, id); readErr != nil {
				return report, readErr
			}
			if _, known := fresh.Members[expected.ID]; known {
				report.Converged++
			} else {
				report.Gaps = append(report.Gaps, WorkflowStateGap{TaskID: expected.ID, Reason: gap})
			}
		} else if !record.State.Terminal() {
			if record.State != Queued && record.State != Scheduled && record.State != Running && record.State != RetryWait {
				return report, ErrUnavailable
			}
			report.Active++
		} else {
			if len(record.Output) > MaxPayloadBytes || len(record.Output) > 0 && !json.Valid(record.Output) || record.Failure != nil && (len(record.Failure.Code) > 256 || len(record.Failure.Message) > 4096) {
				return report, ErrUnavailable
			}
			completion := Completion{ID: expected.ID, State: record.State, Output: append(json.RawMessage(nil), record.Output...)}
			if record.Failure != nil {
				failure := *record.Failure
				completion.Failure = &failure
			}
			err = c.config.Workflows.RecordMember(ctx, id, completion, func(current Graph) (Graph, []Intent, error) {
				if current.Scope != graph.Scope {
					return current, nil, ErrDenied
				}
				if _, err := repairGraph(current, id); err != nil {
					return current, nil, err
				}
				matched := false
				for _, child := range current.Children {
					if child.ID == expected.ID {
						matched = child.Digest() == expected.Digest()
					}
				}
				if current.CallbackClaimed && current.CallbackEnvelope != nil && current.CallbackEnvelope.ID == expected.ID {
					matched = current.CallbackEnvelope.Digest() == expected.Digest()
				}
				if !matched {
					return current, nil, ErrConflict
				}
				if err := c.authorizeWorkflowRepair(ctx, current.Scope, id); err != nil {
					return current, nil, err
				}
				if err := c.authorizeWorkflowRepair(ctx, current.Scope, expected.ID); err != nil {
					return current, nil, err
				}
				return c.advance(current)
			})
			if err != nil {
				return report, repairProviderError(ctx, err)
			}
			if err := c.authorizeWorkflowRepair(ctx, graph.Scope, expected.ID); err != nil {
				return report, err
			}
			report.Converged++
		}
		report.NextTaskID = expected.ID
	}
	if end == len(candidates) {
		report.NextTaskID = ""
	}
	fresh, err := c.config.Workflows.ReadGraph(ctx, id)
	if err != nil {
		return report, repairProviderError(ctx, err)
	}
	if fresh.Scope != graph.Scope {
		return report, ErrDenied
	}
	if err := c.authorizeWorkflowRepair(ctx, fresh.Scope, id); err != nil {
		return report, err
	}
	if _, err := repairGraph(fresh, id); err != nil {
		return report, err
	}
	report.State = fresh.State
	if len(report.Gaps) > 0 {
		return report, ErrWorkflowStateGap
	}
	return report, nil
}
