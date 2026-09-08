package app

import "context"

// Status is a secret-free application lifecycle snapshot, not a dependency
// health check. Database and other resource availability are checked separately.
type Status struct {
	// Started records that Start completed successfully, including resources and
	// Ready hooks. It remains true after admission stops or shutdown completes.
	Started bool
	// Ready requires completed startup, open admission and an uncanceled lifetime.
	Ready bool
	// Stopping includes stopped admission, a canceled lifetime and closed state.
	Stopping bool
	// Closed means the lifecycle owner completed its cleanup attempts; timed-out
	// user hooks can still be running, as documented by Application.Close.
	Closed bool
	// Failed reports startup or cleanup errors without returning their contents.
	// An invalid lifetime context also fails this snapshot closed.
	Failed bool
}

// Status reads lifecycle state without opening resources, running hooks or
// changing admission. A nil Application fails closed. Concurrent startup may
// conservatively report not ready; successful startup remains latched afterward.
func (a *Application) Status() Status {
	if a == nil {
		return Status{Stopping: true, Closed: true, Failed: true}
	}
	a.mu.Lock()
	lifetime := a.lifetime
	a.mu.Unlock()

	// Context is an interface: Err may invoke application code or panic. Never
	// call it while holding the lifecycle mutex. Read the lifecycle flags after
	// it returns so callback-driven StopAdmission/Close cannot leave stale ready.
	alive, invalid := statusLifetime(lifetime)
	a.mu.Lock()
	defer a.mu.Unlock()
	status := Status{
		Started:  a.started,
		Stopping: a.admissionStopped || a.state >= stopping || lifetime != nil && !alive,
		Closed:   a.state == closed,
		Failed:   a.startErr != nil || a.closeErr != nil || invalid,
	}
	status.Ready = a.ready && a.started && a.state == running && alive && !status.Stopping && !status.Failed
	return status
}

func statusLifetime(ctx context.Context) (alive, invalid bool) {
	if ctx == nil {
		return false, false
	}
	defer func() {
		if recover() != nil {
			alive, invalid = false, true
		}
	}()
	return ctx.Err() == nil, false
}
