package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type resultBoundaryStore struct {
	async.ResultStore
	lookup func(context.Context, string) (async.Record, error)
	forget func(context.Context, string) error
	revoke func(context.Context, string, string) error
}

func (s *resultBoundaryStore) Lookup(ctx context.Context, id string) (async.Record, error) {
	return s.lookup(ctx, id)
}
func (s *resultBoundaryStore) Forget(ctx context.Context, id string) error {
	return s.forget(ctx, id)
}
func (s *resultBoundaryStore) RequestCancel(ctx context.Context, id, scope string) error {
	return s.revoke(ctx, id, scope)
}

func resultBoundaryFixture(t *testing.T, authorize func(context.Context, string, string, string) error) (*async.Result[int], *resultBoundaryStore, async.Record) {
	t.Helper()
	memory := fakes.NewMemory()
	store := &resultBoundaryStore{ResultStore: memory, lookup: memory.Lookup, forget: memory.Forget, revoke: memory.RequestCancel}
	registry := async.NewRegistry()
	task, err := async.Register(registry, "result.task", 1, func(context.Context, async.TaskContext, int) (int, error) { return 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Results: store, Broker: memory, Authorize: authorize})
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Delay(context.Background(), client, 1, async.WithScope("tenant", ""))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.Lookup(context.Background(), result.Receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return result, store, record
}

func TestResultBoundaryRejectsWrongLookupIdentity(t *testing.T) {
	result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
	record.Envelope.ID = async.StableID("wrong", "task")
	record.Digest = record.Envelope.Digest()
	store.lookup = func(context.Context, string) (async.Record, error) { return record, nil }
	if out, err := result.Snapshot(context.Background()); !errors.Is(err, async.ErrUnavailable) || !reflect.DeepEqual(out, async.Record{}) {
		t.Fatal("wrong task response disclosed", out.Envelope.ID, err)
	}
}

func TestResultBoundaryFreezesCommandIdentity(t *testing.T) {
	for _, operation := range []string{"forget", "revoke"} {
		for _, changeAt := range []string{"read", operation} {
			t.Run(operation+"/"+changeAt, func(t *testing.T) {
				var result *async.Result[int]
				other := async.StableID("other", "task")
				var grants []string
				authorize := func(_ context.Context, action, _, id string) error {
					if result != nil {
						grants = append(grants, id)
						if action == changeAt {
							result.Receipt.ID = other
						}
					}
					return nil
				}
				var store *resultBoundaryStore
				result, store, _ = resultBoundaryFixture(t, authorize)
				original, target := result.Receipt.ID, ""
				store.forget = func(_ context.Context, id string) error { target = id; return nil }
				store.revoke = func(_ context.Context, id, scope string) error { target = id; return nil }
				command := result.Forget
				if operation == "revoke" {
					command = result.Revoke
				}
				if err := command(context.Background()); err != nil || target != original || !reflect.DeepEqual(grants, []string{original, original}) {
					t.Fatal("authorization callback retargeted command", original, target, grants, err)
				}
			})
		}
	}
}

func TestResultBoundaryDetachesBeforeAuthorization(t *testing.T) {
	var retained async.Record
	result, store, record := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
		if action == "read" {
			retained.Envelope.Headers["label"] = "changed"
			retained.Output[0] = '9'
		}
		return nil
	})
	record.State, record.Output = async.Succeeded, json.RawMessage("1")
	record.Envelope.Headers = map[string]string{"label": "original"}
	record.Digest = record.Envelope.Digest()
	record.FinishedAt = time.Now().UTC()
	record.PayloadExpiresAt, record.TombstoneUntil = record.FinishedAt.Add(time.Hour), record.FinishedAt.Add(24*time.Hour)
	retained = record
	store.lookup = func(context.Context, string) (async.Record, error) { return record, nil }
	out, err := result.Snapshot(context.Background())
	if err != nil || out.Envelope.Headers["label"] != "original" || string(out.Output) != "1" {
		t.Fatal("provider alias changed authorized output", out.Envelope.Headers, string(out.Output), err)
	}
	out.Envelope.Headers["label"] = "caller"
	if retained.Envelope.Headers["label"] != "changed" {
		t.Fatal("returned metadata still aliases provider")
	}
}

