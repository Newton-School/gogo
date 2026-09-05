package async

import (
	"errors"
	"math"
	"time"
)

type RetryPolicy struct {
	MaxRetries    *int
	Unlimited     bool
	Delay         time.Duration
	Backoff       bool
	BackoffFactor time.Duration
	BackoffMax    time.Duration
	DisableJitter bool
	AutoRetryFor  func(error) bool
}

func DefaultRetryPolicy() RetryPolicy {
	max := 3
	return RetryPolicy{MaxRetries: &max, Delay: 180 * time.Second, BackoffFactor: time.Second, BackoffMax: 600 * time.Second}
}

func (p RetryPolicy) normalized() (RetryPolicy, error) {
	d := DefaultRetryPolicy()
	if p.Unlimited {
		p.MaxRetries = nil
	} else if p.MaxRetries == nil {
		p.MaxRetries = d.MaxRetries
	} else {
		n := *p.MaxRetries
		p.MaxRetries = &n
	}
	if p.Delay == 0 {
		p.Delay = d.Delay
	}
	if p.BackoffFactor == 0 {
		p.BackoffFactor = d.BackoffFactor
	}
	if p.BackoffMax == 0 {
		p.BackoffMax = d.BackoffMax
	}
	if (p.MaxRetries != nil && *p.MaxRetries < 0) || p.Delay < 0 || p.BackoffFactor < 0 || p.BackoffMax < 0 {
		return p, ErrInvalid
	}
	return p, nil
}

type RetryRequest struct {
	Cause     error
	Countdown *time.Duration
	ETA       time.Time
}

func (r *RetryRequest) Error() string { return "async: retry requested" }
func (r *RetryRequest) Unwrap() error { return r.Cause }
func Retry(cause error) error         { return &RetryRequest{Cause: cause} }
func RetryAfter(cause error, delay time.Duration) error {
	return &RetryRequest{Cause: cause, Countdown: &delay}
}
func RetryAt(cause error, eta time.Time) error { return &RetryRequest{Cause: cause, ETA: eta} }

// Next returns a UTC next-attempt deadline only for an explicit retry or an
// opted-in auto-retry match. jitter accepts [0,1], enabling deterministic tests.
func (p RetryPolicy) Next(err error, retries int, now, expires time.Time, jitter float64) (time.Time, bool) {
	var req *RetryRequest
	explicit := errors.As(err, &req)
	if retries < 0 || (!explicit && (p.AutoRetryFor == nil || !p.AutoRetryFor(err))) || (p.MaxRetries != nil && retries >= *p.MaxRetries) {
		return time.Time{}, false
	}
	delay := p.Delay
	if p.Backoff {
		power := math.Min(float64(retries), 62)
		n := float64(p.BackoffFactor) * math.Exp2(power)
		if n > float64(p.BackoffMax) {
			n = float64(p.BackoffMax)
		}
		if !p.DisableJitter {
			n *= math.Max(0, math.Min(1, jitter))
		}
		delay = time.Duration(n)
	}
	eta := now.Add(delay)
	if explicit {
		if req.Countdown != nil {
			if *req.Countdown < 0 {
				return time.Time{}, false
			}
			eta = now.Add(*req.Countdown)
		}
		if !req.ETA.IsZero() {
			eta = req.ETA
		}
	}
	if eta.Before(now) {
		eta = now
	}
	eta = eta.UTC()
	if !expires.IsZero() && !eta.Before(expires) {
		return time.Time{}, false
	}
	return eta, true
}
