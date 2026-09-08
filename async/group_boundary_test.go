package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type groupBoundaryStore struct {
	async.WorkflowStore
	read   func(context.Context, string) (async.Graph, error)
	cancel func(context.Context, string, string, func(async.Graph) (async.Graph, []async.Intent, error)) error
}

func TestGroupBoundaryMalformedGraphsExposeNothing(t *testing.T) {
	cases := map[string]func(*async.Graph){
		"revision":        func(g *async.Graph) { g.Revision = 0 },
		"state":           func(g *async.Graph) { g.State = async.State(strings.Repeat("x", 1<<20)) },
		"kind":            func(g *async.Graph) { g.Kind = "unknown" },
		"child_scope":     func(g *async.Graph) { g.Children[0].Scope = "other" },
		"signature_scope": func(g *async.Graph) { g.Signatures[0].Options.Scope = "other" },
		"child_protocol":  func(g *async.Graph) { g.Children[0].ProtocolVersion = 99 },
		"duplicate_child": func(g *async.Graph) {
			g.Children = append(g.Children, g.Children[0])
			g.Signatures = append(g.Signatures, g.Signatures[0])
		},
		"missing_member": func(g *async.Graph) { delete(g.Members, g.Children[0].ID) },
		"member_identity": func(g *async.Graph) {
			m := g.Members[g.Children[0].ID]
			m.ID = async.StableID(g.ID, "wrong")
			g.Members[g.Children[0].ID] = m
		},
		"member_state": func(g *async.Graph) {
			m := g.Members[g.Children[0].ID]
			m.State = async.Running
			g.Members[g.Children[0].ID] = m
		},
		"member_payload": func(g *async.Graph) {
			m := g.Members[g.Children[0].ID]
			m.Output = json.RawMessage(`{"broken"`)
			g.Members[g.Children[0].ID] = m
		},
		"member_oversize": func(g *async.Graph) {
			m := g.Members[g.Children[0].ID]
			m.Output, _ = json.Marshal(strings.Repeat("x", async.MaxPayloadBytes))
			g.Members[g.Children[0].ID] = m
		},
		"failure_success": func(g *async.Graph) { g.Failure = &async.Failure{Code: "FAIL", Message: "Contradiction"} },
		"failure_utf8": func(g *async.Graph) {
			g.State = async.Failed
			g.Failure = &async.Failure{Code: "FAIL", Message: string([]byte{255})}
		},
		"invalid_json":   func(g *async.Graph) { g.Output = json.RawMessage(`{"broken"`) },
		"oversize_graph": func(g *async.Graph) { g.Output, _ = json.Marshal(strings.Repeat("x", async.MaxWorkflowDurableBytes)) },
		"wire_overhead_budget": func(g *async.Graph) {
			g.Output, _ = json.Marshal(strings.Repeat("x", async.MaxWorkflowDurableBytes-128))
		},
		"invalid_utf8": func(g *async.Graph) { g.Signatures[0].ParentField = string([]byte{255}) },
		"cyclic_signature": func(g *async.Graph) {
			links := make([]async.Signature, 1)
			links[0] = g.Signatures[0]
			links[0].Callbacks = links
			g.Signatures[0].Callbacks = links
		},
		"foreign_shape":  func(g *async.Graph) { g.CallbackID = async.StableID(g.ID, "body") },
		"too_many_nodes": func(g *async.Graph) { g.Plan = make([]async.CanvasNode, 2001) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			graph := groupBoundaryGraph(t, 2)
			mutate(&graph)
			store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
			group := async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error {
				t.Fatal("malformed graph reached authorization")
				return nil
			}), graph.ID)
			if out, err := group.Snapshot(context.Background()); err != async.ErrUnavailable || !reflect.DeepEqual(out, async.Graph{}) {
				t.Fatal("partial snapshot", out.ID, err)
			}
			out, err := group.JoinOutcomes(context.Background())
			if err != async.ErrUnavailable || len(out) != 0 {
				t.Fatal("partial malformed stream", len(out), err)
			}
		})
	}
}

func TestGroupBoundaryReadPanicsAndExactProviderErrors(t *testing.T) {
	for _, want := range []error{async.ErrNotFound, async.ErrResultExpired, async.ErrDenied, context.Canceled, context.DeadlineExceeded, async.ErrUnavailable} {
		t.Run(want.Error(), func(t *testing.T) {
			graph := groupBoundaryGraph(t, 1)
			store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
				if want == async.ErrUnavailable {
					panic("private provider panic")
				}
				return graph, want
			}}
			if out, err := async.RestoreGroup(outcomeClient(t, store, nil), graph.ID).Snapshot(context.Background()); err != want || out.ID != "" {
				t.Fatal(out.ID, err)
			}
		})
	}
	graph := groupBoundaryGraph(t, 1)
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
	group := async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error { panic("private grant panic") }), graph.ID)
	if out, err := group.Snapshot(context.Background()); err != async.ErrUnavailable || out.ID != "" {
		t.Fatal(out.ID, err)
	}
}

