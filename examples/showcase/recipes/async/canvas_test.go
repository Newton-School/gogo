package async_recipes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Newton-School/gogo/async"
)

func Example_signatures() {
	registry := async.NewRegistry()
	addOne := increment(registry)
	base := must(addOne.Signature(1))
	copy := base.Clone().Set(async.WithPriority(3), async.OnQueue("default"))
	copy.Args[0] = '9' // The original JSON snapshot remains detached.
	fmt.Println("original:", string(base.Args), "copy:", string(copy.Args))
	fmt.Println("immutable:", base.ImmutableSignature().Immutable)
	fmt.Println("eager:", must(addOne.Apply(context.Background(), 41)))
	// Output:
	// original: 1 copy: 9
	// immutable: true
	// eager: 42
}

type pair struct {
	Left  int `json:"left"`
	Right int `json:"right"`
}

func Example_canvas() {
	registry := async.NewRegistry()
	addOne := increment(registry)
	sum := must(async.Register(registry, "recipes.sum", 1,
		func(_ context.Context, _ async.TaskContext, values []int) (int, error) {
			total := 0
			for _, value := range values {
				total += value
			}
			return total, nil
		}, async.TaskOptions{}))
	add := must(async.Register(registry, "recipes.add", 1,
		func(_ context.Context, _ async.TaskContext, value pair) (int, error) {
			return value.Left + value.Right, nil
		}, async.TaskOptions{}))
	h := newHarness(registry)
	defer h.cancel()
	first, next := must(addOne.Signature(1)), must(addOne.Signature(0))
	chain := must(h.client.ApplyCanvas(h.ctx, async.Chain(first, next, next)))
	group := must(h.client.ApplyCanvas(h.ctx, async.Group(first, must(addOne.Signature(4)))))
	chord := must(h.client.ApplyCanvas(h.ctx, must(async.Chord(
		async.Group(first, must(addOne.Signature(4))), must(sum.Signature(nil))))))
	// Normal successors replace their entire input with the previous output.
	// Immutable signatures keep their original input; FromParent binds one field.
	immutable := must(h.client.ApplyCanvas(h.ctx, async.Chain(first, must(addOne.Signature(10)).ImmutableSignature())))
	bound := must(h.client.ApplyCanvas(h.ctx, async.Chain(first, must(add.Signature(pair{Right: 5})).FromParent("left"))))
	h.drain()
	fmt.Println("chain:", string(must(chain.Join(h.ctx))[0]))
	fmt.Println("group:", string(must(json.Marshal(must(group.Join(h.ctx))))))
	fmt.Println("chord:", string(must(chord.Join(h.ctx))[0]))
	fmt.Println("immutable:", string(must(immutable.Join(h.ctx))[0]))
	fmt.Println("bound field:", string(must(bound.Join(h.ctx))[0]))
	fmt.Println("group ready:", must(group.Ready(h.ctx)), "successful:", must(group.Successful(h.ctx)))
	for member, err := range group.Iterate(h.ctx) {
		check(err)
		fmt.Println("member:", member.Index, string(member.Outcome.Output))
	}
	// Output:
	// chain: 4
	// group: [2,5]
	// chord: 7
	// immutable: 11
	// bound field: 7
	// group ready: true successful: true
	// member: 0 2
	// member: 1 5
}

func Example_mapStarMapAndChunks() {
	registry := async.NewRegistry()
	square := must(async.Register(registry, "recipes.square", 1,
		func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n * n, nil }, async.TaskOptions{}))
	mapped := must(async.RegisterMap(registry, "recipes.squares", 1, square, async.TaskOptions{}))
	add := must(async.Register(registry, "recipes.add", 1,
		func(_ context.Context, _ async.TaskContext, value pair) (int, error) {
			return value.Left + value.Right, nil
		}, async.TaskOptions{}))
	starred := must(async.RegisterStarMap(registry, "recipes.add_pairs", 1, add, async.TaskOptions{}))
	h := newHarness(registry)
	defer h.cancel()
	mapResult := must(mapped.Delay(h.ctx, h.client, []int{1, 2, 3}))
	// Tuples bind to exported Go struct fields in declaration order.
	starResult := must(starred.Delay(h.ctx, h.client, [][]json.RawMessage{{json.RawMessage(`2`), json.RawMessage(`3`)}, {json.RawMessage(`4`), json.RawMessage(`5`)}}))
	chunkResult := must(h.client.ApplyCanvas(h.ctx, must(async.Chunks(mapped, []int{1, 2, 3, 4, 5}, 2))))
	h.drain()
	fmt.Println("map:", must(mapResult.Get(h.ctx)))
	fmt.Println("starmap:", must(starResult.Get(h.ctx)))
	fmt.Println("chunks:", string(must(json.Marshal(must(chunkResult.Join(h.ctx))))))
	// Map executes its bounded list sequentially inside one task. Chunks creates
	// a group of such tasks; retrying a chunk may repeat earlier item effects.
	// Output:
	// map: [1 4 9]
	// starmap: [5 9]
	// chunks: [[1,4],[9,16],[25]]
}

func TestCanvasRejectsUnsupportedBindingsAndLimits(t *testing.T) {
	registry := async.NewRegistry()
	addOne := increment(registry)
	mapped := must(async.RegisterMap(registry, "recipes.increments", 1, addOne, async.TaskOptions{}))
	h := newHarness(registry)
	defer h.cancel()
	signature := must(addOne.Signature(1))
	// An int callback cannot receive the []int produced by a chord header.
	canvas := must(async.Chord(async.Group(signature), signature))
	if _, err := h.client.ApplyCanvas(h.ctx, canvas); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("incompatible chord callback was accepted", err)
	}
	if _, err := async.Chunks(mapped, []int{1}, 0); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("zero chunk size was accepted", err)
	}
	if _, err := mapped.Signature(make([]int, 1001)); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("oversized map payload was accepted", err)
	}
	// A nonempty scope needs an explicitly configured producer authorization
	// policy. This fixture intentionally has none, so it must deny the task.
	if _, err := addOne.Delay(h.ctx, h.client, 1, async.WithScope("tenant-a", "actor-a")); !errors.Is(err, async.ErrDenied) {
		t.Fatal("scope was accepted without a policy", err)
	}
}
