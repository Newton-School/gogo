package async

import (
	"context"
	"time"
)

// DashboardCatalog is an optional, trusted read port. ListDashboard returns at
// most limit distinct IDs (1..100), with an opaque continuation position. Kinds
// are tasks, workers, workflows, beat (schedules), and schedulers (Beat runners).
// Empty pages may have a continuation. Enumeration is not a point-in-time
// snapshot. Providers must honor cancellation and never scan unrelated keys.
// The HTTP dashboard authorizes enumeration and individual objects separately.
type DashboardCatalog interface {
	ListDashboard(ctx context.Context, kind, cursor string, limit int) (DashboardIDs, error)
	ReadDashboardSchedule(context.Context, string) (PeriodicSchedule, error)
	ReadDashboardBeat(context.Context, string) (BeatObservation, error)
}

type DashboardIDs struct {
	IDs        []string
	NextCursor string
}

// Validate bounds untrusted inventory responses before object lookups.
func (p DashboardIDs) Validate(kind string, limit int) error {
	if !dashboardKind(kind) || limit < 1 || limit > 100 || len(p.IDs) > limit || !validInventoryCursor(p.NextCursor) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range p.IDs {
		valid := idPattern.MatchString(id)
		if kind == "workers" || kind == "schedulers" {
			valid = ValidWorkerID(id)
		}
		if !valid || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

func dashboardKind(kind string) bool {
	switch kind {
	case "tasks", "workers", "workflows", "beat", "schedulers":
		return true
	}
	return false
}

// BeatObservation is heartbeat metadata, not a scheduler leadership grant or
// proof of task dispatch/execution. IDs must identify a unique runner. Backend
// timestamps decide freshness; an expired observation means lost, not stopped.
type BeatObservation struct {
	ID         string
	Status     string // online, error, offline, or lost
	ObservedAt time.Time
	ExpiresAt  time.Time
}

// BeatMonitor records bounded best-effort telemetry after a tick, without
// changing the tick outcome. Implementations must respect context deadlines.
type BeatMonitor interface {
	ObserveBeat(ctx context.Context, id, status string, ttl time.Duration) error
}

func (b *Beat) observe(ctx context.Context, status string) {
	if b == nil || b.Monitor == nil || !ValidWorkerID(b.ID) || ctx == nil {
		return
	}
	observeEvent(context.WithoutCancel(ctx), time.Second, func(err error) {
		if b.OnMonitorError != nil {
			b.OnMonitorError(err)
		}
	}, func(ctx context.Context) error {
		return b.Monitor.ObserveBeat(ctx, b.ID, status, 30*time.Second)
	})
}
