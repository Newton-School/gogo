package async

import (
	"context"
	"errors"
	"math"
	"time"
)

type localBucket struct {
	tokens float64
	at     time.Time
}

// Admission delays run before claiming an execution lease. The broker retains
// the reservation throughout the bounded wait, so shutdown or provider failure
// leaves recoverable delivery state without incrementing application retries.
// Capacity remains bounded even when Process is called concurrently by a custom
// consumer, not only when using Worker.Run.
func (w *Worker) admit(ctx context.Context, d *definition, envelope Envelope) (func(), error) {
	key := definitionKey(d.name, d.version)
	release := func() {}
	if quota := w.taskSlots[key]; quota != nil {
		select {
		case quota <- struct{}{}:
			release = func() { <-quota }
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if d.options.Rate == nil {
		return release, nil
	}
	for {
		current, err := w.Results.Lookup(ctx, envelope.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			release()
			return nil, err
		}
		if err == nil && (current.State.Terminal() || current.CancelRequested) || !envelope.ExpiresAt.IsZero() && !w.Clock().Before(envelope.ExpiresAt) {
			return release, nil
		}
		allowed, retryAfter, err := w.allowRate(ctx, key, *d.options.Rate)
		if err != nil {
			release()
			return nil, err
		}
		if allowed {
			return release, nil
		}
		// Wake at least every heartbeat for cooperative shutdown and policy
		// rechecks; providers cannot cause a zero-delay spin or unbounded sleep.
		delay := max(time.Millisecond, min(retryAfter, w.Heartbeat))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *Worker) allowRate(ctx context.Context, key string, rate TaskRate) (bool, time.Duration, error) {
	if rate.Scope == "distributed" {
		decision, err := w.RateLimiter.Allow(ctx, "async.task."+key, rate.Limit, 1)
		return decision.Allowed, decision.RetryAfter, err
	}
	w.rateMu.Lock()
	defer w.rateMu.Unlock()
	now := w.Clock()
	bucket, exists := w.rateBuckets[key]
	if !exists {
		bucket = localBucket{tokens: float64(rate.Limit.Burst), at: now}
	}
	elapsed := max(time.Duration(0), now.Sub(bucket.at))
	bucket.tokens = math.Min(float64(rate.Limit.Burst), bucket.tokens+elapsed.Seconds()/rate.Limit.Period.Seconds()*rate.Limit.Rate)
	if now.After(bucket.at) {
		bucket.at = now
	}
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	w.rateBuckets[key] = bucket
	if allowed {
		return true, 0, nil
	}
	delay := math.Ceil((1 - bucket.tokens) / rate.Limit.Rate * float64(rate.Limit.Period))
	return false, time.Duration(math.Min(delay, float64(w.Heartbeat))), nil
}
