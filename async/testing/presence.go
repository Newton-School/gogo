package testing

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/Newton-School/gogo/async"
)

type workerPresence struct {
	token         [32]byte
	value         async.WorkerPresence
	retainedUntil time.Time
}

func (m *Memory) writeWorker(ctx context.Context, operation string, lease async.WorkerLease, snapshot async.WorkerSnapshot, ttl time.Duration) error {
	if err := m.check(ctx, operation); err != nil {
		return err
	}
	if err := async.ValidateWorkerLease(lease, snapshot, ttl); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[string]workerPresence{}
	}
	now := m.Clock().UTC()
	token := sha256.Sum256([]byte(lease.Token))
	old, exists := m.workers[lease.WorkerID]
	if exists && !old.retainedUntil.After(now) {
		delete(m.workers, lease.WorkerID)
		exists = false
	}
	if operation == "claim_worker" {
		if exists && old.token != token && old.value.Status == async.PresenceOnline && old.value.ExpiresAt.After(now) {
			return async.ErrConflict
		}
	} else if !exists || old.token != token || old.value.Status != async.PresenceOnline {
		return async.ErrLeaseLost
	}
	status, expires := async.PresenceOnline, now.Add(ttl)
	if operation == "release_worker" {
		status, expires = async.PresenceOffline, now
	}
	m.workers[lease.WorkerID] = workerPresence{token: token, value: async.WorkerPresence{Snapshot: copyOf(snapshot), Status: status, ObservedAt: now, ExpiresAt: expires}, retainedUntil: now.Add(24 * time.Hour)}
	return nil
}

func (m *Memory) ClaimWorker(ctx context.Context, lease async.WorkerLease, snapshot async.WorkerSnapshot, ttl time.Duration) error {
	return m.writeWorker(ctx, "claim_worker", lease, snapshot, ttl)
}
func (m *Memory) RenewWorker(ctx context.Context, lease async.WorkerLease, snapshot async.WorkerSnapshot, ttl time.Duration) error {
	return m.writeWorker(ctx, "renew_worker", lease, snapshot, ttl)
}
func (m *Memory) ReleaseWorker(ctx context.Context, lease async.WorkerLease, snapshot async.WorkerSnapshot) error {
	return m.writeWorker(ctx, "release_worker", lease, snapshot, time.Millisecond)
}
func (m *Memory) LookupWorker(ctx context.Context, id string) (async.WorkerPresence, error) {
	if err := m.check(ctx, "lookup_worker"); err != nil {
		return async.WorkerPresence{}, err
	}
	if !async.ValidWorkerID(id) {
		return async.WorkerPresence{}, async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Clock().UTC()
	entry, exists := m.workers[id]
	if !exists || !entry.retainedUntil.After(now) {
		delete(m.workers, id)
		return async.WorkerPresence{}, async.ErrNotFound
	}
	out := copyOf(entry.value)
	if out.Status == async.PresenceOnline && !out.ExpiresAt.After(now) {
		out.Status = async.PresenceLost
	}
	return out, nil
}

var _ async.WorkerPresenceStore = (*Memory)(nil)
