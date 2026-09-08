package testing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func periodicFixture() (*Memory, *time.Time, async.PeriodicSchedule) {
	now := time.Date(2026, 4, 5, 6, 7, 8, 123456789, time.UTC)
	m := NewMemory()
	m.Clock = func() time.Time { return now }
	p := async.PeriodicSchedule{
		ID: async.StableID("periodic", "schedule"), Signature: async.Signature{Task: "test.periodic", Version: 1, Args: json.RawMessage(`{"number":9007199254740993}`)},
		Rule: async.Every(time.Minute + time.Nanosecond), Enabled: true, NextDue: now.Add(time.Minute),
		Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1,
	}
	return m, &now, p
}

func periodicLeaseOne(t *testing.T, m *Memory, p async.PeriodicSchedule) async.PeriodicSchedule {
	t.Helper()
	if err := m.UpsertSchedule(context.Background(), p, 0); err != nil {
		t.Fatal(err)
	}
	items, err := m.LeaseSchedules(context.Background(), "beat", 1, time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	return items[0]
}

func periodicIntent(p async.PeriodicSchedule, label string) async.Intent {
	id := async.StableID(p.ID, label)
	n := 3
	e := async.Envelope{ProtocolVersion: 1, ID: id, Task: p.Signature.Task, Version: 1, Args: append(json.RawMessage(nil), p.Signature.Args...), CreatedAt: p.NextDue, Queue: "default", MaxRetries: &n}
	return async.Intent{ID: async.StableID(p.ID, "dispatch-"+id), SourceID: p.ID, Kind: "publish", Envelope: &e}
}

func TestMemoryPeriodicLifecycleAndMillisecondClock(t *testing.T) {
	ctx := context.Background()
	m, now, p := periodicFixture()
	if err := m.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertSchedule(ctx, p, 0); !errors.Is(err, async.ErrConflict) {
		t.Fatal(err)
	}
	if got, err := m.LeaseSchedules(ctx, "beat", 10, time.Second); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	// Redis's due index truncates milliseconds while its payload retains the
	// precise due instant. The simulator exposes that distinction explicitly.
	*now = p.NextDue.Truncate(time.Millisecond)
	got, err := m.LeaseSchedules(ctx, "beat", 10, time.Second+900*time.Microsecond)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	leased := got[0]
	if !leased.NextDue.Equal(p.NextDue) || leased.Rule.Interval != p.Rule.Interval || !leased.ObservedAt.Equal(*now) || !leased.LeaseUntil.Equal(now.Add(time.Second)) || leased.Revision != 2 || leased.Fence != 1 {
		t.Fatal(leased)
	}
	if items, err := m.LeaseSchedules(ctx, "second", 1, time.Minute); err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	*now = leased.LeaseUntil
	taken, err := m.LeaseSchedules(ctx, "second", 1, time.Minute)
	if err != nil || len(taken) != 1 || taken[0].Fence != 2 || taken[0].Revision != 3 {
		t.Fatal(taken, err)
	}
	intent := periodicIntent(p, "one")
	next := p.NextDue.Add(time.Hour)
	if err := m.CommitOccurrence(ctx, leased, next, intent.Envelope.ID, []async.Intent{intent}); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := m.CommitOccurrence(ctx, taken[0], next, intent.Envelope.ID, []async.Intent{intent}); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Revision != 4 || list[0].Owner != "" || !list[0].ObservedAt.IsZero() || !list[0].NextDue.Equal(next) || list[0].LastTaskID != intent.Envelope.ID {
		t.Fatal(list, err)
	}
	if err := m.Disable(ctx, p.ID, 3); !errors.Is(err, async.ErrConflict) {
		t.Fatal(err)
	}
	if err := m.Disable(ctx, p.ID, 4); err != nil {
		t.Fatal(err)
	}
	*now = next.Add(time.Hour)
	if items, err := m.LeaseSchedules(ctx, "disabled", 1, time.Minute); err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	retained, err := m.ListIntents(ctx, 10)
	if err != nil || len(retained) != 1 || retained[0].Envelope.ID != intent.Envelope.ID {
		t.Fatal(retained, err)
	}
	list, _ = m.List(ctx, 1)
	list[0].Enabled = true
	list[0].Revision++
	if err := m.UpsertSchedule(ctx, list[0], 5); err != nil {
		t.Fatal(err)
	}
	if items, err := m.LeaseSchedules(ctx, "enabled", 1, time.Minute); err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
}

func TestMemoryPeriodicAtomicBatchesAndRetainedIntentIdentity(t *testing.T) {
	for _, mode := range []string{"duplicate", "source", "invalid-envelope", "oversize", "revision", "conflicting-intent", "identical-delivered", "end", "batch-limit"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m, now, p := periodicFixture()
			p.NextDue = now.Add(-time.Minute)
			if mode == "end" {
				p.EndAt = now.Add(time.Minute)
			}
			lease := periodicLeaseOne(t, m, p)
			intent := periodicIntent(p, "one")
			batch := []async.Intent{intent}
			want := async.ErrInvalid
			switch mode {
			case "duplicate":
				batch = append(batch, intent)
			case "source":
				batch[0].SourceID = async.StableID("other", "source")
			case "invalid-envelope":
				batch[0].Envelope.Args = json.RawMessage(`invalid`)
			case "oversize":
				batch[0].Envelope.Args = json.RawMessage(`"` + strings.Repeat("x", periodicBatchBytes) + `"`)
			case "revision":
				lease.Revision = math.MaxUint64
			case "conflicting-intent", "identical-delivered":
				old, _ := periodicCopy(intent, periodicBatchBytes)
				old.Delivered, old.Owner, old.Fence, old.LeaseUntil = true, "relay", 7, *now
				if mode == "conflicting-intent" {
					old.Envelope.Args = json.RawMessage(`0`)
					want = async.ErrConflict
				} else {
					want = nil
				}
				m.intents[intent.ID] = old
			case "end":
				want = nil
			case "batch-limit":
				batch = make([]async.Intent, 1001)
			}
			before, _ := m.List(ctx, 10)
			oldCount := len(m.intents)
			err := m.CommitOccurrence(ctx, lease, now.Add(time.Minute), intent.Envelope.ID, batch)
			if !errors.Is(err, want) {
				t.Fatal(err, want)
			}
			after, _ := m.List(ctx, 10)
			if want != nil {
				if !reflect.DeepEqual(before, after) || len(m.intents) != oldCount {
					t.Fatal("rejected batch partially committed")
				}
			} else if mode == "end" && after[0].Enabled || mode == "identical-delivered" && (!m.intents[intent.ID].Delivered || m.intents[intent.ID].Fence != 7) {
				t.Fatal(after, m.intents)
			}
		})
	}
	// A valid maximum catch-up batch must not be mistaken for excessive work.
	m, now, p := periodicFixture()
	p.NextDue = *now
	lease := periodicLeaseOne(t, m, p)
	batch := make([]async.Intent, 1000)
	for i := range batch {
		batch[i] = periodicIntent(p, time.Duration(i).String())
	}
	if err := m.CommitOccurrence(context.Background(), lease, now.Add(time.Hour), batch[999].Envelope.ID, batch); err != nil || len(m.intents) != 1000 {
		t.Fatal(err, len(m.intents))
	}
}

