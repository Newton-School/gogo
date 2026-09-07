package async

import (
	"context"
	"errors"
	"slices"
)

// InspectQueues reads 1..64 distinct named queues. Every queue needs its own
// inspect grant before and after the provider lookup. A malformed, incomplete,
// denied or unavailable batch returns no statistics, never a partial count.
func (c Control) InspectQueues(ctx context.Context, queues []string) (out []QueueStats, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, ErrUnavailable
		}
		if ctx != nil && ctx.Err() != nil {
			out, err = nil, ctx.Err()
		}
	}()
	if ctx == nil || c.Client == nil || len(queues) < 1 || len(queues) > 64 {
		return nil, ErrInvalid
	}
	targets := slices.Clone(queues)
	requested := make(map[string]bool, len(targets))
	for _, queue := range targets {
		if !namePattern.MatchString(queue) || requested[queue] {
			return nil, ErrInvalid
		}
		requested[queue] = true
	}
	for _, queue := range targets {
		if err := c.authorizeInspection(ctx, queue, ""); err != nil {
			return nil, err
		}
	}
	// The provider receives its own slice; it cannot rewrite the authorized
	// target set used to validate its response or mutate the caller's input.
	rows, err := c.Client.config.Broker.Inspect(ctx, slices.Clone(targets))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrUnavailable
	}
	if len(rows) != len(targets) {
		return nil, ErrUnavailable
	}
	byQueue := make(map[string]QueueStats, len(rows))
	for _, row := range rows {
		if !requested[row.Queue] || row.Queued < 0 || row.Pending < 0 || row.Quarantined < 0 {
			return nil, ErrUnavailable
		}
		if _, duplicate := byQueue[row.Queue]; duplicate {
			return nil, ErrUnavailable
		}
		byQueue[row.Queue] = row
	}
	for _, queue := range targets {
		if err := c.authorizeInspection(ctx, queue, ""); err != nil {
			return nil, err
		}
	}
	// Return detached statistics in requested order, independent of provider
	// ordering or a backing slice retained by the provider.
	out = make([]QueueStats, 0, len(targets))
	for _, queue := range targets {
		out = append(out, byQueue[queue])
	}
	return out, nil
}

// InspectWorker applies the same worker, queue and active-task inspection
// grants as remote presence inspection. A direct local pointer is not a grant.
// Explicitly denied activity is hidden; authority outages discard the snapshot.
// Configure worker fields before concurrent use, as required by Worker itself.
func (c Control) InspectWorker(ctx context.Context, worker *Worker) (out WorkerSnapshot, err error) {
	defer func() {
		if recover() != nil {
			out, err = WorkerSnapshot{}, ErrUnavailable
		}
		if ctx != nil && ctx.Err() != nil {
			out, err = WorkerSnapshot{}, ctx.Err()
		}
	}()
	if ctx == nil || c.Client == nil || worker == nil || !ValidWorkerID(worker.ID) {
		return WorkerSnapshot{}, ErrInvalid
	}
	id := worker.ID
	if err := c.authorizeInspection(ctx, "", id); err != nil {
		return WorkerSnapshot{}, err
	}
	snapshot := worker.Snapshot()
	if snapshot.ID != id || ValidateWorkerSnapshot(snapshot) != nil {
		return WorkerSnapshot{}, ErrUnavailable
	}
	if err := c.authorizeInspection(ctx, "", id); err != nil {
		return WorkerSnapshot{}, err
	}
	for _, queue := range snapshot.Queues {
		if err := c.authorizeInspection(ctx, queue, id); err != nil {
			return WorkerSnapshot{}, err
		}
	}
	visible := make([]TaskActivity, 0, len(snapshot.Active))
	for _, activity := range snapshot.Active {
		if err := c.authorizeInspection(ctx, activity.Scope, activity.ID); err == nil {
			visible = append(visible, activity)
		} else if !errors.Is(err, ErrDenied) {
			return WorkerSnapshot{}, err
		}
	}
	snapshot.Active = visible
	return cloneJSON(snapshot), nil
}
