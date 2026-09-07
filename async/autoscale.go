package async

import (
	"context"
	"errors"
	"sync"
	"time"
)

// AutoscaleOptions bounds the local goroutine reservation pool used by Run.
// Min is at least one: scale-to-zero, process recycling and remote resizing are
// not implied. Configure before first use; options are copied at initialization.
type AutoscaleOptions struct {
	Min, Max    int
	Interval    time.Duration
	IdleTimeout time.Duration
}

func normalizedAutoscale(options AutoscaleOptions) (AutoscaleOptions, error) {
	if options.Interval == 0 {
		options.Interval = time.Second
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = 30 * time.Second
	}
	if options.Min < 1 || options.Max < options.Min || options.Max > 1024 || options.Interval < 10*time.Millisecond || options.Interval > time.Minute || options.IdleTimeout < options.Interval || options.IdleTimeout > 24*time.Hour {
		return AutoscaleOptions{}, ErrInvalid
	}
	return options, nil
}

type autoscaleLoop struct {
	cancel    context.CancelFunc
	busy      bool
	retiring  bool
	idleSince time.Time
}

// Only the controller creates loops. State changes share the same lock as
// accepting a reserved delivery, so retirement either keeps a delivery pending
// or observes an accepted handler that must be allowed to drain.
func (w *Worker) runAutoscaled(ctx, reserveCtx context.Context, options AutoscaleOptions) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	loops := map[*autoscaleLoop]bool{}
	start := func() {
		child, cancel := context.WithCancel(reserveCtx)
		loop := &autoscaleLoop{cancel: cancel, idleSince: time.Now()}
		loops[loop] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			defer func() {
				mu.Lock()
				delete(loops, loop)
				mu.Unlock()
			}()
			accept := func() bool {
				mu.Lock()
				defer mu.Unlock()
				if loop.retiring || child.Err() != nil {
					return false
				}
				loop.busy = true
				return true
			}
			finished := func() {
				mu.Lock()
				loop.busy = false
				loop.idleSince = time.Now()
				mu.Unlock()
			}
			w.runReservationLoop(ctx, child, accept, finished)
		}()
	}
	mu.Lock()
	for range options.Min {
		start()
	}
	mu.Unlock()
	ticker := time.NewTicker(options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-reserveCtx.Done():
			// Child reservation contexts are already canceled. Application
			// handlers use ctx, not those contexts, and are never abandoned.
			wg.Wait()
			return ctx.Err()
		case now := <-ticker.C:
			mu.Lock()
			live, busy := 0, 0
			var oldest *autoscaleLoop
			for loop := range loops {
				if loop.retiring {
					continue
				}
				live++
				if loop.busy {
					busy++
				} else if oldest == nil || loop.idleSince.Before(oldest.idleSince) {
					oldest = loop
				}
			}
			if reserveCtx.Err() == nil {
				// Reserving or waiting for the shared global cap is NOT busy.
				// Count retiring loops toward Max until they have actually exited.
				if len(loops) < options.Max && (live < options.Min || live == busy) {
					start()
				} else if live > options.Min && oldest != nil && now.Sub(oldest.idleSince) >= options.IdleTimeout {
					oldest.retiring = true
					oldest.cancel()
				}
			}
			mu.Unlock()
		}
	}
}

// Each loop owns one global slot before reserving. The same slot remains held
// through admission/execution and is also shared with direct Process callers.
// Retirement cancels reservation only; already accepted work uses ctx to drain.
func (w *Worker) runReservationLoop(ctx, reserveCtx context.Context, accept func() bool, finished func()) {
	lastReclaim := w.Clock()
	freshAfterReclaim := false
	for reserveCtx.Err() == nil {
		if err := w.acquireSlot(reserveCtx); err != nil {
			return
		}
		var delivery Delivery
		var err error
		reclaimed := false
		checkReclaim := !freshAfterReclaim
		freshAfterReclaim = false
		if checkReclaim && w.Clock().Sub(lastReclaim) >= w.Lease {
			lastReclaim = w.Clock()
			var deliveries []Delivery
			deliveries, err = w.Broker.Reclaim(reserveCtx, ConsumeOptions{Queues: w.Queues, Consumer: w.ID, ReclaimLimit: 1}, w.Lease)
			if len(deliveries) > 1 {
				// A violating backend may already have reserved them. Do not
				// acknowledge/drop any receipt or exceed the capacity contract.
				err = ErrUnavailable
			} else if err == nil && len(deliveries) == 1 {
				delivery, reclaimed = deliveries[0], true
				// Even when processing exceeds Lease, attempt fresh work
				// before returning to a continuous reclamation backlog.
				freshAfterReclaim = true
			}
		}
		if err == nil && !reclaimed && reserveCtx.Err() == nil {
			delivery, err = w.Broker.Consume(reserveCtx, ConsumeOptions{Queues: w.Queues, Consumer: w.ID, Wait: time.Second})
		}
		if err == nil && reserveCtx.Err() == nil && (accept == nil || accept()) {
			w.report(w.processReserved(ctx, delivery))
			if finished != nil {
				finished()
			}
		}
		w.releaseSlot()
		if err != nil && reserveCtx.Err() == nil {
			delay := 10 * time.Millisecond
			if !errors.Is(err, ErrNotFound) {
				w.report(err)
				delay = 100 * time.Millisecond
			}
			// Custom nonblocking brokers must not turn an empty pool into a
			// tight spin; all waits still end immediately on reservation stop.
			timer := time.NewTimer(delay)
			select {
			case <-reserveCtx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}
