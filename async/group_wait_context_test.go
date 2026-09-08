package async_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

type groupWaitContext struct {
	context.Context
	err   func() error
	done  func() <-chan struct{}
	value func(any) any
}

func (c *groupWaitContext) Err() error {
	if c.err != nil {
		return c.err()
	}
	return c.Context.Err()
}
func (c *groupWaitContext) Done() <-chan struct{} {
	if c.done != nil {
		return c.done()
	}
	return c.Context.Done()
}
func (c *groupWaitContext) Value(key any) any {
	if c.value != nil {
		return c.value(key)
	}
	return c.Context.Value(key)
}

// The Join adapter compares only output/count/error, never invented member IDs.
func collectGroupWait(g *async.GroupResult, ctx context.Context, method string) ([]async.Completion, error) {
	if method == "join" {
		values, err := g.Join(ctx)
		out := make([]async.Completion, len(values))
		for i, value := range values {
			out[i].Output = value
		}
		return out, err
	}
	if method == "outcomes" {
		return g.JoinOutcomes(ctx)
	}
	var out []async.Completion
	for member, err := range g.Iterate(ctx) {
		if err != nil {
			return out, err
		}
		out = append(out, member.Outcome)
	}
	return out, nil
}

func TestGroupWaitClosedDoneNeverInventsSuccessOrMember(t *testing.T) {
	for _, method := range []string{"join", "outcomes", "iterate"} {
		for _, partial := range []bool{false, true} {
			t.Run(method+"/partial="+map[bool]string{false: "no", true: "yes"}[partial], func(t *testing.T) {
				graph := groupBoundaryGraph(t, 2)
				graph.State, graph.Output = async.Running, nil
				delete(graph.Members, graph.Children[0].ID)
				if !partial {
					delete(graph.Members, graph.Children[1].ID)
				}
				reads := 0
				store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { reads++; return graph, nil }}
				group := async.RestoreGroup(outcomeClient(t, store, nil), graph.ID)
				done := make(chan struct{})
				close(done)
				ctx := resultClosedDoneContext{Context: context.Background(), done: done}
				out, err := collectGroupWait(group, ctx, method)
				want := 0
				if partial && method != "join" {
					want = 1
				}
				if err != async.ErrUnavailable || len(out) != want || reads != 1 {
					t.Fatal("malformed context fabricated completion", len(out), reads, err)
				}
				if want == 1 && (out[0].ID != graph.Children[1].ID || out[0].State != async.Succeeded || string(out[0].Output) != "1") {
					t.Fatal("genuine partial member changed")
				}
			})
		}
	}
}

func TestGroupWaitContextFailuresStaySafe(t *testing.T) {
	for _, method := range []string{"join", "outcomes", "iterate"} {
		for _, mode := range []string{"nil", "typed nil", "canceled", "Value panic", "Done panic", "entry Err panic", "late Err panic", "late Err private"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				graph := groupBoundaryGraph(t, 1)
				graph.State, graph.Output = async.Running, nil
				graph.Members = map[string]async.Completion{}
				reads := 0
				store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { reads++; return graph, nil }}
				group := async.RestoreGroup(outcomeClient(t, store, nil), graph.ID)
				custom := &groupWaitContext{Context: context.Background()}
				var ctx context.Context = custom
				want := error(async.ErrUnavailable)
				switch mode {
				case "nil":
					ctx, want = nil, async.ErrInvalid
				case "typed nil":
					var absent *groupWaitContext
					ctx, want = absent, async.ErrInvalid
				case "canceled":
					custom.err = func() error { return context.Canceled }
					want = context.Canceled
				case "Value panic":
					custom.value = func(any) any { panic("private context Value") }
				case "Done panic":
					custom.done = func() <-chan struct{} { panic("private context Done") }
				case "entry Err panic":
					custom.err = func() error { panic("private context Err") }
				default:
					armed := false
					custom.value = func(any) any { armed = true; return nil }
					custom.err = func() error {
						if armed {
							if mode == "late Err panic" {
								panic("private late context Err")
							}
							return errors.New("private malformed context error")
						}
						return nil
					}
				}
				var out []async.Completion
				var err error
				escaped := false
				func() {
					defer func() { escaped = recover() != nil }()
					out, err = collectGroupWait(group, ctx, method)
				}()
				if escaped || len(out) != 0 || err != want {
					t.Fatal("context failure escaped safe boundary", escaped, len(out), err == want)
				}
				if mode != "Done panic" && reads != 0 {
					t.Fatal("invalid entry reached provider", reads)
				}
			})
		}
	}
}