func TestMemoryPeriodicLeasePreflightAndConcurrentOwnership(t *testing.T) {
	ctx := context.Background()
	m, now, p := periodicFixture()
	p.NextDue = *now
	q := p
	q.ID, q.Fence = async.StableID("periodic", "overflow"), math.MaxUint64
	for _, item := range []async.PeriodicSchedule{p, q} {
		if err := m.UpsertSchedule(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := m.List(ctx, 10)
	if got, err := m.LeaseSchedules(ctx, "one", 10, time.Second); !errors.Is(err, async.ErrInvalid) || len(got) != 0 {
		t.Fatal(got, err)
	}
	after, _ := m.List(ctx, 10)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("overflow partially leased a batch")
	}
	if err := m.Disable(ctx, q.ID, 1); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan async.PeriodicSchedule, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := m.LeaseSchedules(ctx, "contender", 1, time.Minute)
			if err != nil {
				t.Error(err)
			}
			for _, p := range got {
				winners <- p
			}
		}()
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatal("multiple lease winners", len(winners))
	}
}

type periodicTestContext struct {
	context.Context
	hook func()
	err  error
}

func (c *periodicTestContext) Err() error {
	c.hook()
	return c.err
}

func TestMemoryPeriodicCallbackOwnershipAndNoLockedHooks(t *testing.T) {
	ctx := context.Background()
	for _, callback := range []string{"context", "fail", "clock"} {
		t.Run(callback, func(t *testing.T) {
			m, now, p := periodicFixture()
			p.NextDue = *now
			p.Signature.Options.Headers = map[string]string{"safe": "original"}
			original := string(p.Signature.Args)
			mutate := func() {
				if !m.mu.TryLock() {
					panic("hook ran under memory lock")
				}
				m.mu.Unlock()
				p.Signature.Args[0] = '['
				p.Signature.Options.Headers["safe"] = "changed"
				*p.NextDue.Location() = *time.FixedZone("changed", 3600)
			}
			// Never mutate the process-wide UTC location in a regression.
			p.NextDue = periodicTime(p.NextDue)
			operationCtx := ctx
			if callback == "context" {
				operationCtx = &periodicTestContext{Context: ctx, hook: mutate}
			} else if callback == "fail" {
				m.Fail = func(string) error { mutate(); return nil }
			}
			if err := m.UpsertSchedule(operationCtx, p, 0); err != nil {
				t.Fatal(err)
			}
			m.Fail = nil
			if callback == "clock" {
				m.Clock = func() time.Time { mutate(); return *now }
			}
			items, err := m.LeaseSchedules(ctx, "beat", 1, time.Minute)
			if err != nil || len(items) != 1 || string(items[0].Signature.Args) != original || items[0].Signature.Options.Headers["safe"] != "original" {
				t.Fatal(items, err)
			}
			m.Clock = func() time.Time { return *now }
			intent := periodicIntent(items[0], "one")
			batch := []async.Intent{intent}
			m.Fail = func(op string) error {
				if op == "commit_occurrence" {
					batch[0].Envelope.Args[0] = '['
					items[0].Signature.Options.Headers["safe"] = "late"
				}
				return nil
			}
			if err := m.CommitOccurrence(ctx, items[0], now.Add(time.Hour), intent.Envelope.ID, batch); err != nil {
				t.Fatal(err)
			}
			m.Fail = nil
			stored, _ := m.List(ctx, 1)
			stored[0].Signature.Options.Headers["safe"] = "caller"
			*stored[0].LeaseUntil.Location() = *time.FixedZone("caller", 7200)
			stored, _ = m.List(ctx, 1)
			if stored[0].Signature.Options.Headers["safe"] != "original" || !stored[0].LeaseUntil.IsZero() || string(m.intents[intent.ID].Envelope.Args) != original {
				t.Fatal("stored state retained callback/caller aliases")
			}
		})
	}
}

