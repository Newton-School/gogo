package redis

import (
	"context"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func decodeWorkflowCursor(cursor string) (int, string, error) {
	if len(cursor) > 512 {
		return 0, "", async.ErrInvalid
	}
	if cursor == "" {
		return 0, "", nil
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 3 || parts[0] != "redis-v1" {
		return 0, "", async.ErrInvalid
	}
	partition, err := strconv.Atoi(parts[1])
	if err != nil || partition < 0 || partition >= 64 || strconv.Itoa(partition) != parts[1] {
		return 0, "", async.ErrInvalid
	}
	if parts[2] != "" && ((async.WorkflowInventoryPage{IDs: []string{parts[2]}}).Validate(1) != nil || connector.Partition(parts[2]) != partition) {
		return 0, "", async.ErrInvalid
	}
	return partition, parts[2], nil
}

// ListWorkflows reads a strict lexicographic page from exactly one configured
// workflow partition. The inventory and graph are written in the same CAS.
// It never scans unrelated keys or treats a missing graph as completed. Older
// development builds without this index require explicit migration/known-ID
// reconciliation; database-wide SCAN is not an implicit recovery fallback.
func (w *Workflows) ListWorkflows(ctx context.Context, cursor string, limit int) (async.WorkflowInventoryPage, error) {
	if ctx == nil || w == nil || w.Connection == nil || w.Connection.Role() == connector.CacheRole || limit < 1 || limit > 1000 {
		return async.WorkflowInventoryPage{}, async.ErrInvalid
	}
	partition, after, err := decodeWorkflowCursor(cursor)
	if err != nil {
		return async.WorkflowInventoryPage{}, err
	}
	index, _ := w.Connection.PartitionIndex("workflow", partition, "records-v1")
	minimum := "-"
	if after != "" {
		minimum = "(" + after
	}
	ids, err := w.Connection.Client().ZRangeByLex(ctx, index, &redigo.ZRangeBy{Min: minimum, Max: "+", Offset: 0, Count: int64(limit)}).Result()
	if err != nil {
		return async.WorkflowInventoryPage{}, err
	}
	page := async.WorkflowInventoryPage{IDs: ids}
	if len(ids) == limit {
		page.NextCursor = "redis-v1:" + strconv.Itoa(partition) + ":" + ids[len(ids)-1]
	} else if partition < 63 {
		page.NextCursor = "redis-v1:" + strconv.Itoa(partition+1) + ":"
	}
	if page.Validate(limit) != nil {
		return async.WorkflowInventoryPage{}, async.ErrUnavailable
	}
	for _, id := range ids {
		if connector.Partition(id) != partition {
			return async.WorkflowInventoryPage{}, async.ErrUnavailable
		}
	}
	return page, nil
}

var _ async.WorkflowInventory = (*Workflows)(nil)
