package health

import "github.com/Newton-School/gogo/core/app"

// FromApplication adapts an application lifecycle without copying its mutex or
// probing its resources. A nil application yields a nil source, rejected by New.
// Applications with a separate fatal-runtime or worker-admission state should
// compose this source with their own in-process state. An HTTP handler being
// responsive cannot prove that every application goroutine is responsive.
func FromApplication(application *app.Application) func() State {
	if application == nil {
		return nil
	}
	return func() State {
		status := application.Status()
		return State{
			Live:     !status.Closed && !status.Failed,
			Started:  status.Started,
			Ready:    status.Ready,
			Stopping: status.Stopping || status.Closed,
		}
	}
}
