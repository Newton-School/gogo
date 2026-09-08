package async

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sort"
	"time"
)

const MaxResultCollectionBytes = MaxWorkflowDurableBytes

var ErrResultCollectionTooLarge = errors.New("async: collected result data exceeds limit; use Iterate")

// GroupMember retains the original input position even when completion arrives
// out of order. Outcome contains a real terminal member result, never a guessed
// success or synthetic state for an undispatched task.
type GroupMember struct {
	Index   int        `json:"index"`
	Outcome Completion `json:"outcome"`
}

func (g *GroupResult) Ready(ctx context.Context) (bool, error) {
	graph, err := g.Snapshot(ctx)
	return graph.State.Terminal(), err
}

func (g *GroupResult) Successful(ctx context.Context) (bool, error) {
	graph, err := g.Snapshot(ctx)
	return graph.State == Succeeded, err
}

func (g *GroupResult) Failed(ctx context.Context) (bool, error) {
	graph, err := g.Snapshot(ctx)
	return graph.State == Failed, err
}

// Iterate streams each terminal member of a flat Group once, retaining its
// original index. Available outcomes are visited in input order per poll, not
// an asserted wall-clock completion order. A failed member is an Outcome, not
// an iterator error. Backend, authorization, expiry and context errors stop the
// stream and follow any already yielded partial outcomes. Each yield rechecks
// authorization. Breaking the range stops polling without canceling tasks.
//
// Chain/chord/nested canvas handles retain Snapshot/Join; their skipped nodes and
// logical collectors are not silently reinterpreted as flat Group members.
func (g *GroupResult) Iterate(ctx context.Context) iter.Seq2[GroupMember, error] {
	return func(yield func(GroupMember, error) bool) {
		fail := func(err error) { yield(GroupMember{}, err) }
		op, err := g.operation(ctx)
		if err != nil {
			fail(err)
			return
		}
		if ctx.Value(workerContextKey{}) != nil {
			fail(ErrWorkerJoin)
			return
		}
		seen := map[string]bool{}
		var expected []string
		var scope string
		var layout string
		initialized := false
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := ctx.Err(); err != nil {
				fail(err)
				return
			}
			graph, err := op.snapshot(ctx)
			if err != nil {
				fail(err)
				return
			}
			if graph.Kind != "group" {
				fail(ErrInvalid)
				return
			}
			if err := matchGroupLayout(&layout, graph); err != nil {
				fail(err)
				return
			}
			if !initialized {
				if len(graph.Children) > 1000 {
					fail(ErrUnavailable)
					return
				}
				unique := map[string]bool{}
				for _, child := range graph.Children {
					if child.ID == "" || unique[child.ID] || child.Scope != graph.Scope {
						fail(ErrUnavailable)
						return
					}
					unique[child.ID] = true
					expected = append(expected, child.ID)
				}
				scope, initialized = graph.Scope, true
			} else {
				if graph.Scope != scope || len(graph.Children) != len(expected) {
					fail(ErrUnavailable)
					return
				}
				for i, child := range graph.Children {
					if child.ID != expected[i] || child.Scope != scope {
						fail(ErrUnavailable)
						return
					}
				}
			}
			for index, id := range expected {
				if seen[id] {
					continue
				}
				outcome, ok := graph.Members[id]
				if !ok {
					continue
				}
				if outcome.ID != id || !outcome.State.Terminal() {
					fail(ErrUnavailable)
					return
				}
				if err := ctx.Err(); err != nil {
					fail(err)
					return
				}
				if err := op.authorize(ctx, "read", scope); err != nil {
					fail(err)
					return
				}
				if outcome.State == Succeeded && len(outcome.Output) == 0 {
					// Oversized coordination drops copied aggregate payloads, not
					// successful child results. Recover through an independently
					// authorized child lookup; never fabricate an expired value.
					record, err := RestoreResult[json.RawMessage](op.client, id).Snapshot(ctx)
					if err != nil {
						fail(err)
						return
					}
					if record.Envelope.ID != id || record.Envelope.Scope != scope || record.State != outcome.State {
						fail(ErrUnavailable)
						return
					}
					outcome.Output = record.Output
					if len(outcome.Output) == 0 {
						fail(ErrResultExpired)
						return
					}
					if err := op.authorize(ctx, "read", scope); err != nil {
						fail(err)
						return
					}
				}
				if len(outcome.Output) > MaxPayloadBytes {
					fail(ErrUnavailable)
					return
				}
				seen[id] = true
				if !yield(GroupMember{Index: index, Outcome: cloneJSON(outcome)}, nil) {
					return
				}
			}
			if graph.State.Terminal() {
				if len(seen) != len(expected) {
					fail(ErrUnavailable)
				}
				return
			}
			select {
			case <-ctx.Done():
				fail(ctx.Err())
				return
			case <-ticker.C:
			}
		}
	}
}

// JoinOutcomes selects the non-propagating member-failure policy for a flat
// Group. Unlike Join, member failures are returned as Completion values. On a
// wait/read error, the returned slice contains only the observed terminal
// members, in original relative order, together with that error. Collection is
// bounded; use Iterate for larger output sets. Neither method revokes work.
func (g *GroupResult) JoinOutcomes(ctx context.Context) ([]Completion, error) {
	var observed []GroupMember
	var failure error
	total := 0
	for member, err := range g.Iterate(ctx) {
		if err != nil {
			failure = err
			break
		}
		encoded, err := json.Marshal(member)
		if err != nil {
			failure = ErrUnavailable
			break
		}
		if len(encoded) > MaxResultCollectionBytes-total {
			failure = ErrResultCollectionTooLarge
			break
		}
		total += len(encoded)
		observed = append(observed, member)
	}
	sort.Slice(observed, func(i, j int) bool { return observed[i].Index < observed[j].Index })
	outcomes := make([]Completion, len(observed))
	for i, member := range observed {
		outcomes[i] = member.Outcome
	}
	return outcomes, failure
}
