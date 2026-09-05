package redis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/async"
	redigo "github.com/redis/go-redis/v9"
)

func TestRealRedisWorkflowLuaStagesEveryIntentBeforeWrites(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	workflows := &Workflows{Connection: connection}
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			id := async.StableID("workflow-lua", operation)
			graph := async.Graph{ID: id, Kind: "group", State: async.Running, Revision: 1, Members: map[string]async.Completion{}}
			expected, next := "0", "1"
			if operation == "update" {
				if err := workflows.CreateGraph(ctx, graph, nil); err != nil {
					t.Fatal(err)
				}
				expected, next = "1", "2"
				graph.Revision = 2
			}
			keys := workflows.keys(id)
			before := []string{}
			for _, key := range keys {
				raw, err := connection.Client().Dump(ctx, key).Result()
				if err != nil && !errors.Is(err, redigo.Nil) {
					t.Fatal(err)
				}
				before = append(before, raw)
			}
			good := async.Intent{ID: async.StableID(id, "first"), SourceID: id, Kind: "unpin"}
			bad := async.Intent{ID: async.StableID(id, "second"), Kind: "unpin"}
			// Exercise the script independently of Go's input preflight.
			intents, err := marshalIntents([]async.Intent{good, bad})
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(graph)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := connection.Atomic(ctx, graphCAS, keys, expected, next, body, intents, id); err == nil {
				t.Fatal("Lua accepted missing intent source")
			}
			for i, key := range keys {
				raw, err := connection.Client().Dump(ctx, key).Result()
				if err != nil && !errors.Is(err, redigo.Nil) {
					t.Fatal(err)
				}
				if raw != before[i] {
					t.Fatal("Lua partially changed graph before later malformed intent")
				}
			}
		})
	}
}
