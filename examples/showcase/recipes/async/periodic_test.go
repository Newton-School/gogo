package async_recipes_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func Example_periodicSchedules() {
	registry := async.NewRegistry()
	task := increment(registry)
	h := newHarness(registry)
	defer h.cancel()
	schedule := async.PeriodicSchedule{
		ID: async.StableID("recipes", "every-minute"), Signature: must(task.Signature(41)),
		Rule: async.Every(time.Minute), Enabled: true, NextDue: h.now.Add(time.Minute),
		Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1,
	}
	check(h.memory.UpsertSchedule(h.ctx, schedule, 0)) // Expected revision zero creates the row.
	beat := async.Beat{Client: h.client, Store: h.memory, ID: "recipe-beat"}
	check(beat.Tick(h.ctx))
	fmt.Println("before due:", len(must(h.memory.ListIntents(h.ctx, 10))))
	h.now = schedule.NextDue
	check(beat.Tick(h.ctx))
	h.drain()
	occurrenceID := async.StableID(schedule.ID, schedule.NextDue.Format(time.RFC3339Nano))
	result := async.RestoreResult[int](h.client, occurrenceID)
	fmt.Println("periodic result:", must(result.Get(h.ctx)))
	current := must(h.memory.List(h.ctx, 10))[0]
	check(h.memory.Disable(h.ctx, current.ID, current.Revision))
	h.now = h.now.Add(2 * time.Minute)
	check(beat.Tick(h.ctx))
	fmt.Println("disabled future intents:", len(must(h.memory.ListIntents(h.ctx, 10))))
	fmt.Println("retained result:", must(result.Get(h.ctx)))
	cron := must(async.Crontab("*/15 * * * *", "UTC"))
	fmt.Println("next cron:", must(cron.Next(h.now)).Format(time.RFC3339))
	// Output:
	// before due: 0
	// periodic result: 42
	// disabled future intents: 0
	// retained result: 42
	// next cron: 2026-01-01T12:15:00Z
}

func TestPeriodicMisfirePolicies(t *testing.T) {
	for _, scenario := range []struct {
		policy string
		count  int
	}{{"skip", 0}, {"coalesce", 1}, {"catchup", 3}} {
		t.Run(scenario.policy, func(t *testing.T) {
			registry := async.NewRegistry()
			task := increment(registry)
			h := newHarness(registry)
			defer h.cancel()
			schedule := async.PeriodicSchedule{
				ID: async.StableID("recipes.misfire", scenario.policy), Signature: must(task.Signature(1)),
				Rule: async.Every(time.Minute), Enabled: true, NextDue: h.now.Add(-10 * time.Minute),
				Misfire: scenario.policy, CatchUpLimit: 3, Overlap: "allow", Revision: 1,
			}
			check(h.memory.UpsertSchedule(h.ctx, schedule, 0))
			beat := async.Beat{Client: h.client, Store: h.memory, ID: "recipe-beat"}
			check(beat.Tick(h.ctx))
			if got := len(must(h.memory.ListIntents(h.ctx, 10))); got != scenario.count {
				t.Fatalf("%s emitted %d intents; want %d", scenario.policy, got, scenario.count)
			}
		})
	}
}
