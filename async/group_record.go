package async

import (
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

// A provider observation must be bounded before any whole-graph JSON encoding
// or callback. The existing durable shape validator then checks topology; this
// boundary does not compile or advance a second workflow state machine.
func groupRecord(graph Graph, id string) (Graph, error) {
	if graph.ID != id || !idPattern.MatchString(id) || graph.Revision == 0 || len(graph.Scope) > 256 || !utf8.ValidString(graph.Scope) || graph.Members == nil || len(graph.Children) > 1000 || len(graph.Signatures) > 1000 || len(graph.Plan) > 2000 || len(graph.Members) > 2001 {
		return Graph{}, ErrUnavailable
	}
	if graph.State != Running && !graph.State.Terminal() {
		return Graph{}, ErrUnavailable
	}
	if graph.PayloadForgotten {
		if graph.Kind != "group" || !graph.State.Terminal() || len(graph.Output) != 0 {
			return Graph{}, ErrUnavailable
		}
		for _, member := range graph.Members {
			if len(member.Output) != 0 {
				return Graph{}, ErrUnavailable
			}
		}
	}
	b := groupReadBudget{remaining: MaxWorkflowDurableBytes}
	if !b.text(graph.ID, graph.Kind, graph.Scope, graph.CallbackID, graph.RootNode, graph.OriginTaskID, graph.OriginDigest) || !b.payload(graph.Output, MaxWorkflowDurableBytes) || !b.failure(graph.Failure, graph.State) || !b.failure(graph.AbortFailure, Failed) {
		return Graph{}, ErrUnavailable
	}
	if graph.Kind != "dag" && (len(graph.Plan) != 0 || graph.RootNode != "" || graph.OriginTaskID != "" || graph.OriginDigest != "") || graph.Kind != "chord" && (graph.Callback != nil || graph.CallbackID != "" || graph.CallbackClaimed || graph.CallbackEnvelope != nil) || graph.Kind != "chain" && graph.Next != 0 {
		return Graph{}, ErrUnavailable
	}
	if graph.OriginTaskID != "" || graph.OriginDigest != "" {
		if !idPattern.MatchString(graph.OriginTaskID) || len(graph.OriginDigest) != 64 {
			return Graph{}, ErrUnavailable
		}
		if digest, err := hex.DecodeString(graph.OriginDigest); err != nil || hex.EncodeToString(digest) != graph.OriginDigest {
			return Graph{}, ErrUnavailable
		}
	}
	for _, child := range graph.Children {
		if !b.envelope(child) {
			return Graph{}, ErrUnavailable
		}
	}
	for _, signature := range graph.Signatures {
		if !b.signature(signature) {
			return Graph{}, ErrUnavailable
		}
	}
	if graph.Callback != nil && !b.signature(*graph.Callback) || graph.CallbackEnvelope != nil && !b.envelope(*graph.CallbackEnvelope) {
		return Graph{}, ErrUnavailable
	}
	collectors := make(map[string]bool, len(graph.Plan))
	for _, node := range graph.Plan {
		if len(node.Dependencies) > 1000 || len(node.WaitFor) > 1000 || !b.text(node.ID, node.Kind) {
			return Graph{}, ErrUnavailable
		}
		for _, dependency := range node.Dependencies {
			if !b.text(dependency) {
				return Graph{}, ErrUnavailable
			}
		}
		for _, dependency := range node.WaitFor {
			if !b.text(dependency) {
				return Graph{}, ErrUnavailable
			}
		}
		if node.Kind == "collect" {
			if node.Signature != nil {
				return Graph{}, ErrUnavailable
			}
			collectors[node.ID] = true
		}
		if node.Signature != nil && !b.signature(*node.Signature) {
			return Graph{}, ErrUnavailable
		}
	}
	for key, member := range graph.Members {
		limit := MaxPayloadBytes
		if collectors[key] {
			limit = MaxWorkflowDurableBytes
		}
		if !b.text(key, member.ID, string(member.State)) || !b.payload(member.Output, limit) || !b.failure(member.Failure, member.State) {
			return Graph{}, ErrUnavailable
		}
	}
	if graph.Kind == "dag" {
		if len(graph.Signatures) != 0 {
			return Graph{}, ErrUnavailable
		}
	} else {
		if len(graph.Signatures) != len(graph.Children) {
			return Graph{}, ErrUnavailable
		}
		for i, child := range graph.Children {
			s := graph.Signatures[i]
			if s.Task != child.Task || s.Version != child.Version || s.Options.Scope != graph.Scope || s.Options.Principal != child.Principal {
				return Graph{}, ErrUnavailable
			}
		}
	}
	if _, err := repairGraph(graph, id); err != nil {
		return Graph{}, ErrUnavailable
	}
	// A terminal flat group has observed every member, including cancellation
	// and budget failures. Missing retained payloads remain valid metadata.
	if graph.Kind == "group" && graph.State.Terminal() {
		for _, child := range graph.Children {
			member, exists := graph.Members[child.ID]
			if !exists || graph.State == Succeeded && member.State != Succeeded {
				return Graph{}, ErrUnavailable
			}
		}
	}
	raw, err := json.Marshal(graph)
	if err != nil || len(raw) > MaxWorkflowDurableBytes {
		return Graph{}, ErrUnavailable
	}
	var detached Graph
	if json.Unmarshal(raw, &detached) != nil {
		return Graph{}, ErrUnavailable
	}
	for i := range detached.Children {
		detachEnvelopeTimes(&detached.Children[i])
	}
	for i := range detached.Signatures {
		detachSignatureTimes(&detached.Signatures[i])
	}
	if detached.Callback != nil {
		detachSignatureTimes(detached.Callback)
	}
	if detached.CallbackEnvelope != nil {
		detachEnvelopeTimes(detached.CallbackEnvelope)
	}
	for i := range detached.Plan {
		if detached.Plan[i].Signature != nil {
			detachSignatureTimes(detached.Plan[i].Signature)
		}
	}
	return detached, nil
}

type groupReadBudget struct{ remaining int }

func (b *groupReadBudget) text(values ...string) bool {
	for _, value := range values {
		if len(value) > b.remaining || !utf8.ValidString(value) {
			return false
		}
		b.remaining -= len(value)
	}
	return true
}
func (b *groupReadBudget) payload(value json.RawMessage, limit int) bool {
	if len(value) > limit || len(value) > b.remaining || !utf8.Valid(value) || len(value) != 0 && !json.Valid(value) {
		return false
	}
	b.remaining -= len(value)
	return true
}
func (b *groupReadBudget) failure(value *Failure, state State) bool {
	return value == nil || state != Succeeded && value.Code != "" && len(value.Code) <= 256 && len(value.Message) <= 4096 && b.text(value.Code, value.Message)
}
func (b *groupReadBudget) encoded(value any, limit int) bool {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > limit || len(raw) > b.remaining {
		return false
	}
	b.remaining -= len(raw)
	return true
}
func (b *groupReadBudget) envelope(value Envelope) bool {
	return boundedResultEnvelope(value) && b.encoded(value, MaxPayloadBytes)
}
func (b *groupReadBudget) signature(value Signature) bool {
	o := value.Options
	// Map root fields directly: adding a synthetic callback level would reject
	// a valid depth-16 signature tree. ParentField is not an envelope field and
	// the original ID option is replaced by the compiled stable task ID.
	e := Envelope{Task: value.Task, Args: value.Args, Queue: o.Queue, Scope: o.Scope, Principal: o.Principal, IdempotencyKey: o.IdempotencyKey, CorrelationID: o.CorrelationID, Headers: o.Headers, Stamps: o.Stamps, Callbacks: value.Callbacks, Errbacks: value.Errbacks}
	// Signature-only binding metadata is governed by the durable graph budget,
	// not the encoded task-envelope cap: an accepted flat group can retain an
	// unused ParentField longer than a task envelope without dispatching it.
	return len(value.ParentField) <= b.remaining && len(o.ID) <= b.remaining-len(value.ParentField) && utf8.ValidString(value.ParentField) && utf8.ValidString(o.ID) && boundedResultEnvelope(e) && namePattern.MatchString(value.Task) && value.Version > 0 && json.Valid(value.Args) && b.encoded(value, MaxWorkflowDurableBytes)
}
