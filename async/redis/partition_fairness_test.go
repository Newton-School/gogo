package redis

import (
	"context"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func TestPartitionRotationWraparoundAndConcurrentCalls(t *testing.T) {
	var rotation partitionRotation
	rotation.next.Store(math.MaxUint32 - 1)
	for _, expected := range []int{62, 63, 0, 1} {
		if actual := rotation.start(); actual != expected {
			t.Fatal("counter wrap changed fixed partition sequence", actual, expected)
		}
	}
	var concurrent partitionRotation
	var counts [64]atomic.Int64
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 64 {
				counts[concurrent.start()].Add(1)
			}
		})
	}
	group.Wait()
	for partition := range counts {
		if counts[partition].Load() != 8 {
			t.Fatal("concurrent rotation lost a partition turn", partition, counts[partition].Load())
		}
	}
}

func fairnessEnvelope(t *testing.T, label string, partition int) async.Envelope {
	t.Helper()
	e := codecEnvelope(t)
	for candidate := 0; ; candidate++ {
		e.ID = async.StableID(label, strconv.Itoa(candidate))
		if connector.Partition(e.ID) == partition {
			return e
		}
	}
}

func TestRealRedisIntentListingRotatesStartingPartition(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	for _, family := range []string{"task", "workflow", "schedule"} {
		t.Run(family, func(t *testing.T) {
			var store async.IntentStore
			switch family {
			case "task":
				store = &Results{Connection: connection}
			case "workflow":
				store = &Workflows{Connection: connection}
			case "schedule":
				store = &Schedules{Connection: connection}
			}
			for _, partition := range []int{0, 63} {
				e := fairnessEnvelope(t, family, partition)
				intent := async.Intent{ID: async.StableID(e.ID, "publish"), SourceID: e.ID, Kind: "publish", Envelope: &e}
				raw, err := marshalDocument(opaqueDocument{ID: intent.ID, SourceID: intent.SourceID}, intent)
				if err != nil {
					t.Fatal(err)
				}
				key := connection.PartitionKey(family, e.ID, "intents")
				index, _ := connection.PartitionIndex(family, partition, "pending-intents")
				if err := connection.Client().HSet(ctx, key, intent.ID, raw).Err(); err != nil {
					t.Fatal(err)
				}
				if err := connection.Client().ZAdd(ctx, index, redigo.Z{Member: e.ID + ":" + intent.ID}).Err(); err != nil {
					t.Fatal(err)
				}
			}
			first, err := store.ListIntents(ctx, 1)
			if err != nil || len(first) != 1 || connector.Partition(first[0].SourceID) != 0 {
				t.Fatal("initial partition fixture not observed", len(first), err)
			}
			// Leave the early partition pending to represent a continuing
			// backlog. It must not hide a later partition on every call.
			second, err := store.ListIntents(ctx, 1)
			if err != nil || len(second) != 1 || connector.Partition(second[0].SourceID) != 63 {
				t.Fatal("early partition starved later pending intent", len(second), err)
			}
		})
	}
}

func TestRealRedisScheduleLeasingRotatesStartingPartition(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	for _, family := range []string{"delayed", "periodic"} {
		t.Run(family, func(t *testing.T) {
			schedules := &Schedules{Connection: connection}
			for i, partition := range []int{0, 0, 63} {
				e := fairnessEnvelope(t, family+strconv.Itoa(i), partition)
				e.ETA = time.Now().Add(-time.Minute)
				var err error
				if family == "periodic" {
					err = schedules.UpsertSchedule(ctx, codecPeriodic(e), 0)
				} else {
					err = schedules.Schedule(ctx, e)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for call, expectedPartition := range []int{0, 63} {
				var id string
				if family == "periodic" {
					items, err := schedules.LeaseSchedules(ctx, "beat", 1, time.Minute)
					if err != nil || len(items) != 1 {
						t.Fatal(len(items), err)
					}
					id = items[0].ID
				} else {
					items, err := schedules.LeaseDue(ctx, "scheduler", 1, time.Minute)
					if err != nil || len(items) != 1 {
						t.Fatal(len(items), err)
					}
					id = items[0].Envelope.ID
				}
				if connector.Partition(id) != expectedPartition {
					t.Fatal("due work in later partition was starved", call)
				}
			}
		})
	}
}