func TestResultBoundaryLateCanceledReadAndProviderError(t *testing.T) {
	for _, reason := range []string{"cancel", "error"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
			store.lookup = func(context.Context, string) (async.Record, error) {
				if reason == "cancel" {
					cancel()
					return record, nil
				}
				return record, errors.New("private provider diagnostic")
			}
			want := async.ErrUnavailable
			if reason == "cancel" {
				want = context.Canceled
			}
			if out, err := result.Snapshot(ctx); !errors.Is(err, want) || !reflect.DeepEqual(out, async.Record{}) {
				t.Fatal("read failure disclosed state or diagnostic", out.State, err)
			}
		})
	}
}

func terminalBoundaryRecord(record async.Record) async.Record {
	record.State, record.Output = async.Succeeded, json.RawMessage("1")
	record.FinishedAt = time.Now().UTC()
	record.PayloadExpiresAt, record.TombstoneUntil = record.FinishedAt.Add(time.Hour), record.FinishedAt.Add(24*time.Hour)
	return record
}

func TestResultBoundaryWaitFreezesWholeHandle(t *testing.T) {
	var result, replacement *async.Result[int]
	result, store, record := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
		if action == "read" {
			*result = *replacement
		}
		return nil
	})
	var otherStore *resultBoundaryStore
	replacement, otherStore, _ = resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
	otherStore.lookup = func(context.Context, string) (async.Record, error) {
		t.Fatal("wait switched client")
		return async.Record{}, nil
	}
	id, calls := result.Receipt.ID, 0
	store.lookup = func(_ context.Context, requested string) (async.Record, error) {
		if requested != id {
			t.Fatal("wait switched task", requested)
		}
		calls++
		if calls > 1 {
			return terminalBoundaryRecord(record), nil
		}
		return record, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := result.Wait(ctx); err != nil || out != 1 || calls != 2 {
		t.Fatal(out, calls, err)
	}
}

func TestResultBoundaryRejectsMalformedMetadataBeforeAuthority(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*async.Record)
	}{
		{"revision", func(r *async.Record) { r.Revision = 0 }},
		{"state", func(r *async.Record) { r.State = async.Unknown }},
		{"negative-delivery-count", func(r *async.Record) { r.DeliveryCount = -1 }},
		{"digest", func(r *async.Record) { r.Digest = "wrong" }},
		{"scope", func(r *async.Record) { r.Envelope.Scope = string([]byte{255}) }},
		{"args", func(r *async.Record) { r.Envelope.Args = json.RawMessage("invalid") }},
		{"replacement", func(r *async.Record) { r.ReplacementID = "invalid" }},
		{"output", func(r *async.Record) { r.Output = json.RawMessage("invalid") }},
		{"output-utf8", func(r *async.Record) { r.Output = json.RawMessage{'"', 255, '"'} }},
		{"output-size", func(r *async.Record) { r.Output = json.RawMessage(strings.Repeat("0", async.MaxPayloadBytes+1)) }},
		{"progress", func(r *async.Record) { r.Progress = json.RawMessage("invalid") }},
		{"progress-size", func(r *async.Record) { r.Progress = json.RawMessage(strings.Repeat("0", (16<<10)+1)) }},
		{"failure-size", func(r *async.Record) { r.Failure = &async.Failure{Code: "FAIL", Message: strings.Repeat("x", 4097)} }},
		{"failure-utf8", func(r *async.Record) { r.Failure = &async.Failure{Code: string([]byte{255})} }},
		{"contradictory-success", func(r *async.Record) { r.State = async.Succeeded; r.Failure = &async.Failure{Code: "FAIL"} }},
		{"cyclic-links", func(r *async.Record) {
			links := make([]async.Signature, 1)
			links[0].Callbacks = links
			r.Envelope.Callbacks = links
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			result, store, record := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
				if action == "read" {
					reads++
				}
				return nil
			})
			tc.alter(&record)
			store.lookup = func(context.Context, string) (async.Record, error) { return record, nil }
			if out, err := result.Snapshot(context.Background()); !errors.Is(err, async.ErrUnavailable) || !reflect.DeepEqual(out, async.Record{}) || reads != 0 {
				t.Fatal("malformed metadata reached caller or authority", out.State, err, reads)
			}
		})
	}
}

