package redis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	redigo "github.com/redis/go-redis/v9"
)

const preciseJSON = `{"z":9007199254740993,"a":{"id":18446744073709551615,"negative":-9007199254740993,"decimal":1.2300}}`
const highCounter uint64 = 9007199254740992

func codecConnection(t *testing.T) *connector.Connection {
	t.Helper()
	cfg := fixture.Start(t)
	cfg.Role = connector.ResultRole
	c, err := connector.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func codecEnvelope(t *testing.T) async.Envelope {
	t.Helper()
	id, err := async.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return async.Envelope{ID: id, ProtocolVersion: 1, Task: "codec.exact", Version: 1, Queue: "default", Args: json.RawMessage(preciseJSON), CreatedAt: time.Now().UTC(), Callbacks: []async.Signature{{Task: "codec.callback", Version: 1, Args: json.RawMessage(preciseJSON)}}}
}

func readDocument(t *testing.T, c *connector.Connection, key, field string) opaqueDocument {
	t.Helper()
	raw, err := c.Client().HGet(context.Background(), key, field).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	d, err := unmarshalDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func writeDocument(t *testing.T, c *connector.Connection, key, field string, d opaqueDocument) {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Client().HSet(context.Background(), key, field, raw).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRealRedisOpaqueDelayedPayloadAndExactFence(t *testing.T) {
	ctx := context.Background()
	c := codecConnection(t)
	s := &Schedules{Connection: c}
	e := codecEnvelope(t)
	e.ETA = time.Now().Add(-time.Minute)
	if err := s.Schedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	key := s.keys(connector.Partition(e.ID))[1]
	d := readDocument(t, c, key, delayedID(e))
	original := append([]byte(nil), d.Payload...)
	d.Fence = strconv.FormatUint(highCounter, 10)
	writeDocument(t, c, key, delayedID(e), d)
	items, err := s.LeaseDue(ctx, "owner", 1, time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	item := items[0]
	if item.Fence != highCounter+1 || item.Envelope.DispatchDigest() != e.DispatchDigest() || string(item.Envelope.Args) != preciseJSON || string(item.Envelope.Callbacks[0].Args) != preciseJSON {
		t.Fatal(item)
	}
	if !bytes.Equal(original, readDocument(t, c, key, delayedID(e)).Payload) {
		t.Fatal("lease rewrote envelope bytes")
	}
	if err := s.Schedule(ctx, e); err != nil {
		t.Fatal("duplicate dispatch conflicted after lease", err)
	}
	stale := item
	stale.Fence--
	if err := s.CommitFire(ctx, stale); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := s.CommitFire(ctx, item); err != nil {
		t.Fatal(err)
	}

	// Exhaustion must never wrap or partially replace the accepted document.
	e = codecEnvelope(t)
	e.ETA = time.Now().Add(-time.Minute)
	if err := s.Schedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	key = s.keys(connector.Partition(e.ID))[1]
	d = readDocument(t, c, key, delayedID(e))
	d.Fence = strconv.FormatUint(math.MaxUint64, 10)
	writeDocument(t, c, key, delayedID(e), d)
	before, _ := c.Client().HGet(ctx, key, delayedID(e)).Result()
	if _, err := s.LeaseDue(ctx, "overflow", 1, time.Minute); err == nil {
		t.Fatal("exhausted fence accepted")
	}
	after, _ := c.Client().HGet(ctx, key, delayedID(e)).Result()
	if before != after {
		t.Fatal("exhaustion mutated accepted document")
	}
}

func TestRealRedisOpaqueIntentFamilies(t *testing.T) {
	ctx := context.Background()
	c := codecConnection(t)
	for _, family := range []string{"task", "workflow", "schedule"} {
		t.Run(family, func(t *testing.T) {
			e := codecEnvelope(t)
			completion := async.Completion{ID: e.ID, State: async.Succeeded, Output: json.RawMessage(preciseJSON)}
			graph := async.Graph{ID: e.ID, Kind: "group", Revision: 1, State: async.Running, Children: []async.Envelope{e}, Members: map[string]async.Completion{e.ID: completion}, Output: json.RawMessage(preciseJSON)}
			intent := async.Intent{ID: async.StableID(e.ID, "intent"), SourceID: e.ID, Kind: "replace", Envelope: &e, Completion: &completion, Graph: &graph}
			var store async.IntentStore
			switch family {
			case "task":
				r := &Results{Connection: c}
				record, err := async.InitialRecord(e, async.Queued)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.cas(ctx, 0, record, "create", 0, "", 0, []async.Intent{intent}); err != nil {
					t.Fatal(err)
				}
				store = r
			case "workflow":
				w := &Workflows{Connection: c}
				if err := w.CreateGraph(ctx, graph, []async.Intent{intent}); err != nil {
					t.Fatal(err)
				}
				stored, err := w.ReadGraph(ctx, graph.ID)
				if err != nil || string(stored.Output) != preciseJSON || string(stored.Members[e.ID].Output) != preciseJSON {
					t.Fatal(stored, err)
				}
				store = w
			case "schedule":
				s := &Schedules{Connection: c}
				p := codecPeriodic(e)
				if err := s.UpsertSchedule(ctx, p, 0); err != nil {
					t.Fatal(err)
				}
				leased, err := s.LeaseSchedules(ctx, "beat", 1, time.Minute)
				if err != nil || len(leased) != 1 {
					t.Fatal(leased, err)
				}
				if err := s.CommitOccurrence(ctx, leased[0], time.Now().Add(time.Hour), e.ID, []async.Intent{intent}); err != nil {
					t.Fatal(err)
				}
				store = s
			}
			list, err := store.ListIntents(ctx, 100)
			if err != nil || len(list) != 1 {
				t.Fatal(list, err)
			}
			key := c.PartitionKey(family, e.ID, "intents")
			d := readDocument(t, c, key, intent.ID)
			expected, _ := json.Marshal(intent)
			if !bytes.Equal(expected, d.Payload) {
				t.Fatal("commit rewrote intent bytes")
			}
			d.Fence = strconv.FormatUint(highCounter, 10)
			writeDocument(t, c, key, intent.ID, d)
			claimed, err := store.ClaimIntent(ctx, e.ID, intent.ID, "relay", time.Minute)
			if err != nil || claimed.Fence != highCounter+1 || string(claimed.Envelope.Args) != preciseJSON || string(claimed.Completion.Output) != preciseJSON || string(claimed.Graph.Output) != preciseJSON {
				t.Fatal(claimed, err)
			}
			if err := store.MarkIntentDelivered(ctx, e.ID, intent.ID, highCounter, "relay"); !errors.Is(err, async.ErrLeaseLost) {
				t.Fatal(err)
			}
			if err := store.MarkIntentDelivered(ctx, e.ID, intent.ID, claimed.Fence, "relay"); err != nil {
				t.Fatal(err)
			}
			d = readDocument(t, c, key, intent.ID)
			if !d.Delivered || !bytes.Equal(expected, d.Payload) {
				t.Fatal("claim/delivery rewrote intent bytes")
			}
		})
	}
}

func codecPeriodic(e async.Envelope) async.PeriodicSchedule {
	return async.PeriodicSchedule{ID: e.ID, Signature: async.Signature{Task: e.Task, Version: e.Version, Args: e.Args}, Rule: async.Every(time.Duration(highCounter + 1)), Revision: 1, Enabled: true, NextDue: time.Now().Add(-time.Minute), Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow"}
}

func TestRealRedisOpaquePeriodicPayloadAndExactCounters(t *testing.T) {
	ctx := context.Background()
	c := codecConnection(t)
	s := &Schedules{Connection: c}
	p := codecPeriodic(codecEnvelope(t))
	if err := s.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	key := s.periodicKeys(connector.Partition(p.ID))[1]
	d := readDocument(t, c, key, p.ID)
	original := append([]byte(nil), d.Payload...)
	d.Revision, d.Fence = strconv.FormatUint(highCounter, 10), strconv.FormatUint(highCounter, 10)
	writeDocument(t, c, key, p.ID, d)
	leased, err := s.LeaseSchedules(ctx, "beat", 1, time.Minute)
	if err != nil || len(leased) != 1 {
		t.Fatal(leased, err)
	}
	p = leased[0]
	if p.Revision != highCounter+1 || p.Fence != highCounter+1 || p.Rule.Interval != time.Duration(highCounter+1) || string(p.Signature.Args) != preciseJSON {
		t.Fatal(p)
	}
	if !bytes.Equal(original, readDocument(t, c, key, p.ID).Payload) {
		t.Fatal("lease rewrote periodic bytes")
	}
	if err := s.CommitOccurrence(ctx, p, time.Now().Add(time.Hour), "", nil); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, 1)
	if err != nil || len(list) != 1 || list[0].Revision != highCounter+2 || list[0].Fence != highCounter+1 || list[0].Rule.Interval != time.Duration(highCounter+1) || string(list[0].Signature.Args) != preciseJSON {
		t.Fatal(list, err)
	}
	if err := s.Disable(ctx, p.ID, list[0].Revision); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(ctx, 1)
	if err != nil || list[0].Enabled || list[0].Revision != highCounter+3 {
		t.Fatal(list, err)
	}
	p.Revision = 0
	if err := s.UpsertSchedule(ctx, p, math.MaxUint64); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("revision wrapped", err)
	}
	p.Revision = math.MaxUint64
	if err := s.CommitOccurrence(ctx, p, time.Now().Add(time.Hour), "", nil); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("commit revision wrapped", err)
	}
}

func TestOpaqueDocumentRejectsUnversionedAndInexactCounters(t *testing.T) {
	for _, raw := range []string{`{"envelope":{},"fence":1}`, `{"format":1,"payload":"e30=","fence":9007199254740993,"revision":"1"}`, `{"format":1,"payload":"e30=","fence":"01","revision":"1"}`, `{"format":1,"payload":"e30=","fence":"18446744073709551616","revision":"1"}`} {
		if _, err := unmarshalDocument([]byte(raw)); !errors.Is(err, async.ErrUnavailable) {
			t.Fatal(raw, err)
		}
	}
}

func TestRealRedisResultPayloadAndCountersRemainExact(t *testing.T) {
	ctx := context.Background()
	c := codecConnection(t)
	r := &Results{Connection: c}
	e := codecEnvelope(t)
	if err := r.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
	record, err := r.Lookup(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Revision, record.Fence = highCounter, highCounter
	raw, _ := json.Marshal(record)
	if err := c.Client().HSet(ctx, r.keys(e.ID)[0], "record", raw, "revision", strconv.FormatUint(record.Revision, 10), "fence", strconv.FormatUint(record.Fence, 10)).Err(); err != nil {
		t.Fatal(err)
	}
	claim, err := r.Claim(ctx, e, "worker", time.Minute)
	if err != nil || !claim.Acquired || claim.Record.Revision != highCounter+1 || claim.Record.Fence != highCounter+1 {
		t.Fatal(claim, err)
	}
	if err := r.RecordProgress(ctx, e.ID, claim.Record.Fence, "worker", json.RawMessage(preciseJSON)); err != nil {
		t.Fatal(err)
	}
	record, err = r.Lookup(ctx, e.ID)
	if err != nil || string(record.Progress) != preciseJSON || record.Revision != highCounter+2 {
		t.Fatal(record, err)
	}
	if err := r.Transition(ctx, async.Transition{ID: e.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(preciseJSON)}); err != nil {
		t.Fatal(err)
	}
	record, err = r.Lookup(ctx, e.ID)
	if err != nil || string(record.Output) != preciseJSON || string(record.Envelope.Args) != preciseJSON || record.Revision != highCounter+3 {
		t.Fatal(record, err)
	}
	for _, exhausted := range []string{"fence", "revision"} {
		state, _ := async.InitialRecord(e, async.Queued)
		if exhausted == "fence" {
			state.Fence = math.MaxUint64
		} else {
			state.Revision = math.MaxUint64
		}
		if _, err := async.ClaimRecord(state, e, "worker", time.Now(), time.Minute); !errors.Is(err, async.ErrUnavailable) {
			t.Fatal(exhausted, err)
		}
	}
}

func TestRealRedisLeaseBatchPreflightsEveryDueDocument(t *testing.T) {
	ctx := context.Background()
	c := codecConnection(t)
	s := &Schedules{Connection: c}
	for _, periodic := range []bool{false, true} {
		for _, failure := range []string{"legacy", "fence", "revision"} {
			if !periodic && failure == "revision" {
				continue
			}
			t.Run(strconv.FormatBool(periodic)+"/"+failure, func(t *testing.T) {
				e := codecEnvelope(t)
				e.ETA = time.Now().Add(-2 * time.Minute)
				bad := codecEnvelope(t)
				for connector.Partition(bad.ID) != connector.Partition(e.ID) {
					bad = codecEnvelope(t)
				}
				bad.ETA = time.Now().Add(-time.Minute)
				keys := s.keys(connector.Partition(e.ID))
				firstID, secondID := delayedID(e), delayedID(bad)
				script := scheduleLease
				if periodic {
					keys = s.periodicKeys(connector.Partition(e.ID))
					firstID, secondID = e.ID, bad.ID
					script = periodicLease
					for _, envelope := range []async.Envelope{e, bad} {
						p := codecPeriodic(envelope)
						p.NextDue = envelope.ETA
						if err := s.UpsertSchedule(ctx, p, 0); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					for _, envelope := range []async.Envelope{e, bad} {
						if err := s.Schedule(ctx, envelope); err != nil {
							t.Fatal(err)
						}
					}
				}
				d := readDocument(t, c, keys[1], secondID)
				switch failure {
				case "legacy":
					d.Format = 0
				case "fence":
					d.Fence = strconv.FormatUint(math.MaxUint64, 10)
				case "revision":
					d.Revision = strconv.FormatUint(math.MaxUint64, 10)
				}
				writeDocument(t, c, keys[1], secondID, d)
				// Select only these two deterministic scores even if this random
				// partition was reused by another subtest in the same fixture.
				if err := c.Client().ZRem(ctx, keys[0], firstID, secondID).Err(); err != nil {
					t.Fatal(err)
				}
				if err := c.Client().ZAdd(ctx, keys[0], redigo.Z{Score: -2, Member: firstID}, redigo.Z{Score: -1, Member: secondID}).Err(); err != nil {
					t.Fatal(err)
				}
				before, _ := c.Client().HGet(ctx, keys[1], firstID).Result()
				if _, err := c.Atomic(ctx, script, keys, "owner", 2, time.Minute.Milliseconds()); err == nil {
					t.Fatal("malformed batch accepted")
				}
				after, _ := c.Client().HGet(ctx, keys[1], firstID).Result()
				score, err := c.Client().ZScore(ctx, keys[0], firstID).Result()
				if err != nil || score != -2 || before != after {
					t.Fatal("failed batch partially leased healthy item", score, err)
				}
				if err := c.Client().ZRem(ctx, keys[0], firstID, secondID).Err(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