func TestGroupWaitFinalYieldCancellationRetainsOnlyObservedMember(t *testing.T) {
	graph := groupBoundaryGraph(t, 1)
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
	group := async.RestoreGroup(outcomeClient(t, store, nil), graph.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen, failures := 0, 0
	for member, err := range group.Iterate(ctx) {
		if err != nil {
			if err != context.Canceled || member.Outcome.ID != "" {
				t.Fatal("terminal cancellation changed failure")
			}
			failures++
			continue
		}
		if member.Index != 0 || member.Outcome.ID != graph.Children[0].ID {
			t.Fatal("fabricated member")
		}
		seen++
		cancel()
	}
	if seen != 1 || failures != 1 {
		t.Fatal("last accepted yield hid cancellation", seen, failures)
	}
}

func TestGroupWaitEmptyCompletionChecksCancellation(t *testing.T) {
	for _, method := range []string{"join", "outcomes", "iterate"} {
		t.Run(method, func(t *testing.T) {
			graph := groupBoundaryGraph(t, 0)
			granted, checks := false, 0
			ctx := &groupWaitContext{Context: context.Background(), err: func() error {
				if granted {
					checks++
					// The authorization and Snapshot completion checks pass; the
					// next completion fence observes cancellation deterministically.
					if checks > 2 {
						return context.Canceled
					}
				}
				return nil
			}}
			store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
			client := outcomeClient(t, store, func(context.Context, string, string, string) error { granted = true; return nil })
			out, err := collectGroupWait(async.RestoreGroup(client, graph.ID), ctx, method)
			if len(out) != 0 || err != context.Canceled {
				t.Fatal("empty terminal completion skipped context fence", len(out), err)
			}
		})
	}
}

func TestGroupWaitConsumerBreakAndPanicRemainOwnedByConsumer(t *testing.T) {
	for _, mode := range []string{"break", "panic"} {
		t.Run(mode, func(t *testing.T) {
			graph := groupBoundaryGraph(t, 1)
			reads, lateContextCalls := 0, 0
			consuming := false
			ctx := &groupWaitContext{Context: context.Background(), err: func() error {
				if consuming {
					lateContextCalls++
					return context.Canceled
				}
				return nil
			}}
			store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { reads++; return graph, nil }}
			group := async.RestoreGroup(outcomeClient(t, store, nil), graph.ID)
			marker := errors.New("consumer owns this panic")
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				for _, err := range group.Iterate(ctx) {
					if err != nil {
						t.Fatal(err)
					}
					consuming = true
					if mode == "panic" {
						panic(marker)
					}
					break
				}
			}()
			if reads != 1 || lateContextCalls != 0 || mode == "break" && recovered != nil || mode == "panic" && recovered != marker {
				t.Fatal("iterator replaced consumer control flow", reads, lateContextCalls, recovered == marker)
			}
		})
	}
}

func TestGroupWaitFreezesIdentityBeforeContextCallbacks(t *testing.T) {
	for _, method := range []string{"join", "outcomes", "iterate"} {
		for _, stage := range []string{"Err", "Value", "Done"} {
			t.Run(method+"/"+stage, func(t *testing.T) {
				graph := groupBoundaryGraph(t, 1)
				reads, wrong := 0, 0
				store := &groupBoundaryStore{read: func(_ context.Context, id string) (async.Graph, error) {
					if id != graph.ID {
						wrong++
					}
					reads++
					copy := graph
					if reads == 1 {
						copy.State = async.Running
					}
					return copy, nil
				}}
				client := outcomeClient(t, store, nil)
				replacement := outcomeClient(t, &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
					wrong++
					return async.Graph{}, async.ErrUnavailable
				}}, nil)
				group := async.RestoreGroup(client, graph.ID)
				armed := true
				replace := func() {
					if armed {
						armed = false
						*client = *replacement
						*group = *async.RestoreGroup(replacement, async.StableID(graph.ID, "replacement"))
					}
				}
				base, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				ctx := &groupWaitContext{Context: base}
				if stage == "Err" {
					ctx.err = func() error { replace(); return base.Err() }
				} else if stage == "Done" {
					ctx.done = func() <-chan struct{} { replace(); return base.Done() }
				} else {
					ctx.value = func(key any) any { replace(); return base.Value(key) }
				}
				out, err := collectGroupWait(group, ctx, method)
				if err != nil || len(out) != 1 || string(out[0].Output) != "1" || reads != 2 || wrong != 0 {
					t.Fatal("context retargeted the wait", len(out), reads, wrong, err)
				}
			})
		}
	}
}