func TestResultBoundaryInvalidHandlesAndReadPanics(t *testing.T) {
	result, store, _ := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
	store.lookup = func(context.Context, string) (async.Record, error) {
		t.Fatal("invalid handle reached lookup")
		return async.Record{}, nil
	}
	for _, handle := range []*async.Result[int]{nil, {}, async.RestoreResult[int](nil, result.Receipt.ID)} {
		if _, err := handle.Snapshot(context.Background()); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := handle.Get(context.Background()); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(err)
		}
		if err := handle.Forget(context.Background()); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := result.Snapshot(nil); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	result.Receipt.ID = "bad"
	if _, err := result.Snapshot(context.Background()); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	for _, at := range []string{"lookup", "authorize"} {
		result, store, record := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
			if action == "read" && at == "authorize" {
				panic("private callback diagnostic")
			}
			return nil
		})
		store.lookup = func(context.Context, string) (async.Record, error) {
			if at == "lookup" {
				panic("private provider diagnostic")
			}
			return record, nil
		}
		if out, err := result.Snapshot(context.Background()); !errors.Is(err, async.ErrUnavailable) || !reflect.DeepEqual(out, async.Record{}) {
			t.Fatal(out.State, err)
		}
	}
}

func TestResultBoundaryCommandsPreservePossibleAppliedOutcomes(t *testing.T) {
	for _, operation := range []string{"forget", "revoke"} {
		for _, failure := range []string{"confirmed-cancel", "error", "panic", "joined-no-write"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if operation == "forget" {
					claim, err := store.ResultStore.Claim(ctx, record.Envelope, "worker", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					if err := store.ResultStore.Transition(ctx, async.Transition{ID: result.Receipt.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage("1")}); err != nil {
						t.Fatal(err)
					}
				}
				afterWrite := func() error {
					switch failure {
					case "confirmed-cancel":
						cancel()
						return nil
					case "panic":
						panic("private after-write diagnostic")
					case "joined-no-write":
						return errors.Join(async.ErrPinned, errors.New("private after-write diagnostic"))
					default:
						return errors.New("private after-write diagnostic")
					}
				}
				store.forget = func(ctx context.Context, id string) error {
					if err := store.ResultStore.Forget(ctx, id); err != nil {
						return err
					}
					return afterWrite()
				}
				store.revoke = func(ctx context.Context, id, scope string) error {
					if err := store.ResultStore.RequestCancel(ctx, id, scope); err != nil {
						return err
					}
					return afterWrite()
				}
				command := result.Forget
				if operation == "revoke" {
					command = result.Revoke
				}
				err := command(ctx)
				if failure == "confirmed-cancel" && err != nil || failure != "confirmed-cancel" && !errors.Is(err, async.ErrUnavailable) {
					t.Fatal(err)
				}
				stored, err := store.ResultStore.Lookup(context.Background(), result.Receipt.ID)
				if err != nil || operation == "forget" && len(stored.Output) != 0 || operation == "revoke" && !stored.CancelRequested {
					t.Fatal("write fixture did not apply", stored.State, err)
				}
			})
		}
	}
}

