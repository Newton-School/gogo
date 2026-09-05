package async

import (
	"context"
	"errors"
	"time"
)

// Remote shutdown cancels only reservation. Accepted work drains under the
// caller's original context; cancellation of that context retains the existing
// cooperative task cancellation and management shutdown deadline behavior.
func (w *Worker) startControls(ctx context.Context, lease WorkerLease, stopReserving context.CancelFunc) func() {
	if w.Controls == nil {
		return func() {}
	}
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := min(w.Heartbeat, 100*time.Millisecond)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			callCtx, stop := context.WithTimeout(child, min(w.Heartbeat, 5*time.Second))
			requests, err := w.Controls.ReceiveWorkerControls(callCtx, lease, 16)
			stop()
			if err == nil && len(requests) > 16 {
				err = ErrUnavailable
			}
			if err != nil {
				if child.Err() == nil {
					w.report(err)
				}
				if errors.Is(err, ErrLeaseLost) {
					return
				}
			} else {
				for _, request := range requests {
					if child.Err() != nil {
						return
					}
					snapshot := w.Snapshot()
					if ValidateWorkerControlRequest(request) != nil || request.WorkerID != lease.WorkerID || request.InstanceID != snapshot.InstanceID {
						w.report(ErrInvalid)
						continue
					}
					outcome := ControlAccepted
					if !sameWorkerQueues(request.Queues, snapshot.Queues) {
						outcome = ControlQueueChanged
					}
					callCtx, stop := context.WithTimeout(child, min(w.Heartbeat, 5*time.Second))
					reply, err := w.Controls.ReplyWorkerControl(callCtx, lease, request, outcome)
					stop()
					if err != nil {
						if child.Err() == nil {
							w.report(err)
						}
						// The reply may have committed despite a lost response.
						// Read only this exact immutable request; never retarget.
						callCtx, stop = context.WithTimeout(child, min(w.Heartbeat, 5*time.Second))
						reply, err = w.Controls.LookupWorkerControl(callCtx, request)
						stop()
						if err != nil {
							continue
						}
					}
					if ValidateWorkerControlReply(request, reply) != nil || reply.Outcome != outcome {
						w.report(ErrUnavailable)
						continue
					}
					if outcome == ControlAccepted {
						// A reply is durable before this lifecycle effect. The
						// acknowledgment does not claim this line or drain ran.
						stopReserving()
						return
					}
				}
			}
			select {
			case <-child.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
