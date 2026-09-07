package async_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

type queueInspectionBroker struct {
	async.Broker
	inspect func(context.Context, []string) ([]async.QueueStats, error)
}

func (b queueInspectionBroker) Inspect(ctx context.Context, queues []string) ([]async.QueueStats, error) {
	return b.inspect(ctx, queues)
}

func TestQueueInspectionRejectsUnscopedAndUnavailableProviderPayloads(t *testing.T) {
	for _, scenario := range []string{"wrong-queue", "partial-error", "revoked-after-read"} {
		t.Run(scenario, func(t *testing.T) {
			read := false
			client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
				config.Authorize = func(_ context.Context, action, scope, id string) error {
					if action != "inspect" || scope != "default" || id != "" {
						t.Fatal("wrong queue inspection grant", action, scope, id)
					}
					if read && scenario == "revoked-after-read" {
						return async.ErrDenied
					}
					return nil
				}
				config.Broker = queueInspectionBroker{Broker: config.Broker, inspect: func(context.Context, []string) ([]async.QueueStats, error) {
					read = true
					if scenario == "wrong-queue" {
						return []async.QueueStats{{Queue: "hidden", Queued: 42}}, nil
					}
					rows := []async.QueueStats{{Queue: "default", Queued: 42}}
					if scenario == "partial-error" {
						return rows, errors.New("private provider connection")
					}
					return rows, nil
				}}
			})
			rows, err := (async.Control{Client: client}).InspectQueues(context.Background(), []string{"default"})
			want := async.ErrUnavailable
			if scenario == "revoked-after-read" {
				want = async.ErrDenied
			}
			if len(rows) != 0 || !errors.Is(err, want) || strings.Contains(err.Error(), "private") {
				t.Fatal("queue inspection disclosed an unauthorized/unavailable payload", rows, err)
			}
		})
	}
}

func TestLocalWorkerInspectionRequiresEveryQueueGrant(t *testing.T) {
	client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
		config.Authorize = func(_ context.Context, action, scope, id string) error {
			if action != "inspect" || id != "worker" {
				t.Fatal("wrong worker inspection grant", action, scope, id)
			}
			if scope == "hidden" {
				return async.ErrDenied
			}
			return nil
		}
	})
	worker := &async.Worker{ID: "worker", Queues: []string{"default", "hidden"}, Registry: async.NewRegistry(), Concurrency: 1}
	row, err := (async.Control{Client: client}).InspectWorker(context.Background(), worker)
	if row.ID != "" || len(row.Queues) != 0 || len(row.Registered) != 0 || !errors.Is(err, async.ErrDenied) {
		t.Fatal("local worker inspection bypassed queue authority", row, err)
	}
}

func TestQueueInspectionValidatesWholeTargetSetBeforeCallbacks(t *testing.T) {
	calls := 0
	client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
		config.Authorize = func(context.Context, string, string, string) error { calls++; return nil }
		config.Broker = queueInspectionBroker{Broker: config.Broker, inspect: func(context.Context, []string) ([]async.QueueStats, error) { calls++; return nil, nil }}
	})
	for _, queues := range [][]string{nil, {}, {"default", "default"}, {"default", "bad{slot}"}, {"default", ""}, {strings.Repeat("x", 193)}, make([]string, 65)} {
		if rows, err := (async.Control{Client: client}).InspectQueues(context.Background(), queues); len(rows) != 0 || !errors.Is(err, async.ErrInvalid) || calls != 0 {
			t.Fatal("invalid target set reached authority/provider", queues, rows, err, calls)
		}
	}
	if rows, err := (async.Control{Client: client}).InspectQueues(nil, []string{"default"}); len(rows) != 0 || !errors.Is(err, async.ErrInvalid) || calls != 0 {
		t.Fatal(rows, err, calls)
	}
	if rows, err := (async.Control{}).InspectQueues(context.Background(), []string{"default"}); len(rows) != 0 || !errors.Is(err, async.ErrInvalid) {
		t.Fatal(rows, err)
	}
}

func TestQueueInspectionDiscardsEveryMalformedOrFailedBatch(t *testing.T) {
	for _, scenario := range []string{"missing", "extra", "duplicate", "negative-queued", "negative-pending", "negative-quarantine", "provider-panic", "provider-cancel", "provider-cancel-nil", "policy-denied", "policy-error", "policy-panic", "policy-cancel", "post-policy-panic", "post-policy-error", "pre-canceled", "provider-retarget"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
				config.Authorize = func(context.Context, string, string, string) error {
					switch scenario {
					case "policy-denied":
						return async.ErrDenied
					case "policy-error":
						return errors.New("private policy backend")
					case "policy-panic":
						panic("private policy panic")
					case "policy-cancel":
						cancel()
					case "post-policy-panic":
						if reads != 0 {
							panic("private policy panic")
						}
					case "post-policy-error":
						if reads != 0 {
							return errors.New("private policy backend")
						}
					}
					return nil
				}
				config.Broker = queueInspectionBroker{Broker: config.Broker, inspect: func(_ context.Context, queues []string) ([]async.QueueStats, error) {
					reads++
					rows := []async.QueueStats{{Queue: "default"}, {Queue: "second"}}
					switch scenario {
					case "missing":
						rows = rows[:1]
					case "extra":
						rows = append(rows, async.QueueStats{Queue: "hidden"})
					case "duplicate":
						rows[1].Queue = "default"
					case "negative-queued":
						rows[1].Queued = -1
					case "negative-pending":
						rows[1].Pending = -1
					case "negative-quarantine":
						rows[1].Quarantined = -1
					case "provider-panic":
						panic("private provider panic")
					case "provider-cancel":
						return rows, context.Canceled
					case "provider-cancel-nil":
						cancel()
					case "provider-retarget":
						queues[1] = "hidden"
						rows[1].Queue = "hidden"
					}
					return rows, nil
				}}
			})
			if scenario == "pre-canceled" {
				cancel()
			}
			targets := []string{"default", "second"}
			rows, err := (async.Control{Client: client}).InspectQueues(ctx, targets)
			want := async.ErrUnavailable
			if strings.Contains(scenario, "cancel") {
				want = context.Canceled
			}
			if scenario == "policy-denied" {
				want = async.ErrDenied
			}
			if len(rows) != 0 || !errors.Is(err, want) || strings.Contains(err.Error(), "private") || targets[1] != "second" {
				t.Fatal("failed batch leaked or changed its caller", rows, err, targets)
			}
			if strings.HasPrefix(scenario, "policy-") || scenario == "pre-canceled" {
				if reads != 0 {
					t.Fatal("rejected authority invoked provider")
				}
			}
		})
	}
}