func TestGroupBoundaryCancellationCommandOutcomes(t *testing.T) {
	for _, mode := range []string{"before", "confirmed", "joined_error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			graph := groupBoundaryGraph(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			applied := false
			store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }, cancel: func(context.Context, string, string, func(async.Graph) (async.Graph, []async.Intent, error)) error {
				applied = true
				if mode == "panic" {
					panic("private provider panic")
				}
				if mode == "joined_error" {
					return errors.Join(async.ErrDenied, errors.New("possibly applied"))
				}
				cancel()
				return nil
			}}
			client := outcomeClient(t, store, func(_ context.Context, action, _, _ string) error {
				if action == "revoke" && mode == "before" {
					cancel()
				}
				return nil
			})
			err := async.RestoreGroup(client, graph.ID).Revoke(ctx)
			want := async.ErrUnavailable
			if mode == "before" {
				want = context.Canceled
			}
			if mode == "confirmed" {
				want = nil
			}
			if err != want || applied != (mode != "before") {
				t.Fatal(applied, err)
			}
		})
	}
}

func TestGroupBoundaryDetachedTimestampLocations(t *testing.T) {
	var timestamp time.Time
	if err := json.Unmarshal([]byte(`"2026-01-02T03:04:05+01:00"`), &timestamp); err != nil {
		t.Fatal(err)
	}
	location := timestamp.Location()
	prior := *location
	defer func() { *location = prior }()
	graph := groupBoundaryGraph(t, 1)
	graph.Children[0].CreatedAt, graph.Children[0].ETA, graph.Children[0].ExpiresAt = timestamp, timestamp, timestamp
	link := graph.Signatures[0]
	link.Options.ETA, link.Options.ExpiresAt = timestamp, timestamp
	graph.Signatures[0] = link
	graph.Signatures[0].Callbacks = []async.Signature{link}
	graph.Signatures[0].Errbacks = []async.Signature{link}
	graph.Children[0].Callbacks = []async.Signature{link}
	graph.Children[0].Errbacks = []async.Signature{link}
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
	group := async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error {
		*location = *time.FixedZone("changed", 7200)
		return nil
	}), graph.ID)
	out, err := group.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	times := []time.Time{out.Children[0].CreatedAt, out.Children[0].ETA, out.Children[0].ExpiresAt, out.Signatures[0].Options.ETA, out.Signatures[0].Options.ExpiresAt, out.Signatures[0].Callbacks[0].Options.ETA, out.Signatures[0].Errbacks[0].Options.ExpiresAt, out.Children[0].Callbacks[0].Options.ETA, out.Children[0].Errbacks[0].Options.ExpiresAt}
	for _, value := range times {
		if _, offset := value.Zone(); offset != 3600 {
			t.Fatal("provider location alias", offset)
		}
	}
	*times[0].Location() = *time.FixedZone("caller changed", -3600)
	for _, value := range times[1:] {
		if _, offset := value.Zone(); offset != 3600 {
			t.Fatal("returned timestamps share location", offset)
		}
	}
}

func TestGroupBoundaryIterateFreezesHandleAcrossYields(t *testing.T) {
	graph := groupBoundaryGraph(t, 2)
	var group *async.GroupResult
	store := &groupBoundaryStore{read: func(_ context.Context, id string) (async.Graph, error) {
		if id != graph.ID {
			t.Fatal("read retargeted", id)
		}
		return graph, nil
	}}
	client := outcomeClient(t, store, func(_ context.Context, _, _, id string) error {
		if id != graph.ID {
			t.Fatal("grant retargeted", id)
		}
		return nil
	})
	other := outcomeClient(t, &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
		t.Fatal("replacement client read")
		return async.Graph{}, nil
	}}, nil)
	group = async.RestoreGroup(client, graph.ID)
	count := 0
	for _, err := range group.Iterate(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		*group = *async.RestoreGroup(other, async.StableID(graph.ID, "other"))
	}
	if count != 2 {
		t.Fatal(count)
	}
}

