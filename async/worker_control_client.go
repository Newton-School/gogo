package async

import (
	"context"
	"errors"
	"slices"
	"time"
)

var ErrControlReplyTimeout = errors.New("async: worker control reply wait expired")

type ControlSubmission string

const (
	ControlNotSubmitted       ControlSubmission = "not_submitted"
	ControlSubmitted          ControlSubmission = "confirmed"
	ControlSubmissionUnknown  ControlSubmission = "unknown"
	ControlSubmissionRejected ControlSubmission = "rejected"
)

type WorkerControlStatus struct {
	WorkerID   string            `json:"worker_id"`
	InstanceID string            `json:"instance_id"`
	Submission ControlSubmission `json:"submission"`
	// Nil means no reply was observed, not that shutdown succeeded or failed.
	Reply *WorkerControlReply `json:"reply,omitempty"`
}

type WorkerControlReport struct {
	RequestID string                `json:"request_id"`
	Targets   []WorkerControlStatus `json:"targets"`
}

// Complete means every target replied, including explicit rejections. It does
// not mean every request was accepted or that any process stopped.
func (r WorkerControlReport) Complete() bool {
	if len(r.Targets) == 0 {
		return false
	}
	for _, target := range r.Targets {
		if target.Reply == nil {
			return false
		}
	}
	return true
}

