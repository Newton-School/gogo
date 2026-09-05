package redis

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func TestRealRedisScheduleCreatePreflightsDueIndex(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	schedules := &Schedules{Connection: connection}
	for _, family := range []string{"delayed", "periodic"} {
		t.Run(family, func(t *testing.T) {
			e := codecEnvelope(t)
			e.ETA = time.Now().Add(time.Hour)
			keys := schedules.keys(connector.Partition(e.ID))
			if family == "periodic" {
				keys = schedules.periodicKeys(connector.Partition(e.ID))
			}
			if err := connection.Client().Set(ctx, keys[0], "wrong-type", 0).Err(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Client().Del(ctx, keys[0]).Err() })
			before := resultKeySnapshot(t, connection, keys)
			var err error
			if family == "delayed" {
				err = schedules.Schedule(ctx, e)
			} else {
				err = schedules.UpsertSchedule(ctx, codecPeriodic(e), 0)
			}
			if err == nil {
				t.Fatal("invalid due index accepted")
			}
			assertResultKeysUnchanged(t, connection, keys, before)
		})
	}
}

func TestRealRedisOccurrencePreflightsAllIntents(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	schedules := &Schedules{Connection: connection}
	e := codecEnvelope(t)
	p := codecPeriodic(e)
	if err := schedules.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	leased, err := schedules.LeaseSchedules(ctx, "beat", 1, time.Minute)
	if err != nil || len(leased) != 1 {
		t.Fatal(len(leased), err)
	}
	p = leased[0]
	keys := schedules.periodicKeys(connector.Partition(p.ID))
	keys = append(keys, schedules.intentBackend().keys(p.ID)...)
	before := resultKeySnapshot(t, connection, keys)
	good := async.Intent{ID: async.StableID(p.ID, "good"), SourceID: p.ID, Kind: "publish", Envelope: &e}
	bad := async.Intent{ID: async.StableID(p.ID, "bad"), Kind: "publish", Envelope: &e}
	if err := schedules.CommitOccurrence(ctx, p, time.Now().Add(time.Hour), e.ID, []async.Intent{good, bad}); err == nil {
		t.Fatal("invalid later occurrence intent accepted")
	}
	assertResultKeysUnchanged(t, connection, keys, before)
	next := p
	next.NextDue = time.Now().Add(time.Hour)
	next.Revision++
	next.Owner, next.LeaseUntil = "", time.Time{}
	payload, _ := marshalPeriodic(next)
	intents, _ := marshalIntents([]async.Intent{good, bad})
	// Verify the script's staging independently from the Go identity preflight.
	if _, err := connection.Atomic(ctx, periodicCommit, keys, p.ID, strconv.FormatUint(p.Revision, 10), strconv.FormatUint(p.Fence, 10), p.Owner, payload, next.NextDue.UnixMilli(), intents, "1"); err == nil {
		t.Fatal("Lua accepted an invalid later occurrence intent")
	}
	assertResultKeysUnchanged(t, connection, keys, before)
	if _, err := connection.Client().ZScore(ctx, keys[3], p.ID+":"+good.ID).Result(); !errors.Is(err, redigo.Nil) {
		t.Fatal("failed occurrence partially published an intent", err)
	}
}

func TestRealRedisScheduleMutationPreflightsAllKeyTypes(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	schedules := &Schedules{Connection: connection}
	for _, family := range []string{"delayed", "periodic"} {
		for _, operation := range []string{"write", "lease", "commit"} {
			keyCount := 2
			if family == "periodic" && operation == "commit" {
				keyCount = 4
			}
			for index := 0; index < keyCount; index++ {
				t.Run(family+"/"+operation+"/"+strconv.Itoa(index), func(t *testing.T) {
					e := codecEnvelope(t)
					e.ETA = time.Now().Add(-time.Minute)
					p := codecPeriodic(e)
					var delayed async.DelayedItem
					keys := schedules.keys(connector.Partition(e.ID))
					if family == "periodic" {
						keys = schedules.periodicKeys(connector.Partition(e.ID))
						if err := schedules.UpsertSchedule(ctx, p, 0); err != nil {
							t.Fatal(err)
						}
						if operation == "commit" {
							items, err := schedules.LeaseSchedules(ctx, "beat", 1000, time.Minute)
							if err != nil {
								t.Fatal(err)
							}
							for _, item := range items {
								if item.ID == p.ID {
									p = item
								}
							}
							if p.Owner != "beat" {
								t.Fatal("fixture did not claim selected periodic schedule")
							}
							keys = append(keys, schedules.intentBackend().keys(p.ID)...)
						}
					} else {
						if err := schedules.Schedule(ctx, e); err != nil {
							t.Fatal(err)
						}
						if operation == "commit" {
							items, err := schedules.LeaseDue(ctx, "scheduler", 1000, time.Minute)
							if err != nil {
								t.Fatal(err)
							}
							for _, item := range items {
								if item.Envelope.ID == e.ID {
									delayed = item
								}
							}
							if delayed.Owner != "scheduler" {
								t.Fatal("fixture did not claim selected delayed schedule")
							}
						}
					}
					prior, err := connection.Client().Dump(ctx, keys[index]).Result()
					if err != nil && !errors.Is(err, redigo.Nil) {
						t.Fatal(err)
					}
					if err := connection.Client().Set(ctx, keys[index], "wrong-type", 0).Err(); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if prior == "" {
							_ = connection.Client().Del(ctx, keys[index]).Err()
						} else {
							_ = connection.Client().RestoreReplace(ctx, keys[index], 0, prior).Err()
						}
					})
					before := resultKeySnapshot(t, connection, keys)
					if family == "periodic" {
						switch operation {
						case "write":
							p.Revision++
							err = schedules.UpsertSchedule(ctx, p, 1)
						case "lease":
							_, err = schedules.LeaseSchedules(ctx, "beat", 1000, time.Minute)
						case "commit":
							err = schedules.CommitOccurrence(ctx, p, time.Now().Add(time.Hour), e.ID, nil)
						}
					} else {
						switch operation {
						case "write":
							err = schedules.Schedule(ctx, e)
						case "lease":
							_, err = schedules.LeaseDue(ctx, "scheduler", 1000, time.Minute)
						case "commit":
							err = schedules.CommitFire(ctx, delayed)
						}
					}
					if err == nil {
						t.Fatal("invalid schedule key type accepted")
					}
					assertResultKeysUnchanged(t, connection, keys, before)
				})
			}
		}
	}
}