func TestGroupBoundaryFreezesClientPortsBeforeCallbacks(t *testing.T) {
	graph := groupBoundaryGraph(t, 1)
	originalCommands, replacementCommands, originalGrants := 0, 0, 0
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }, cancel: func(context.Context, string, string, func(async.Graph) (async.Graph, []async.Intent, error)) error {
		originalCommands++
		return nil
	}}
	replacement := outcomeClient(t, &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }, cancel: func(context.Context, string, string, func(async.Graph) (async.Graph, []async.Intent, error)) error {
		replacementCommands++
		return nil
	}}, func(context.Context, string, string, string) error { return nil })
	var client *async.Client
	client = outcomeClient(t, store, func(context.Context, string, string, string) error {
		originalGrants++
		*client = *replacement
		return nil
	})
	if err := async.RestoreGroup(client, graph.ID).Revoke(context.Background()); err != nil || originalCommands != 1 || replacementCommands != 0 || originalGrants != 2 {
		t.Fatal("client ports/grants retargeted", originalCommands, replacementCommands, originalGrants, err)
	}
}

func TestGroupBoundaryWaitRejectsLayoutChangesWithGenuinePartialResults(t *testing.T) {
	for _, mode := range []string{"scope", "children", "kind"} {
		for _, streaming := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "join", true: "stream"}[streaming], func(t *testing.T) {
				graph := groupBoundaryGraph(t, 2)
				graph.State = async.Running
				delete(graph.Members, graph.Children[1].ID)
				reads := 0
				store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
					reads++
					if reads == 2 {
						switch mode {
						case "scope":
							graph.Scope = "other"
							for i := range graph.Children {
								graph.Children[i].Scope = "other"
								graph.Signatures[i].Options.Scope = "other"
							}
						case "children":
							graph.Children[1].ID = async.StableID(graph.ID, "new member")
						case "kind":
							graph.Kind = "chain"
						}
						graph.State = async.Succeeded
						graph.Members[graph.Children[1].ID] = async.Completion{ID: graph.Children[1].ID, State: async.Succeeded, Output: json.RawMessage("1")}
					}
					return graph, nil
				}}
				group := async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error { return nil }), graph.ID)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if streaming {
					out, err := group.JoinOutcomes(ctx)
					want := async.ErrUnavailable
					if mode == "kind" {
						want = async.ErrInvalid
					}
					if err != want || len(out) != 1 || out[0].ID != graph.Children[0].ID {
						t.Fatal(len(out), err)
					}
				} else if out, err := group.Join(ctx); err != async.ErrUnavailable || out != nil {
					t.Fatal(len(out), err)
				}
			})
		}
	}
}

func TestGroupBoundaryChildRecoveryRechecksGraphAfterChildGrant(t *testing.T) {
	for _, cancelOnChild := range []bool{false, true} {
		t.Run(map[bool]string{false: "deny", true: "cancel"}[cancelOnChild], func(t *testing.T) {
			graph := groupBoundaryGraph(t, 1)
			child := graph.Children[0]
			graph.Members[child.ID] = async.Completion{ID: child.ID, State: async.Succeeded}
			record := async.Record{Envelope: child, Digest: child.Digest(), Revision: 1, State: async.Succeeded, Output: json.RawMessage("1")}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			allowed := true
			backend := fakes.NewMemory()
			store := &resultBoundaryStore{ResultStore: backend, lookup: func(context.Context, string) (async.Record, error) { return record, nil }}
			client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: store, Workflows: &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}, Authorize: func(_ context.Context, _, _, id string) error {
				if id == child.ID {
					allowed = false
					if cancelOnChild {
						cancel()
					}
					return nil
				}
				if !allowed {
					return async.ErrDenied
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := async.RestoreGroup(client, graph.ID).JoinOutcomes(ctx)
			want := async.ErrDenied
			if cancelOnChild {
				want = context.Canceled
			}
			if err != want || len(out) != 0 {
				t.Fatal("child grant released unauthorized graph member", len(out), err)
			}
		})
	}
}

func (s *groupBoundaryStore) ReadGraph(ctx context.Context, id string) (async.Graph, error) {
	return s.read(ctx, id)
}
func (s *groupBoundaryStore) CancelGraph(ctx context.Context, id, scope string, advance func(async.Graph) (async.Graph, []async.Intent, error)) error {
	return s.cancel(ctx, id, scope, advance)
}

func groupBoundaryGraph(t *testing.T, count int) async.Graph {
	t.Helper()
	id, err := async.NewID()
	if err != nil {
		t.Fatal(err)
	}
	graph := async.Graph{ID: id, Kind: "group", Scope: "tenant", State: async.Succeeded, Revision: 1, Members: map[string]async.Completion{}}
	outputs := make([]json.RawMessage, count)
	for i := 0; i < count; i++ {
		child := async.Envelope{ID: async.StableID(id, string(rune('a'+i))), ProtocolVersion: 1, Task: "result.task", Version: 1, Queue: "default", Args: json.RawMessage("1"), CreatedAt: time.Now().UTC(), Scope: graph.Scope, WorkflowID: id, RootID: id, ParentID: id}
		graph.Children = append(graph.Children, child)
		graph.Signatures = append(graph.Signatures, async.Signature{Task: child.Task, Version: child.Version, Args: child.Args, Options: async.DispatchOptions{Scope: graph.Scope}})
		graph.Members[child.ID] = async.Completion{ID: child.ID, State: async.Succeeded, Output: json.RawMessage("1")}
		outputs[i] = json.RawMessage("1")
	}
	graph.Output, err = json.Marshal(outputs)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func TestGroupBoundaryWrongIdentityAndReadFailures(t *testing.T) {
	for _, mode := range []string{"identity", "cancel", "error"} {
		t.Run(mode, func(t *testing.T) {
			graph := groupBoundaryGraph(t, 1)
			id := graph.ID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
				if mode == "identity" {
					graph.ID = async.StableID(id, "wrong")
				}
				if mode == "cancel" {
					cancel()
				}
				if mode == "error" {
					return graph, errors.New("private provider error")
				}
				return graph, nil
			}}
			group := async.RestoreGroup(outcomeClient(t, store, nil), id)
			want := async.ErrUnavailable
			if mode == "cancel" {
				want = context.Canceled
			}
			if out, err := group.Snapshot(ctx); !errors.Is(err, want) || !reflect.DeepEqual(out, async.Graph{}) {
				t.Fatal("invalid read disclosed data", out.ID, err)
			}
		})
	}
}