func TestResultBoundaryCommandCancellationBeforeProvider(t *testing.T) {
	for _, at := range []string{"read", "forget", "revoke"} {
		ctx, cancel := context.WithCancel(context.Background())
		result, store, _ := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
			if action == at {
				cancel()
			}
			return nil
		})
		store.forget = func(context.Context, string) error { t.Fatal("canceled command invoked provider"); return nil }
		store.revoke = func(context.Context, string, string) error { t.Fatal("canceled command invoked provider"); return nil }
		command := result.Forget
		if at == "revoke" {
			command = result.Revoke
		}
		if err := command(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(at, err)
		}
		cancel()
	}
}

func TestResultBoundaryReadStatesAndSafeErrors(t *testing.T) {
	result, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
	for _, want := range []error{async.ErrNotFound, async.ErrResultExpired, async.ErrDenied, context.Canceled, context.DeadlineExceeded} {
		store.lookup = func(context.Context, string) (async.Record, error) { return record, want }
		if out, err := result.Snapshot(context.Background()); err != want || !reflect.DeepEqual(out, async.Record{}) {
			t.Fatal(out.State, err)
		}
	}
	store.lookup = func(context.Context, string) (async.Record, error) { return record, nil }
	if ready, err := result.Ready(context.Background()); err != nil || ready {
		t.Fatal(ready, err)
	}
	record = terminalBoundaryRecord(record)
	if ready, err := result.Successful(context.Background()); err != nil || !ready {
		t.Fatal(ready, err)
	}
	record.Output = nil
	if out, err := result.Get(context.Background()); !errors.Is(err, async.ErrResultExpired) || out != 0 {
		t.Fatal(out, err)
	}
	record.State, record.Failure = async.Failed, &async.Failure{Code: "FAILED", Message: "Safe task failure"}
	if failed, err := result.Failed(context.Background()); err != nil || !failed {
		t.Fatal(failed, err)
	}
	if out, err := result.Get(context.Background()); out != 0 || err == nil || err.Error() != "FAILED: Safe task failure" {
		t.Fatal(out, err)
	}
}

func TestResultBoundaryCommandFreezesClient(t *testing.T) {
	for _, operation := range []string{"forget", "revoke"} {
		var result, replacement *async.Result[int]
		result, store, _ := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
			if action == operation {
				*result = *replacement
			}
			return nil
		})
		var other *resultBoundaryStore
		replacement, other, _ = resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
		other.forget = func(context.Context, string) error { t.Fatal("command switched client"); return nil }
		other.revoke = func(context.Context, string, string) error { t.Fatal("command switched client"); return nil }
		original, calls := result.Receipt.ID, 0
		store.forget = func(_ context.Context, id string) error {
			if id != original {
				t.Fatal(id)
			}
			calls++
			return nil
		}
		store.revoke = func(_ context.Context, id, scope string) error {
			if id != original || scope != "tenant" {
				t.Fatal(id, scope)
			}
			calls++
			return nil
		}
		command := result.Forget
		if operation == "revoke" {
			command = result.Revoke
		}
		if err := command(context.Background()); err != nil || calls != 1 {
			t.Fatal(calls, err)
		}
	}
}

type resultDecodeError struct{ Value int }

func (r *resultDecodeError) UnmarshalJSON([]byte) error {
	r.Value = 1
	return errors.New("private decoder error")
}

type resultDecodePanic struct{ Value int }

func (r *resultDecodePanic) UnmarshalJSON([]byte) error { r.Value = 1; panic("private decoder panic") }

func TestResultBoundaryDecoderFailuresReturnZeroOutput(t *testing.T) {
	task, client, worker, memory := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 1, nil }, async.TaskOptions{})
	result, err := task.Delay(context.Background(), client, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), take(t, memory)); err != nil {
		t.Fatal(err)
	}
	if out, err := async.RestoreResult[resultDecodeError](client, result.Receipt.ID).Get(context.Background()); !errors.Is(err, async.ErrUnavailable) || out.Value != 0 {
		t.Fatal(out, err)
	}
	if out, err := async.RestoreResult[resultDecodePanic](client, result.Receipt.ID).Get(context.Background()); !errors.Is(err, async.ErrUnavailable) || out.Value != 0 {
		t.Fatal(out, err)
	}
}

