package testing

import (
	"context"
	"crypto/sha256"
	"sort"
	"time"

	"github.com/Newton-School/gogo/async"
)

type workerControl struct {
	request       async.WorkerControlRequest
	digest        string
	reply         *async.WorkerControlReply
	retainedUntil time.Time
}

func (m *Memory) liveControlOwner(lease async.WorkerLease, now time.Time) (workerPresence, error) {
	entry, ok := m.workers[lease.WorkerID]
	if !ok || entry.token != sha256.Sum256([]byte(lease.Token)) || !entry.retainedUntil.After(now) || entry.value.Status != async.PresenceOnline || !entry.value.ExpiresAt.After(now) || entry.value.Snapshot.InstanceID == "" {
		return workerPresence{}, async.ErrLeaseLost
	}
	return entry, nil
}

func (m *Memory) SubmitWorkerControl(ctx context.Context, request async.WorkerControlRequest) error {
	if err := m.check(ctx, "submit_worker_control"); err != nil {
		return err
	}
	digest, err := async.WorkerControlDigest(request)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Clock().UTC()
	entries := m.controls[request.WorkerID]
	if old, ok := entries[request.ID]; ok && old.retainedUntil.After(now) {
		if old.digest != digest {
			return async.ErrConflict
		}
		return nil
	}
	if !request.ExpiresAt.After(now) || request.ExpiresAt.Sub(now) > async.MaxWorkerControlLifetime {
		return async.ErrInvalid
	}
	worker, ok := m.workers[request.WorkerID]
	if !ok || !worker.retainedUntil.After(now) || worker.value.Status != async.PresenceOnline || !worker.value.ExpiresAt.After(now) || worker.value.Snapshot.InstanceID != request.InstanceID {
		return async.ErrLeaseLost
	}
	count := 0
	for _, entry := range entries {
		if entry.retainedUntil.After(now) {
			count++
		}
	}
	if count >= async.MaxRetainedWorkerControls {
		return async.ErrConflict
	}
	if m.controls == nil {
		m.controls = map[string]map[string]workerControl{}
	}
	if entries == nil {
		entries = map[string]workerControl{}
		m.controls[request.WorkerID] = entries
	}
	for id, entry := range entries {
		if !entry.retainedUntil.After(now) {
			delete(entries, id)
		}
	}
	entries[request.ID] = workerControl{request: copyOf(request), digest: digest, retainedUntil: now.Add(24 * time.Hour)}
	return nil
}

func (m *Memory) ReceiveWorkerControls(ctx context.Context, lease async.WorkerLease, limit int) ([]async.WorkerControlRequest, error) {
	if err := m.check(ctx, "receive_worker_controls"); err != nil {
		return nil, err
	}
	if async.ValidateWorkerControlReceiver(lease, limit) != nil {
		return nil, async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Clock().UTC()
	worker, err := m.liveControlOwner(lease, now)
	if err != nil {
		return nil, err
	}
	requests := []async.WorkerControlRequest{}
	for _, entry := range m.controls[lease.WorkerID] {
		if entry.retainedUntil.After(now) && entry.request.ExpiresAt.After(now) && entry.request.InstanceID == worker.value.Snapshot.InstanceID && (entry.reply == nil || entry.reply.Outcome == async.ControlAccepted) {
			requests = append(requests, copyOf(entry.request))
		}
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].ID < requests[j].ID })
	return requests[:min(limit, len(requests))], nil
}

func (m *Memory) ReplyWorkerControl(ctx context.Context, lease async.WorkerLease, request async.WorkerControlRequest, outcome async.WorkerControlOutcome) (async.WorkerControlReply, error) {
	if err := m.check(ctx, "reply_worker_control"); err != nil {
		return async.WorkerControlReply{}, err
	}
	digest, err := async.WorkerControlDigest(request)
	if err != nil || async.ValidateWorkerControlReceiver(lease, 1) != nil || request.WorkerID != lease.WorkerID || outcome != async.ControlAccepted && outcome != async.ControlQueueChanged {
		return async.WorkerControlReply{}, async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Clock().UTC()
	worker, err := m.liveControlOwner(lease, now)
	if err != nil || worker.value.Snapshot.InstanceID != request.InstanceID {
		return async.WorkerControlReply{}, async.ErrLeaseLost
	}
	entry, ok := m.controls[request.WorkerID][request.ID]
	if !ok || !entry.retainedUntil.After(now) {
		return async.WorkerControlReply{}, async.ErrNotFound
	}
	if entry.digest != digest {
		return async.WorkerControlReply{}, async.ErrConflict
	}
	if entry.reply != nil {
		if entry.reply.Outcome != outcome {
			return async.WorkerControlReply{}, async.ErrConflict
		}
		return *entry.reply, nil
	}
	if !request.ExpiresAt.After(now) {
		return async.WorkerControlReply{}, async.ErrNotFound
	}
	reply := async.WorkerControlReply{RequestID: request.ID, WorkerID: request.WorkerID, InstanceID: request.InstanceID, Command: request.Command, Outcome: outcome, At: now}
	entry.reply = &reply
	m.controls[request.WorkerID][request.ID] = entry
	return reply, nil
}

func (m *Memory) LookupWorkerControl(ctx context.Context, request async.WorkerControlRequest) (async.WorkerControlReply, error) {
	if err := m.check(ctx, "lookup_worker_control"); err != nil {
		return async.WorkerControlReply{}, err
	}
	digest, err := async.WorkerControlDigest(request)
	if err != nil {
		return async.WorkerControlReply{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.controls[request.WorkerID][request.ID]
	if !ok || !entry.retainedUntil.After(m.Clock()) {
		return async.WorkerControlReply{}, async.ErrNotFound
	}
	if entry.digest != digest {
		return async.WorkerControlReply{}, async.ErrConflict
	}
	if entry.reply == nil {
		return async.WorkerControlReply{}, async.ErrNotFound
	}
	return *entry.reply, nil
}

var _ async.WorkerControlStore = (*Memory)(nil)