func TestGroupBoundaryInvalidHandlesDoNotRead(t *testing.T) {
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
		t.Fatal("invalid handle read provider")
		return async.Graph{}, nil
	}}
	client := outcomeClient(t, store, nil)
	var absent *async.GroupResult
	for _, group := range []*async.GroupResult{absent, async.RestoreGroup(nil, async.StableID("fixture", "nil client")), async.RestoreGroup(client, "not-an-id")} {
		if _, err := group.Snapshot(context.Background()); err != async.ErrInvalid {
			t.Fatal(err)
		}
		if _, err := group.Join(context.Background()); err != async.ErrInvalid {
			t.Fatal(err)
		}
		if err := group.Revoke(context.Background()); err != async.ErrInvalid {
			t.Fatal(err)
		}
		if out, err := group.JoinOutcomes(context.Background()); err != async.ErrInvalid || len(out) != 0 {
			t.Fatal(out, err)
		}
	}
	group := async.RestoreGroup(client, async.StableID("fixture", "valid"))
	if _, err := group.Snapshot(nil); err != async.ErrInvalid {
		t.Fatal(err)
	}
	if _, err := group.Join(nil); err != async.ErrInvalid {
		t.Fatal(err)
	}
	if out, err := group.JoinOutcomes(nil); err != async.ErrInvalid || len(out) != 0 {
		t.Fatal(out, err)
	}
}

func TestGroupBoundaryJoinCollectionShapeAndScalarOutput(t *testing.T) {
	for _, value := range []string{"null", "1", "{}", "[]", "[1,2]"} {
		graph := groupBoundaryGraph(t, 1)
		graph.Output = json.RawMessage(value)
		group := async.RestoreGroup(outcomeClient(t, &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}, nil), graph.ID)
		if out, err := group.Join(context.Background()); err != async.ErrUnavailable || out != nil {
			t.Fatal("invalid collection accepted", value, out, err)
		}
		graph.Kind, graph.Next = "chain", 1
		if out, err := group.Join(context.Background()); err != nil || len(out) != 1 || string(out[0]) != value {
			t.Fatal("scalar chain reinterpreted", value, out, err)
		}
	}
}

func TestGroupBoundaryAcceptedSignatureMetadataRemainsReadable(t *testing.T) {
	for _, size := range []int{async.MaxPayloadBytes - 100, async.MaxPayloadBytes + 100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx := context.Background()
			task, client, _, _ := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
			signature, err := task.Signature(1)
			if err != nil {
				t.Fatal(err)
			}
			// A flat group retains unused parent binding metadata under the graph
			// budget. It is not part of the independently bounded child envelope.
			signature.ParentField = strings.Repeat("x", size)
			// Original IDs are also retained only as signature metadata: the
			// compiler replaces the dispatch identity with its own stable ID.
			signature.Options.ID = strings.Repeat("y", size)
			group, err := client.ApplyCanvas(ctx, async.Group(signature))
			if err != nil {
				t.Fatal("workflow was not accepted", err)
			}
			graph, err := group.Snapshot(ctx)
			if err != nil || len(graph.Signatures) != 1 || graph.Signatures[0].ParentField != signature.ParentField || graph.Signatures[0].Options.ID != signature.Options.ID {
				t.Fatal("accepted workflow became unreadable", err)
			}
		})
	}
}

