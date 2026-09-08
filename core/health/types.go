// Package health separates local process health from bounded role-specific
// dependency readiness. Probe results never retain provider error details.
package health

import (
	"context"
	"errors"
	"time"
)

type Kind string

const (
	KindLive    Kind = "live"
	KindStartup Kind = "startup"
	KindReady   Kind = "ready"
)

type Status string

const (
	StatusLive     Status = "live"
	StatusNotLive  Status = "not_live"
	StatusStarted  Status = "started"
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusNotReady Status = "not_ready"
	StatusDegraded Status = "degraded"
)

type CheckStatus string

const (
	CheckPassed  CheckStatus = "passed"
	CheckFailed  CheckStatus = "failed"
	CheckTimeout CheckStatus = "timeout"
	CheckBusy    CheckStatus = "busy"
)

var (
	ErrConfiguration = errors.New("health: invalid configuration")
	ErrInvalid       = errors.New("health: invalid request")
	ErrUnavailable   = errors.New("health: unavailable")
	ErrForbidden     = errors.New("health: access denied")
)

// State is a process observation, not a dependency result. Live is independent
// of startup and drain; Started may remain latched after successful startup.
// Readiness requires Live, Started and Ready, with Stopping false.
type State struct{ Live, Started, Ready, Stopping bool }

type Dependency struct {
	ID string
	// Empty selects every role. Otherwise only explicitly listed roles run it.
	Roles []string
	// Pass a bound method such as backend.Ping or redisConnection.Ping. The
	// checker neither opens nor closes these application-owned resources.
	Probe func(context.Context) error
	// Optional alone does not tolerate failure. Both flags are required for
	// an explicitly bypassable dependency to produce degraded readiness.
	Optional, AllowDegraded bool
	// Zero selects 500ms, capped by the whole-flight timeout. This deadline
	// starts after concurrency admission; queued work has the flight deadline.
	Timeout time.Duration
}

type Config struct {
	Role string
	// State is a trusted, nonblocking, read-only process snapshot. It must not
	// perform I/O or wait for another health check. It runs without health locks
	// and is not executed as a dependency goroutine.
	State        func() State
	Dependencies []Dependency
	// Zero selects four callbacks; the hard maximum is sixteen. Timed-out
	// callbacks retain capacity until they actually exit.
	Concurrency int
	// Zero selects two seconds; accepted nonzero range is 1ms through 30s.
	Timeout time.Duration
	// Zero selects 250ms; nonzero values range from 1ms through 5s. Both healthy
	// and failed dependency reports are cached; lifecycle state is always fresh.
	CacheTTL     time.Duration
	DisableCache bool
}

// Report is a private diagnostic value, not the public HTTP representation.
// Codes, durations and configured IDs contain no provider errors or endpoints.
type Report struct {
	Kind               Kind          `json:"kind"`
	Status             Status        `json:"status"`
	Role               string        `json:"role"`
	CheckedAtUnixMilli int64         `json:"checked_at_unix_ms"`
	Duration           time.Duration `json:"duration_ns"`
	Cached             bool          `json:"cached"`
	Checks             []CheckResult `json:"checks,omitempty"`
}

func (r Report) Success() bool {
	switch r.Status {
	case StatusLive, StatusStarted, StatusReady, StatusDegraded:
		return true
	default:
		return false
	}
}

type CheckResult struct {
	ID       string        `json:"id"`
	Status   CheckStatus   `json:"status"`
	Optional bool          `json:"optional"`
	Bypassed bool          `json:"bypassed"`
	Duration time.Duration `json:"duration_ns"`
}
