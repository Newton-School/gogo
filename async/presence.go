package async

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"
)

type PresenceStatus string

const (
	PresenceOnline  PresenceStatus = "online"
	PresenceLost    PresenceStatus = "lost"
	PresenceOffline PresenceStatus = "offline"
	PresenceUnknown PresenceStatus = "unknown"
)

// WorkerPresence is observational, never task execution authority. Lost means
// the heartbeat expired; offline means its runner returned. Neither proves that
// arbitrary goroutines, subprocesses or previously reserved tasks have stopped.
// ObservedAt/ExpiresAt come from the backend clock, not Snapshot.At.
type WorkerPresence struct {
	Snapshot   WorkerSnapshot `json:"snapshot"`
	Status     PresenceStatus `json:"status"`
	ObservedAt time.Time      `json:"observed_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
}

// WorkerLease is an instance ownership token for monitoring updates, not a task
// fence. Never send it to inspection clients or ordinary logs/serialization.
type WorkerLease struct{ WorkerID, Token string }

func (WorkerLease) String() string               { return "async.WorkerLease{token:redacted}" }
func (l WorkerLease) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, l.String()) }
func (WorkerLease) MarshalJSON() ([]byte, error) { return nil, ErrDenied }

// WorkerPresenceStore is a trusted backend port; application reads use Control.
// Claim excludes another live instance. Renew/Release compare exact ownership
// and never overwrite a replacement instance. Store a digest of the ownership
// token, not its plaintext. The optional public Snapshot.InstanceID cannot
// change under the same owner; a new runner uses both a new instance and lease.
// A late renewal may restore the
// same instance if no replacement claimed it. Stored JSON remains opaque, and
// lost/offline records are retained for at most 24 hours since the last write.
// Missing is ErrNotFound, ownership loss ErrLeaseLost, live collision ErrConflict.
// Implementations must honor context cancellation/deadlines; runner shutdown
// waits for the current renewal to return before publishing its offline state.
type WorkerPresenceStore interface {
	ClaimWorker(context.Context, WorkerLease, WorkerSnapshot, time.Duration) error
	RenewWorker(context.Context, WorkerLease, WorkerSnapshot, time.Duration) error
	ReleaseWorker(context.Context, WorkerLease, WorkerSnapshot) error
	LookupWorker(context.Context, string) (WorkerPresence, error)
}

var workerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,191}$`)
var registeredTaskPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,191}@[1-9][0-9]{0,18}$`)

func ValidWorkerID(id string) bool { return workerIDPattern.MatchString(id) }

// ValidateWorkerSnapshot is shared by external adapters before any write. The
// explicit snapshot excludes arguments, outputs, headers and identity secrets.
func ValidateWorkerSnapshot(snapshot WorkerSnapshot) error {
	if !ValidWorkerID(snapshot.ID) || snapshot.At.IsZero() || snapshot.Concurrency < 1 || snapshot.Concurrency > 1024 || len(snapshot.Queues) < 1 || len(snapshot.Queues) > 64 || len(snapshot.Registered) > 4096 || len(snapshot.Active) > 1024 {
		return ErrInvalid
	}
	if snapshot.InstanceID != "" && !idPattern.MatchString(snapshot.InstanceID) {
		return ErrInvalid
	}
	unique := map[string]bool{}
	for _, queue := range snapshot.Queues {
		if !namePattern.MatchString(queue) || unique[queue] {
			return ErrInvalid
		}
		unique[queue] = true
	}
	for _, task := range snapshot.Registered {
		if !registeredTaskPattern.MatchString(task) {
			return ErrInvalid
		}
	}
	unique = map[string]bool{}
	for _, task := range snapshot.Active {
		if !idPattern.MatchString(task.ID) || unique[task.ID] || !namePattern.MatchString(task.Task) || len(task.Scope) > 256 || !utf8.ValidString(task.Scope) || task.Retries < 0 || task.StartedAt.IsZero() {
			return ErrInvalid
		}
		unique[task.ID] = true
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > MaxPayloadBytes {
		return ErrInvalid
	}
	return nil
}

func ValidateWorkerLease(lease WorkerLease, snapshot WorkerSnapshot, ttl time.Duration) error {
	if !idPattern.MatchString(lease.Token) || lease.WorkerID != snapshot.ID || ttl < time.Millisecond || ttl > time.Hour {
		return ErrInvalid
	}
	return ValidateWorkerSnapshot(snapshot)
}

// startPresence runs only around Run/RunOnce, never an isolated Process call.
// Startup ownership must be confirmed before reserving. Later monitoring
// outages are reported but do not rewrite task results or cancel handlers.
func (w *Worker) startPresence(ctx context.Context) (func(), WorkerLease, error) {
	if w.Presence == nil {
		return func() {}, WorkerLease{}, nil
	}
	token, err := NewID()
	if err != nil {
		return nil, WorkerLease{}, err
	}
	instance, err := NewID()
	if err != nil {
		return nil, WorkerLease{}, err
	}
	w.activityMu.Lock()
	if w.instanceID != "" {
		w.activityMu.Unlock()
		return nil, WorkerLease{}, ErrConflict
	}
	w.instanceID = instance
	w.activityMu.Unlock()
	clearInstance := func() {
		w.activityMu.Lock()
		if w.instanceID == instance {
			w.instanceID = ""
		}
		w.activityMu.Unlock()
	}
	lease := WorkerLease{WorkerID: w.ID, Token: token}
	snapshot := w.Snapshot()
	if err := ValidateWorkerLease(lease, snapshot, w.Lease); err != nil {
		clearInstance()
		return nil, WorkerLease{}, err
	}
	timeout := min(w.Heartbeat, 5*time.Second)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	err = w.Presence.ClaimWorker(callCtx, lease, snapshot, w.Lease)
	cancel()
	if err != nil {
		clearInstance()
		return nil, WorkerLease{}, err
	}
	child, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				callCtx, cancel := context.WithTimeout(child, timeout)
				err := w.Presence.RenewWorker(callCtx, lease, w.Snapshot(), w.Lease)
				cancel()
				if err != nil {
					if child.Err() == nil {
						w.report(err)
					}
					if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrConflict) {
						return
					}
				}
			}
		}
	}()
	return func() {
		defer clearInstance()
		stop()
		<-done
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		w.report(w.Presence.ReleaseWorker(callCtx, lease, w.Snapshot()))
	}, lease, nil
}

// InspectWorkers reads an explicit bounded target set. Missing entries are
// unknown, never offline/success. A backend or authorization failure returns
// already inspected targets plus an error. Queue and active-task scopes are
// checked separately; hidden active tasks do not appear in counts or payloads.
func (c Control) InspectWorkers(ctx context.Context, ids []string) ([]WorkerPresence, error) {
	if ctx == nil || c.Client == nil || len(ids) < 1 || len(ids) > 64 {
		return nil, ErrInvalid
	}
	if c.Client.config.Presence == nil {
		return nil, ErrUnavailable
	}
	unique := map[string]bool{}
	for _, id := range ids {
		if !ValidWorkerID(id) || unique[id] {
			return nil, ErrInvalid
		}
		unique[id] = true
		if err := c.authorizeInspection(ctx, "", id); err != nil {
			return nil, err
		}
	}
	out := make([]WorkerPresence, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		p, err := c.Client.config.Presence.LookupWorker(ctx, id)
		if errors.Is(err, ErrNotFound) {
			p = WorkerPresence{Status: PresenceUnknown, Snapshot: WorkerSnapshot{ID: id}}
		} else if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, ErrUnavailable
		} else if ValidateWorkerSnapshot(p.Snapshot) != nil || p.Snapshot.ID != id || p.Status != PresenceOnline && p.Status != PresenceOffline && p.Status != PresenceLost || p.ObservedAt.IsZero() || p.ExpiresAt.IsZero() {
			return out, ErrUnavailable
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := c.authorizeInspection(ctx, "", id); err != nil {
			return out, err
		}
		for _, queue := range p.Snapshot.Queues {
			if err := c.authorizeInspection(ctx, queue, id); err != nil {
				return out, err
			}
		}
		visible := make([]TaskActivity, 0, len(p.Snapshot.Active))
		for _, activity := range p.Snapshot.Active {
			if err := c.authorizeInspection(ctx, activity.Scope, activity.ID); err == nil {
				visible = append(visible, activity)
			} else if !errors.Is(err, ErrDenied) {
				return out, err
			}
		}
		p.Snapshot.Active = visible
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out = append(out, cloneJSON(p))
	}
	return out, nil
}

// Inspection needs an unavailable authority to remain distinguishable from
// an explicit hidden task. Other provider details remain private.
func (c Control) authorizeInspection(ctx context.Context, scope, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Client.config.Authorize == nil {
		return c.Client.authorize(ctx, "inspect", scope, id)
	}
	err := c.Client.config.Authorize(ctx, "inspect", scope, id)
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