func TestQueueInspectionReturnsDetachedRequestedOrder(t *testing.T) {
	providerRows := []async.QueueStats{{Queue: "second", Queued: 2}, {Queue: "default", Queued: 1}}
	reads := 0
	client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
		config.Authorize = func(context.Context, string, string, string) error {
			if reads > 0 {
				providerRows[0].Queued = 99
			}
			return nil
		}
		config.Broker = queueInspectionBroker{Broker: config.Broker, inspect: func(context.Context, []string) ([]async.QueueStats, error) { reads++; return providerRows, nil }}
	})
	rows, err := (async.Control{Client: client}).InspectQueues(context.Background(), []string{"default", "second"})
	if err != nil || !reflect.DeepEqual(rows, []async.QueueStats{{Queue: "default", Queued: 1}, {Queue: "second", Queued: 2}}) {
		t.Fatal("postlookup callback changed validated statistics or provider order leaked", rows, err)
	}
	rows[1].Queued = 0
	if providerRows[0].Queued != 99 {
		t.Fatal("caller received provider slice alias")
	}
}

func TestLocalWorkerInspectionValidatesSnapshotAndSafeErrors(t *testing.T) {
	for _, scenario := range []string{"nil-worker", "nil-context", "invalid-id", "duplicate-queue", "missing-queue", "bad-concurrency", "identity-changed", "panic", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			worker := &async.Worker{ID: "worker", Queues: []string{"default"}, Concurrency: 1}
			client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
				config.Authorize = func(context.Context, string, string, string) error {
					switch scenario {
					case "identity-changed":
						worker.ID = "replacement"
					case "panic":
						panic("private authorization details")
					case "cancel":
						cancel()
					}
					return nil
				}
			})
			want := async.ErrUnavailable
			switch scenario {
			case "nil-worker":
				worker = nil
				want = async.ErrInvalid
			case "nil-context":
				ctx = nil
				want = async.ErrInvalid
			case "invalid-id":
				worker.ID = "bad{slot}"
				want = async.ErrInvalid
			case "duplicate-queue":
				worker.Queues = []string{"default", "default"}
			case "missing-queue":
				worker.Queues = nil
			case "bad-concurrency":
				worker.Concurrency = 1025
			case "cancel":
				want = context.Canceled
			}
			row, err := (async.Control{Client: client}).InspectWorker(ctx, worker)
			if !reflect.DeepEqual(row, async.WorkerSnapshot{}) || !errors.Is(err, want) || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid snapshot or unsafe error released", row, err)
			}
		})
	}
}

func TestLocalWorkerInspectionHidesDeniedActivityButNotAuthorityFailures(t *testing.T) {
	ctx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	started, finish := make(chan struct{}), make(chan struct{})
	task, producer, worker, _ := setup(t, func(ctx context.Context, _ async.TaskContext, value int) (int, error) {
		close(started)
		select {
		case <-finish:
			return value, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	result, err := task.Delay(ctx, producer, 42)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.RunOnce(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	defer func() {
		close(finish)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for _, scenario := range []string{"visible", "denied", "outage", "cancel", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			inspectCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
				config.Authorize = func(_ context.Context, action, scope, id string) error {
					if action != "inspect" {
						t.Fatal("wrong inspection operation", action)
					}
					if id != result.Receipt.ID {
						return nil
					}
					switch scenario {
					case "denied":
						return async.ErrDenied
					case "outage":
						return errors.New("private activity policy")
					case "cancel":
						cancel()
					case "panic":
						panic("private activity policy")
					}
					return nil
				}
			})
			row, err := (async.Control{Client: client}).InspectWorker(inspectCtx, worker)
			if scenario == "visible" {
				if err != nil || len(row.Active) != 1 || row.Active[0].ID != result.Receipt.ID {
					t.Fatal(row, err)
				}
				row.Active[0].ID, row.Queues[0], row.Registered[0] = "changed", "changed", "changed"
				fresh := worker.Snapshot()
				if fresh.Active[0].ID != result.Receipt.ID || fresh.Queues[0] != "default" || fresh.Registered[0] != "test.add@1" {
					t.Fatal("caller mutated live worker snapshot", fresh)
				}
			} else if scenario == "denied" {
				if err != nil || row.ID != worker.ID || len(row.Active) != 0 {
					t.Fatal("denied task was not hidden", row, err)
				}
			} else {
				want := async.ErrUnavailable
				if scenario == "cancel" {
					want = context.Canceled
				}
				if !reflect.DeepEqual(row, async.WorkerSnapshot{}) || !errors.Is(err, want) || strings.Contains(err.Error(), "private") {
					t.Fatal("policy failure fabricated empty work", row, err)
				}
			}
		})
	}
}
