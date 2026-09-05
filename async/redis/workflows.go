package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

type Workflows struct{ Connection *connector.Connection }

func (w *Workflows) keys(id string) []string {
	index, _ := w.Connection.PartitionIndex("workflow", connector.Partition(id), "pending-intents")
	return []string{w.Connection.PartitionKey("workflow", id, "state"), w.Connection.PartitionKey("workflow", id, "intents"), index}
}
func (w *Workflows) ReadGraph(ctx context.Context, id string) (async.Graph, error) {
	var graph async.Graph
	if w.Connection == nil || w.Connection.Role() == connector.CacheRole {
		return graph, async.ErrInvalid
	}
	b, err := w.Connection.Client().HGet(ctx, w.keys(id)[0], "graph").Bytes()
	if errors.Is(err, redigo.Nil) {
		return graph, async.ErrNotFound
	}
	if err != nil {
		return graph, err
	}
	if err := json.Unmarshal(b, &graph); err != nil {
		return graph, async.ErrUnavailable
	}
	return graph, nil
}

var graphCAS = redigo.NewScript(`
local old=redis.call('HGET',KEYS[1],'revision') or '0';if old~=ARGV[1] then return 0 end
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
redis.call('HSET',KEYS[1],'revision',ARGV[2],'graph',ARGV[3])
local intents=cjson.decode(ARGV[4]);for _,intent in ipairs(intents) do
 if redis.call('HEXISTS',KEYS[2],intent.id)==0 then redis.call('HSET',KEYS[2],intent.id,cjson.encode(intent));redis.call('ZADD',KEYS[3],now,intent.source_id..':'..intent.id) end
end;return 1
`)

func (w *Workflows) write(ctx context.Context, expected uint64, g async.Graph, intents []async.Intent) error {
	if w.Connection == nil || w.Connection.Role() == connector.CacheRole {
		return async.ErrInvalid
	}
	if intents == nil {
		intents = []async.Intent{}
	}
	gb, err := json.Marshal(g)
	if err != nil || len(gb) > 8<<20 {
		return async.ErrInvalid
	}
	ib, err := json.Marshal(intents)
	if err != nil || len(ib) > 8<<20 {
		return async.ErrInvalid
	}
	out, err := w.Connection.Atomic(ctx, graphCAS, w.keys(g.ID), strconv.FormatUint(expected, 10), strconv.FormatUint(g.Revision, 10), gb, ib)
	if err != nil {
		return err
	}
	if out.(int64) != 1 {
		return async.ErrConflict
	}
	return nil
}
func (w *Workflows) CreateGraph(ctx context.Context, g async.Graph, intents []async.Intent) error {
	if g.ID == "" || g.Revision != 1 || g.Members == nil || len(g.Children) > 1000 {
		return async.ErrInvalid
	}
	return w.write(ctx, 0, g, intents)
}
func (w *Workflows) RecordMember(ctx context.Context, id string, c async.Completion, advance func(async.Graph) (async.Graph, []async.Intent, error)) error {
	if !c.State.Terminal() || advance == nil {
		return async.ErrInvalid
	}
	for attempt := 0; attempt < 64; attempt++ {
		g, err := w.ReadGraph(ctx, id)
		if err != nil {
			return err
		}
		if _, exists := g.Members[c.ID]; exists {
			return nil
		}
		expected := c.ID == g.CallbackID && g.CallbackClaimed
		for _, child := range g.Children {
			expected = expected || child.ID == c.ID
		}
		if !expected {
			return async.ErrInvalid
		}
		revision := g.Revision
		g.Members[c.ID] = c
		next, intents, err := advance(g)
		if err != nil {
			return err
		}
		next.Revision = revision + 1
		err = w.write(ctx, revision, next, intents)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return err
	}
	return async.ErrBusy
}
func (w *Workflows) intentBackend() intentBackend { return intentBackend{w.Connection, "workflow"} }
func (w *Workflows) ListIntents(ctx context.Context, limit int) ([]async.Intent, error) {
	return w.intentBackend().list(ctx, limit)
}
func (w *Workflows) ClaimIntent(ctx context.Context, source, id, owner string, lease time.Duration) (async.Intent, error) {
	return w.intentBackend().claim(ctx, source, id, owner, lease)
}
func (w *Workflows) MarkIntentDelivered(ctx context.Context, source, id string, fence uint64, owner string) error {
	return w.intentBackend().mark(ctx, source, id, fence, owner)
}

var _ async.WorkflowStore = (*Workflows)(nil)
