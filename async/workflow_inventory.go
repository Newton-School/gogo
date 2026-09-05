package async

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"
)

// WorkflowInventory is an optional trusted maintenance port, not an
// authorization boundary. Implementations enumerate only their own configured
// namespace and must honor context cancellation. A page contains at most limit
// unique workflow IDs. Empty pages may still have a continuation cursor.
// A cursor is backend-specific; empty starts/ends one pass, not a snapshot.
// Concurrent creation before a cursor can become visible on the next pass.
type WorkflowInventory interface {
	ListWorkflows(ctx context.Context, cursor string, limit int) (WorkflowInventoryPage, error)
}

type WorkflowInventoryPage struct {
	IDs        []string `json:"ids"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

func (p WorkflowInventoryPage) Validate(limit int) error {
	if limit < 1 || limit > 1000 || len(p.IDs) > limit || !validInventoryCursor(p.NextCursor) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range p.IDs {
		if !idPattern.MatchString(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

func validInventoryCursor(cursor string) bool {
	return len(cursor) <= 512 && utf8.ValidString(cursor) && !strings.ContainsAny(cursor, "\x00\r\n")
}

// WorkflowReconcileCursor can be persisted by a maintenance runner. Keep it
// with the same configured backend. EndOfPass means inventory enumeration ended;
// a remaining WorkflowID must still finish its bounded task pass.
type WorkflowReconcileCursor struct {
	Inventory   string `json:"inventory,omitempty"`
	WorkflowID  string `json:"workflow_id,omitempty"`
	AfterTaskID string `json:"after_task_id,omitempty"`
	EndOfPass   bool   `json:"end_of_pass"`
}

type WorkflowReconcileReport struct {
	InventoryPages  int                     `json:"inventory_pages"`
	WorkflowID      string                  `json:"workflow_id,omitempty"`
	MissingWorkflow bool                    `json:"missing_workflow"`
	Repair          WorkflowRepairReport    `json:"repair"`
	PassComplete    bool                    `json:"pass_complete"`
	Cursor          WorkflowReconcileCursor `json:"cursor"`
}

// WorkflowReconciler visits one workflow's bounded task page per call. It
// retains that workflow only until a full task pass is exhausted, including
// active tasks and state gaps, then advances the inventory. Denied or missing
// workflows are reported and skipped for this pass; backend failures retain the
// position. This prevents a live early child or inaccessible workflow from
// starving the rest. Retrying is still the caller's explicit scheduling policy.
//
// Configure fields before concurrent use; use Position to read the cursor.
// At most one call is active; overlapping calls return ErrBusy. The explicit
// reconcile_inventory grant covers namespace ID enumeration only. Actual task
// inspection/mutation still requires the workflow/task reconcile grants.
type WorkflowReconciler struct {
	Client           *Client
	TaskBatchSize    int
	InventoryLookups int
	Cursor           WorkflowReconcileCursor
	runMu            sync.Mutex
	mu               sync.Mutex
}

func (r *WorkflowReconciler) Position() WorkflowReconcileCursor {
	if r == nil {
		return WorkflowReconcileCursor{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Cursor
}

func (r *WorkflowReconciler) Tick(ctx context.Context) error {
	_, err := r.Reconcile(ctx)
	return err
}

func (c *Client) authorizeWorkflowInventory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.config.Authorize == nil {
		return ErrDenied
	}
	err := c.config.Authorize(ctx, "reconcile_inventory", "", "")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	return ErrUnavailable
}

func (r *WorkflowReconciler) Reconcile(ctx context.Context) (WorkflowReconcileReport, error) {
	if r == nil || ctx == nil || r.Client == nil || r.TaskBatchSize < 0 || r.TaskBatchSize > 1000 || r.InventoryLookups < 0 || r.InventoryLookups > 64 {
		return WorkflowReconcileReport{}, ErrInvalid
	}
	if !r.runMu.TryLock() {
		return WorkflowReconcileReport{}, ErrBusy
	}
	defer r.runMu.Unlock()
	cursor := r.Position()
	if !validInventoryCursor(cursor.Inventory) || cursor.WorkflowID != "" && !idPattern.MatchString(cursor.WorkflowID) || cursor.AfterTaskID != "" && (!idPattern.MatchString(cursor.AfterTaskID) || cursor.WorkflowID == "") || cursor.EndOfPass && cursor.Inventory != "" {
		return WorkflowReconcileReport{}, ErrInvalid
	}
	if err := r.Client.authorizeWorkflowInventory(ctx); err != nil {
		return WorkflowReconcileReport{}, err
	}
	inventory, ok := r.Client.config.Workflows.(WorkflowInventory)
	if !ok {
		return WorkflowReconcileReport{}, ErrUnavailable
	}
	report := WorkflowReconcileReport{}
	finish := func(err error) (WorkflowReconcileReport, error) {
		r.mu.Lock()
		r.Cursor = cursor
		r.mu.Unlock()
		report.Cursor = cursor
		report.PassComplete = cursor.EndOfPass && cursor.WorkflowID == ""
		return report, err
	}
	if cursor.EndOfPass && cursor.WorkflowID == "" {
		cursor = WorkflowReconcileCursor{}
	}
	lookups := r.InventoryLookups
	if lookups == 0 {
		lookups = 4
	}
	for cursor.WorkflowID == "" && report.InventoryPages < lookups {
		if err := r.Client.authorizeWorkflowInventory(ctx); err != nil {
			return finish(err)
		}
		page, err := inventory.ListWorkflows(ctx, cursor.Inventory, 1)
		if err != nil {
			return finish(repairProviderError(ctx, err))
		}
		if page.Validate(1) != nil || page.NextCursor != "" && page.NextCursor == cursor.Inventory {
			return finish(ErrUnavailable)
		}
		if err := r.Client.authorizeWorkflowInventory(ctx); err != nil {
			return finish(err)
		}
		report.InventoryPages++
		cursor.Inventory = page.NextCursor
		cursor.EndOfPass = page.NextCursor == ""
		if len(page.IDs) == 1 {
			cursor.WorkflowID = page.IDs[0]
		}
		if cursor.EndOfPass {
			break
		}
	}
	if cursor.WorkflowID == "" {
		return finish(nil)
	}
	report.WorkflowID = cursor.WorkflowID
	repair, err := r.Client.ReconcileWorkflow(ctx, cursor.WorkflowID, WorkflowRepairOptions{AfterTaskID: cursor.AfterTaskID, BatchSize: r.TaskBatchSize})
	report.Repair = repair
	switch {
	case err == nil || errors.Is(err, ErrWorkflowStateGap):
		cursor.AfterTaskID = repair.NextTaskID
		if repair.NextTaskID == "" || repair.State.Terminal() {
			cursor.WorkflowID = ""
			cursor.AfterTaskID = ""
		}
	case errors.Is(err, ErrNotFound):
		// Missing authoritative workflow state requires operator/outbox
		// reconciliation. Keep its ID in the report and advance this pass,
		// never invent a replacement graph or remove its inventory entry.
		report.MissingWorkflow = true
		cursor.WorkflowID = ""
		cursor.AfterTaskID = ""
		err = ErrWorkflowStateGap
	case errors.Is(err, ErrDenied):
		cursor.WorkflowID = ""
		cursor.AfterTaskID = ""
	default:
		if repair.NextTaskID != "" {
			cursor.AfterTaskID = repair.NextTaskID
		}
	}
	return finish(err)
}