func TestMemoryPeriodicFailuresAndBounds(t *testing.T) {
	for _, mode := range []string{"nil-context", "typed-nil", "canceled", "private-context", "context-panic", "fail", "fail-panic", "clock-panic", "clock-cancel", "zero-clock", "pre-epoch-clock", "bad-limit", "owner", "short-lease", "huge-text", "cycle", "revision"} {
		t.Run(mode, func(t *testing.T) {
			m, now, p := periodicFixture()
			p.NextDue = *now
			if err := m.UpsertSchedule(context.Background(), p, 0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var input context.Context = ctx
			owner, limit, lease := "beat", 1, time.Minute
			want := async.ErrUnavailable
			switch mode {
			case "nil-context":
				input, want = nil, async.ErrInvalid
			case "typed-nil":
				var bad *periodicTestContext
				input = bad
			case "canceled":
				cancel()
				want = context.Canceled
			case "private-context":
				input = &periodicTestContext{Context: ctx, hook: func() {}, err: errors.New("private")}
			case "context-panic":
				input = &periodicTestContext{Context: ctx, hook: func() { panic("private") }}
			case "fail":
				m.Fail = func(string) error { return async.ErrUnavailable }
			case "fail-panic":
				m.Fail = func(string) error { panic("private") }
			case "clock-panic":
				m.Clock = func() time.Time { panic("private") }
			case "clock-cancel":
				m.Clock = func() time.Time { cancel(); return *now }
				want = context.Canceled
			case "zero-clock":
				m.Clock = func() time.Time { return time.Time{} }
				want = async.ErrInvalid
			case "pre-epoch-clock":
				m.Clock = func() time.Time { return time.Unix(-1, 0) }
				want = async.ErrInvalid
			case "bad-limit":
				limit, want = 1001, async.ErrInvalid
			case "owner":
				owner, want = strings.Repeat("x", 257), async.ErrInvalid
			case "short-lease":
				lease, want = time.Nanosecond, async.ErrInvalid
			case "huge-text", "cycle", "revision":
				p.Revision = 2
				if mode == "huge-text" {
					p.Rule.Timezone = strings.Repeat("x", async.MaxPayloadBytes+1)
				}
				if mode == "cycle" {
					links := make([]async.Signature, 1)
					links[0].Callbacks = links
					p.Signature.Callbacks = links
				}
				if mode == "revision" {
					p.Revision = math.MaxUint64
				}
				if err := m.UpsertSchedule(ctx, p, 1); !errors.Is(err, async.ErrInvalid) {
					t.Fatal(err)
				}
				if m.periodic[p.ID].Revision != 1 {
					t.Fatal("invalid input mutated schedule")
				}
				return
			}
			got, err := m.LeaseSchedules(input, owner, limit, lease)
			if !errors.Is(err, want) || got != nil || m.periodic[p.ID].Revision != 1 {
				t.Fatal(got, err, want)
			}
		})
	}
}

func TestMemoryPeriodicDisablePreflightsEncodedGrowth(t *testing.T) {
	m, _, p := periodicFixture()
	// Leave one revision digit to grow while the disabled flag has the same
	// encoded width. Refusal must preserve a readable, unchanged stored row.
	p.Enabled, p.Revision = false, 9
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Signature.ParentField = strings.Repeat("x", async.MaxPayloadBytes-len(raw)-len(`,"parent_field":""`))
	raw, err = json.Marshal(p)
	if err != nil || len(raw) != async.MaxPayloadBytes {
		t.Fatal(len(raw), err)
	}
	m.periodic[p.ID] = p
	if list, err := m.List(context.Background(), 1); err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if err := m.Disable(context.Background(), p.ID, 9); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	if m.periodic[p.ID].Revision != 9 {
		t.Fatal("failed disable changed revision")
	}
}

func FuzzMemoryPeriodicSnapshot(f *testing.F) {
	for _, value := range []string{`null`, `0`, `9007199254740993`, `{"items":[1,null,"x"]}`, `invalid`, `"\u0000"`} {
		f.Add(value, uint8(0))
		f.Add(value, uint8(5))
	}
	f.Fuzz(func(t *testing.T, raw string, nesting uint8) {
		if len(raw) > async.MaxPayloadBytes+1 {
			t.Skip()
		}
		m, _, p := periodicFixture()
		p.Signature.Args = json.RawMessage(raw)
		for i := 0; i < int(nesting)%40; i++ {
			p.Signature = async.Signature{Task: "test.periodic", Version: 1, Args: json.RawMessage(`null`), Callbacks: []async.Signature{p.Signature}}
		}
		err := m.UpsertSchedule(context.Background(), p, 0)
		if err != nil {
			if !errors.Is(err, async.ErrInvalid) || len(m.periodic) != 0 {
				t.Fatal("invalid snapshot mutated state", err)
			}
			return
		}
		list, err := m.List(context.Background(), 1)
		if err != nil || len(list) != 1 {
			t.Fatal(list, err)
		}
		a, _ := json.Marshal(list[0])
		list[0].Signature.Args[0] = '['
		if !list[0].LeaseUntil.IsZero() || list[0].LeaseUntil.Location() == time.UTC {
			t.Fatal("zero timestamp alias")
		}
		*list[0].LeaseUntil.Location() = *time.FixedZone("changed", 3600)
		again, err := m.List(context.Background(), 1)
		b, _ := json.Marshal(again[0])
		if err != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("caller rewrote retained snapshot", err)
		}
	})
}