func (c Control) authorizeWorkerControl(ctx context.Context, command WorkerCommand, scope, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Unlike an ordinary unscoped enqueue, remote process control always
	// requires an explicitly configured authority and a distinct command grant.
	if c.Client.config.Authorize == nil {
		return ErrDenied
	}
	err := c.Client.config.Authorize(ctx, string(command), scope, id)
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

func (c Control) authorizeWorkerControlTarget(ctx context.Context, request WorkerControlRequest) error {
	if err := c.authorizeWorkerControl(ctx, request.Command, "", request.WorkerID); err != nil {
		return err
	}
	for _, queue := range request.Queues {
		if err := c.authorizeWorkerControl(ctx, request.Command, queue, request.WorkerID); err != nil {
			return err
		}
	}
	return nil
}

// PrepareWorkerShutdown resolves and authorizes an explicit set of currently
// online instances without writing control state. Keep these exact requests
// for dispatch retries; preparing again intentionally targets a fresh snapshot.
func (c Control) PrepareWorkerShutdown(ctx context.Context, ids []string, lifetime time.Duration) ([]WorkerControlRequest, error) {
	if ctx == nil || c.Client == nil || len(ids) < 1 || len(ids) > 64 || lifetime < time.Millisecond || lifetime > MaxWorkerControlLifetime {
		return nil, ErrInvalid
	}
	if c.Client.config.Controls == nil {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !ValidWorkerID(id) || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		if err := c.authorizeWorkerControl(ctx, ShutdownWorker, "", id); err != nil {
			return nil, err
		}
	}
	id, err := c.Client.config.NewID()
	if err != nil {
		return nil, ErrUnavailable
	}
	expires := c.Client.config.Clock().UTC().Add(lifetime)
	requests := make([]WorkerControlRequest, 0, len(ids))
	for _, workerID := range ids {
		presence, err := c.Client.config.Controls.LookupWorker(ctx, workerID)
		if err != nil {
			return nil, controlProviderError(ctx, err)
		}
		if ValidateWorkerSnapshot(presence.Snapshot) != nil || presence.Snapshot.ID != workerID || presence.Snapshot.InstanceID == "" || presence.Status != PresenceOnline {
			return nil, ErrLeaseLost
		}
		queues := slices.Clone(presence.Snapshot.Queues)
		slices.Sort(queues)
		request := WorkerControlRequest{ID: id, WorkerID: workerID, InstanceID: presence.Snapshot.InstanceID, Command: ShutdownWorker, Queues: queues, ExpiresAt: expires}
		if ValidateWorkerControlRequest(request) != nil {
			return nil, ErrUnavailable
		}
		if err := c.authorizeWorkerControlTarget(ctx, request); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func controlProviderError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{ErrInvalid, ErrConflict, ErrLeaseLost, ErrNotFound} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}

// DispatchWorkerControls authorizes every target before any mutation, then
// submits to separate worker partitions and waits at most wait (default 5s,
// maximum 1m) for replies. Errors return a partial report: unknown submissions
// may have been accepted, nil replies never mean success, and no automatic
// retargeting/retry broadens the instance set. Accepted replies acknowledge a
// drain request, not process exit. Authority is rechecked for each submission
// and observed reply; no separate inspection grant is required after mutation.
func (c Control) DispatchWorkerControls(ctx context.Context, requests []WorkerControlRequest, wait time.Duration) (WorkerControlReport, error) {
	if ctx == nil || c.Client == nil || len(requests) < 1 || len(requests) > 64 {
		return WorkerControlReport{}, ErrInvalid
	}
	if c.Client.config.Controls == nil {
		return WorkerControlReport{}, ErrUnavailable
	}
	if wait == 0 {
		wait = 5 * time.Second
	}
	if wait < time.Millisecond || wait > time.Minute {
		return WorkerControlReport{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, request := range requests {
		if ValidateWorkerControlRequest(request) != nil || request.ID != requests[0].ID || seen[request.WorkerID] {
			return WorkerControlReport{}, ErrInvalid
		}
		seen[request.WorkerID] = true
	}
	// Validate the whole bounded shape before copying. This typed snapshot
	// cannot turn a JSON encoding failure (e.g. year 10000) into an empty slice.
	requests = slices.Clone(requests)
	for index := range requests {
		requests[index].Queues = slices.Clone(requests[index].Queues)
	}
	for _, request := range requests {
		if err := c.authorizeWorkerControlTarget(ctx, request); err != nil {
			return WorkerControlReport{}, err
		}
	}
	store := c.Client.config.Controls
	previous := make([]*WorkerControlReply, len(requests))
	// Resolve all fresh targets before the first write. A previously completed
	// exact request can be read back even after its worker went offline/restarted.
	for index, request := range requests {
		reply, err := store.LookupWorkerControl(ctx, request)
		if err == nil {
			if ValidateWorkerControlReply(request, reply) != nil {
				return WorkerControlReport{}, ErrUnavailable
			}
			previous[index] = &reply
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			return WorkerControlReport{}, controlProviderError(ctx, err)
		}
		presence, err := store.LookupWorker(ctx, request.WorkerID)
		if err != nil {
			return WorkerControlReport{}, controlProviderError(ctx, err)
		}
		if ValidateWorkerSnapshot(presence.Snapshot) != nil || presence.Snapshot.ID != request.WorkerID || presence.Snapshot.InstanceID != request.InstanceID || presence.Status != PresenceOnline || !sameWorkerQueues(presence.Snapshot.Queues, request.Queues) {
			return WorkerControlReport{}, ErrLeaseLost
		}
		if err := c.authorizeWorkerControlTarget(ctx, request); err != nil {
			return WorkerControlReport{}, err
		}
	}
	report := WorkerControlReport{RequestID: requests[0].ID, Targets: make([]WorkerControlStatus, len(requests))}
	for index, request := range requests {
		report.Targets[index] = WorkerControlStatus{WorkerID: request.WorkerID, InstanceID: request.InstanceID, Submission: ControlNotSubmitted}
	}
	for index, request := range requests {
		if err := c.authorizeWorkerControlTarget(ctx, request); err != nil {
			return report, err
		}
		if previous[index] != nil {
			report.Targets[index].Submission = ControlSubmitted
			report.Targets[index].Reply = previous[index]
			continue
		}
		err := store.SubmitWorkerControl(ctx, request)
		if err != nil {
			report.Targets[index].Submission = ControlSubmissionUnknown
			if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrLeaseLost) {
				report.Targets[index].Submission = ControlSubmissionRejected
			}
			return report, controlProviderError(ctx, err)
		}
		report.Targets[index].Submission = ControlSubmitted
	}
	child, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		for index, request := range requests {
			if report.Targets[index].Reply != nil {
				continue
			}
			if err := c.authorizeWorkerControlTarget(child, request); err != nil {
				if child.Err() != nil && ctx.Err() == nil {
					return report, ErrControlReplyTimeout
				}
				return report, err
			}
			reply, err := store.LookupWorkerControl(child, request)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				if child.Err() != nil && ctx.Err() == nil {
					return report, ErrControlReplyTimeout
				}
				return report, controlProviderError(ctx, err)
			}
			if ValidateWorkerControlReply(request, reply) != nil {
				return report, ErrUnavailable
			}
			if err := c.authorizeWorkerControlTarget(child, request); err != nil {
				if child.Err() != nil && ctx.Err() == nil {
					return report, ErrControlReplyTimeout
				}
				return report, err
			}
			report.Targets[index].Reply = &reply
		}
		if report.Complete() {
			return report, nil
		}
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		case <-child.Done():
			return report, ErrControlReplyTimeout
		case <-ticker.C:
		}
	}
}