func TestResultBoundaryReadDenialAndPinnedForget(t *testing.T) {
	for _, deniedAt := range []string{"read", "forget", "revoke"} {
		result, store, _ := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
			if action == deniedAt {
				return errors.New("private authorization diagnostic")
			}
			return nil
		})
		store.forget = func(context.Context, string) error { t.Fatal("denied command reached provider"); return nil }
		store.revoke = func(context.Context, string, string) error { t.Fatal("denied command reached provider"); return nil }
		command := result.Forget
		if deniedAt == "revoke" {
			command = result.Revoke
		}
		if err := command(context.Background()); err != async.ErrDenied {
			t.Fatal(err)
		}
	}
	result, _, _ := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
	if err := result.Forget(context.Background()); err != async.ErrPinned {
		t.Fatal("pending task retention was not preserved", err)
	}
}

func TestResultBoundaryTimestampLocationsAreDetached(t *testing.T) {
	var providerLocation *time.Location
	result, store, record := resultBoundaryFixture(t, func(_ context.Context, action, _, _ string) error {
		if action == "read" {
			*providerLocation = *time.FixedZone("changed-provider-zone", 7200)
		}
		return nil
	})
	// Unnamed integral-hour locations use a shared standard-library parse cache.
	// Restore it even on test failure; no global timezone policy is modified.
	providerLocation = time.FixedZone("", 3600)
	original := *providerLocation
	defer func() { *providerLocation = original }()
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, providerLocation)
	link := async.Signature{Task: "result.task", Version: 1, Args: json.RawMessage("1"), Options: async.DispatchOptions{ETA: at, ExpiresAt: at}}
	link.Errbacks = []async.Signature{link}
	record.Envelope.Callbacks, record.Envelope.Errbacks = []async.Signature{link}, []async.Signature{link}
	fields := func(r *async.Record) []*time.Time {
		return []*time.Time{&r.Envelope.CreatedAt, &r.Envelope.ETA, &r.Envelope.ExpiresAt, &r.LeaseUntil, &r.FinishedAt, &r.PayloadExpiresAt, &r.TombstoneUntil, &r.ReplayUntil,
			&r.Envelope.Callbacks[0].Options.ETA, &r.Envelope.Callbacks[0].Options.ExpiresAt, &r.Envelope.Callbacks[0].Errbacks[0].Options.ETA,
			&r.Envelope.Errbacks[0].Options.ETA, &r.Envelope.Errbacks[0].Options.ExpiresAt, &r.Envelope.Errbacks[0].Errbacks[0].Options.ExpiresAt}
	}
	for _, field := range fields(&record) {
		*field = at
	}
	record.Digest = record.Envelope.Digest()
	expected := at.Format(time.RFC3339Nano)
	store.lookup = func(context.Context, string) (async.Record, error) { return record, nil }
	out, err := result.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields(&out) {
		if field.Format(time.RFC3339Nano) != expected || out.Digest != out.Envelope.Digest() {
			t.Fatal("provider location mutation reached snapshot", field, expected)
		}
	}
	*out.Envelope.CreatedAt.Location() = *time.FixedZone("changed-returned-zone", 10800)
	if out.LeaseUntil.Format(time.RFC3339Nano) != expected || providerLocation.String() != "changed-provider-zone" {
		t.Fatal("returned timestamp locations still alias")
	}
}

