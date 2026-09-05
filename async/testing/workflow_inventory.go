package testing

import (
	"context"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/async"
)

func (m *Memory) ListWorkflows(ctx context.Context, cursor string, limit int) (async.WorkflowInventoryPage, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return async.WorkflowInventoryPage{}, async.ErrInvalid
	}
	after := ""
	if cursor != "" {
		var ok bool
		after, ok = strings.CutPrefix(cursor, "memory-v1:")
		if !ok || (async.WorkflowInventoryPage{IDs: []string{after}}).Validate(1) != nil {
			return async.WorkflowInventoryPage{}, async.ErrInvalid
		}
	}
	if err := m.check(ctx, "workflow_inventory"); err != nil {
		return async.WorkflowInventoryPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	start := sort.Search(len(m.workflowIDs), func(i int) bool { return m.workflowIDs[i] > after })
	end := min(start+limit, len(m.workflowIDs))
	page := async.WorkflowInventoryPage{IDs: append([]string{}, m.workflowIDs[start:end]...)}
	if end < len(m.workflowIDs) {
		page.NextCursor = "memory-v1:" + m.workflowIDs[end-1]
	}
	return page, nil
}

var _ async.WorkflowInventory = (*Memory)(nil)
