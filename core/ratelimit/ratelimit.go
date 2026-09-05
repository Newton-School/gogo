// Package ratelimit defines atomic quota providers. Security gates fail closed.
package ratelimit

import (
	"context"
	"errors"
	"math"
	"time"
)

type Limit struct {
	Rate   float64
	Burst  int
	Period time.Duration
}
type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}
type Limiter interface {
	Allow(context.Context, string, Limit, int) (Decision, error)
}

var ErrInvalidLimit = errors.New("invalid rate limit")

func (l Limit) Validate(cost int) error {
	if l.Rate <= 0 || math.IsNaN(l.Rate) || math.IsInf(l.Rate, 0) || l.Burst < 1 || l.Period <= 0 || cost < 1 || cost > l.Burst {
		return ErrInvalidLimit
	}
	return nil
}