func resultBoundaryClient(t *testing.T, store async.ResultStore, authorize func(context.Context, string, string, string) error) *async.Client {
	t.Helper()
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: fakes.NewMemory(), Results: store, Authorize: authorize})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestResultBoundaryFreezesClientPortsBeforeCallbacks(t *testing.T) {
	for _, operation := range []string{"forget", "revoke"} {
		for _, replaceAt := range []string{"lookup", "read", operation} {
			t.Run(operation+"/"+replaceAt, func(t *testing.T) {
				_, originalStore, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
				originalCommands, replacementCommands, replacementReads := 0, 0, 0
				var originalGrants, replacementGrants []string
				replacementStore := &resultBoundaryStore{ResultStore: originalStore.ResultStore,
					lookup: func(context.Context, string) (async.Record, error) { replacementReads++; return record, nil },
					forget: func(context.Context, string) error { replacementCommands++; return nil },
					revoke: func(context.Context, string, string) error { replacementCommands++; return nil },
				}
				replacement := resultBoundaryClient(t, replacementStore, func(_ context.Context, action, _, _ string) error {
					replacementGrants = append(replacementGrants, action)
					return nil
				})
				var client *async.Client
				originalStore.lookup = func(context.Context, string) (async.Record, error) {
					if replaceAt == "lookup" {
						*client = *replacement
					}
					return record, nil
				}
				originalStore.forget = func(_ context.Context, id string) error {
					if id != record.Envelope.ID {
						t.Fatal("task identity changed", id)
					}
					originalCommands++
					return nil
				}
				originalStore.revoke = func(_ context.Context, id, scope string) error {
					if id != record.Envelope.ID || scope != record.Envelope.Scope {
						t.Fatal("task identity/scope changed", id, scope)
					}
					originalCommands++
					return nil
				}
				client = resultBoundaryClient(t, originalStore, func(_ context.Context, action, scope, id string) error {
					if id != record.Envelope.ID || scope != record.Envelope.Scope {
						t.Fatal("grant identity/scope changed", id, scope)
					}
					originalGrants = append(originalGrants, action)
					if action == replaceAt {
						*client = *replacement
					}
					return nil
				})
				result := async.RestoreResult[int](client, record.Envelope.ID)
				command := result.Forget
				if operation == "revoke" {
					command = result.Revoke
				}
				if err := command(context.Background()); err != nil || originalCommands != 1 || replacementCommands != 0 || replacementReads != 0 || len(replacementGrants) != 0 || !reflect.DeepEqual(originalGrants, []string{"read", operation}) {
					t.Fatal("client replacement retargeted an in-flight command", originalCommands, replacementCommands, replacementReads, originalGrants, replacementGrants, err)
				}
				// A later operation observes the caller's replacement normally.
				if _, err := result.Snapshot(context.Background()); err != nil || replacementReads != 1 || !reflect.DeepEqual(replacementGrants, []string{"read"}) {
					t.Fatal("replacement did not affect later call", replacementReads, replacementGrants, err)
				}
			})
		}
	}
}

func TestResultBoundaryWaitFreezesClientValueAcrossPolls(t *testing.T) {
	_, store, record := resultBoundaryFixture(t, func(context.Context, string, string, string) error { return nil })
	reads, grants, replacementReads := 0, 0, 0
	store.lookup = func(context.Context, string) (async.Record, error) {
		reads++
		if reads == 1 {
			return record, nil
		}
		return terminalBoundaryRecord(record), nil
	}
	replacementStore := &resultBoundaryStore{ResultStore: store.ResultStore, lookup: func(context.Context, string) (async.Record, error) {
		replacementReads++
		out := terminalBoundaryRecord(record)
		out.Output = json.RawMessage("9")
		return out, nil
	}}
	replacement := resultBoundaryClient(t, replacementStore, func(context.Context, string, string, string) error { return nil })
	var client *async.Client
	client = resultBoundaryClient(t, store, func(context.Context, string, string, string) error { grants++; *client = *replacement; return nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := async.RestoreResult[int](client, record.Envelope.ID).Wait(ctx); err != nil || out != 1 || reads != 2 || grants != 2 || replacementReads != 0 {
		t.Fatal("wait switched client configuration", out, reads, grants, replacementReads, err)
	}
}