func TestGroupBoundaryRevokeFreezesAuthorizedIdentity(t *testing.T) {
	for _, mutateAt := range []string{"read", "revoke"} {
		graph := groupBoundaryGraph(t, 1)
		graph.State = async.Running
		var group *async.GroupResult
		store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
		var target string
		store.cancel = func(_ context.Context, id, scope string, _ func(async.Graph) (async.Graph, []async.Intent, error)) error {
			target = id
			return nil
		}
		var grants []string
		client := outcomeClient(t, store, func(_ context.Context, action, _, id string) error {
			grants = append(grants, id)
			if action == mutateAt {
				group.ID = async.StableID(graph.ID, "other")
			}
			return nil
		})
		group = async.RestoreGroup(client, graph.ID)
		if err := group.Revoke(context.Background()); err != nil || target != graph.ID || !reflect.DeepEqual(grants, []string{graph.ID, graph.ID}) {
			t.Fatal("callback retargeted revoke", mutateAt, target, grants, err)
		}
	}
}

func TestGroupBoundarySnapshotDetachesBeforeAuthorization(t *testing.T) {
	graph := groupBoundaryGraph(t, 1)
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
	client := outcomeClient(t, store, func(context.Context, string, string, string) error {
		graph.Output[1] = '9'
		graph.Children[0].Args[0] = '9'
		graph.Members[async.StableID(graph.ID, "other")] = async.Completion{}
		return nil
	})
	out, err := async.RestoreGroup(client, graph.ID).Snapshot(context.Background())
	if err != nil || string(out.Output) != "[1]" || string(out.Children[0].Args) != "1" || len(out.Members) != 1 {
		t.Fatal("provider aliases changed authorized graph", string(out.Output), len(out.Members), err)
	}
}

func TestGroupBoundaryJoinFreezesWholeHandle(t *testing.T) {
	graph := groupBoundaryGraph(t, 1)
	var group, replacement *async.GroupResult
	reads := 0
	store := &groupBoundaryStore{read: func(_ context.Context, id string) (async.Graph, error) {
		if id != graph.ID {
			t.Fatal("join changed identity", id)
		}
		reads++
		value := graph
		if reads == 1 {
			value.State = async.Running
		}
		return value, nil
	}}
	client := outcomeClient(t, store, func(context.Context, string, string, string) error { *group = *replacement; return nil })
	other := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return async.Graph{}, errors.New("wrong client") }}
	replacement = async.RestoreGroup(outcomeClient(t, other, nil), async.StableID(graph.ID, "other"))
	group = async.RestoreGroup(client, graph.ID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := group.Join(ctx); err != nil || len(out) != 1 || string(out[0]) != "1" || reads != 2 {
		t.Fatal(out, reads, err)
	}
}

func TestGroupBoundaryJoinNeverInventsMissingOutputOrSuccess(t *testing.T) {
	for _, state := range []async.State{async.Succeeded, async.Revoked} {
		graph := groupBoundaryGraph(t, 1)
		graph.Kind, graph.State, graph.Next, graph.Output = "chain", state, 1, nil
		store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
		out, err := async.RestoreGroup(outcomeClient(t, store, nil), graph.ID).Join(context.Background())
		if err == nil || out != nil || state == async.Succeeded && !errors.Is(err, async.ErrResultExpired) {
			t.Fatal("missing result fabricated", state, out, err)
		}
	}
}

func TestGroupBoundaryIterationDoesNotYieldAfterCanceledGrant(t *testing.T) {
	for _, stopAfter := range []int{0, 1} {
		graph := groupBoundaryGraph(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
		client := outcomeClient(t, store, func(context.Context, string, string, string) error {
			calls++
			if calls == 2+stopAfter {
				cancel()
			}
			return nil
		})
		seen, failures := 0, 0
		for _, err := range async.RestoreGroup(client, graph.ID).Iterate(ctx) {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				failures++
				continue
			}
			seen++
		}
		if seen != stopAfter || failures != 1 {
			t.Fatal("canceled authorization still yielded", stopAfter, seen, failures)
		}
	}
}
