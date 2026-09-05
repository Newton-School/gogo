package async

import (
	"encoding/json"
	"errors"
)

const (
	// Initial acceptance reserves space for completion metadata and recovery
	// intents below durable adapters' 8 MiB atomic-document limit.
	MaxWorkflowInitialBytes = 2 << 20
	MaxWorkflowWorkingBytes = 4 << 20
	MaxWorkflowDurableBytes = 8 << 20
)

var errWorkflowSize = errors.New("async: workflow payload budget exceeded")

func withinGraphBudget(graph Graph, intents []Intent, limit int) bool {
	encoded, err := json.Marshal(graph)
	if err != nil || len(encoded) > limit {
		return false
	}
	encoded, err = json.Marshal(intents)
	return err == nil && len(encoded) <= limit
}

func validateGraphAcceptance(graph Graph, intents []Intent) error {
	if !withinGraphBudget(graph, intents, MaxWorkflowInitialBytes) {
		return ErrInvalid
	}
	return nil
}

// advanceAbortedGraph preserves real task outcomes and waits for accepted
// deliveries. Only undispatched dependents receive synthetic failed records;
// no running task is killed and no successful child is rewritten as failed.
func advanceAbortedGraph(graph Graph) (Graph, []Intent, error) {
	if graph.AbortFailure == nil {
		return graph, nil, ErrInvalid
	}
	for id, member := range graph.Members {
		member.Output = nil
		graph.Members[id] = member
	}
	graph.Output = nil
	var intents []Intent
	finished := true
	children := map[string]Envelope{}
	for _, child := range graph.Children {
		children[child.ID] = child
	}
	fail := func(child Envelope) {
		intent := dispatchIntent(graph.ID, child)
		intent.Kind = "fail"
		intent.Failure = graph.AbortFailure
		intent.State = Failed
		intents = append(intents, intent)
	}
	if graph.Kind == "dag" {
		for i := range graph.Plan {
			node := &graph.Plan[i]
			if node.Kind != "task" {
				continue
			}
			if _, done := graph.Members[node.ID]; done {
				continue
			}
			finished = false
			if !node.Dispatched {
				child, exists := children[node.ID]
				if !exists {
					return graph, nil, ErrInvalid
				}
				fail(child)
				node.Dispatched = true
			}
		}
	} else {
		for index, child := range graph.Children {
			if _, done := graph.Members[child.ID]; done {
				continue
			}
			finished = false
			if graph.Kind == "chain" && index > graph.Next {
				fail(child)
			}
		}
		if graph.CallbackClaimed {
			if _, done := graph.Members[graph.CallbackID]; !done {
				finished = false
			}
		}
	}
	if finished {
		graph.State = Failed
		graph.Failure = graph.AbortFailure
		intents = append(intents, graphUnpins(graph)...)
	}
	return graph, intents, nil
}
