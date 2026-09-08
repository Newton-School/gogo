package health

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"time"
)

// Checker is a small handle to private state. A value copy pins configuration,
// cache and in-flight ownership without copying a mutex or a resource provider.
type Checker struct{ state *checkerState }

type checkerState struct {
	role         string
	observe      func() State
	dependencies []dependency
	timeout      time.Duration
	cacheTTL     time.Duration
	disableCache bool
	slots        chan struct{}
	mu           sync.Mutex
	flight       *probeFlight
	cached       Report
	expires      time.Time
}

type dependency struct {
	ID                      string
	probe                   func(context.Context) error
	optional, allowDegraded bool
	timeout                 time.Duration
	running                 bool // protected by checkerState.mu
}

type probeFlight struct {
	done   chan struct{}
	report Report // published by closing done; never subsequently modified
}

func New(config Config) (*Checker, error) {
	if !validLabel(config.Role) || config.State == nil || len(config.Dependencies) > 64 || config.Concurrency < 0 || config.Concurrency > 16 || config.Timeout < 0 || config.Timeout > 30*time.Second || config.CacheTTL < 0 || config.CacheTTL > 5*time.Second {
		return nil, ErrConfiguration
	}
	if config.Concurrency == 0 {
		config.Concurrency = 4
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Second
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = 250 * time.Millisecond
	}
	if config.Timeout < time.Millisecond || config.CacheTTL < time.Millisecond {
		return nil, ErrConfiguration
	}
	s := &checkerState{role: config.Role, observe: config.State, timeout: config.Timeout, cacheTTL: config.CacheTTL, disableCache: config.DisableCache, slots: make(chan struct{}, config.Concurrency)}
	seen := map[string]bool{}
	for _, input := range config.Dependencies {
		if !validLabel(input.ID) || seen[input.ID] || input.Probe == nil || len(input.Roles) > 16 || input.AllowDegraded && !input.Optional || input.Timeout < 0 || input.Timeout > config.Timeout {
			return nil, ErrConfiguration
		}
		seen[input.ID] = true
		roles := map[string]bool{}
		selected := len(input.Roles) == 0
		for _, role := range input.Roles {
			if !validLabel(role) || roles[role] {
				return nil, ErrConfiguration
			}
			roles[role] = true
			selected = selected || role == config.Role
		}
		if input.Timeout == 0 {
			input.Timeout = min(500*time.Millisecond, config.Timeout)
		}
		if input.Timeout < time.Millisecond {
			return nil, ErrConfiguration
		}
		if selected {
			s.dependencies = append(s.dependencies, dependency{ID: input.ID, probe: input.Probe, optional: input.Optional, allowDegraded: input.AllowDegraded, timeout: input.Timeout})
		}
	}
	return &Checker{state: s}, nil
}

// Check returns complete safe reports. A dependency failure is a report status,
// not a raw error. Canceling one caller does not cancel a shared probe flight or
// poison its cache; the flight has its own short deadline and no request values.
func (c *Checker) Check(ctx context.Context, kind Kind) (report Report, err error) {
	if c == nil || c.state == nil {
		return Report{}, ErrConfiguration
	}
	s := c.state // Capture before Context methods or lifecycle callbacks.
	defer func() {
		if recover() != nil {
			report, err = Report{}, ErrUnavailable
		}
		if err != nil {
			report = Report{}
		}
	}()
	if !validKind(kind) {
		return Report{}, ErrInvalid
	}
	if err := contextError(ctx); err != nil {
		return Report{}, err
	}
	lifecycle, err := s.snapshot()
	if err != nil {
		return Report{}, err
	}
	if err := contextError(ctx); err != nil {
		return Report{}, err
	}
	report = s.localReport(kind, lifecycle)
	if kind != KindReady || report.Status != StatusReady {
		return s.recheck(ctx, kind, report)
	}
	s.mu.Lock()
	if !s.disableCache && !s.expires.IsZero() && time.Now().Before(s.expires) {
		report = cloneReport(s.cached)
		report.Cached = true
		s.mu.Unlock()
		return s.recheck(ctx, kind, report)
	}
	flight := s.flight
	if flight == nil {
		flight = &probeFlight{done: make(chan struct{})}
		s.flight = flight
		go s.runFlight(flight)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		err := contextError(ctx)
		if err == nil { // A broken Context must not report empty success.
			err = ErrUnavailable
		}
		return Report{}, err
	case <-flight.done:
		return s.recheck(ctx, kind, cloneReport(flight.report))
	}
}

// recheck only downgrades an existing report from fresh local lifecycle state.
// It never performs external probes, upgrades a failure or returns cache data.
func (c *Checker) recheck(ctx context.Context, kind Kind, report Report) (Report, error) {
	if c == nil || c.state == nil {
		return Report{}, ErrConfiguration
	}
	return c.state.recheck(ctx, kind, report)
}

func (s *checkerState) recheck(ctx context.Context, kind Kind, report Report) (Report, error) {
	if !validKind(kind) || report.Kind != kind || report.Role != s.role {
		return Report{}, ErrInvalid
	}
	if err := contextError(ctx); err != nil {
		return Report{}, err
	}
	lifecycle, err := s.snapshot()
	if err != nil {
		return Report{}, err
	}
	if err := contextError(ctx); err != nil {
		return Report{}, err
	}
	current := s.localReport(kind, lifecycle)
	if !current.Success() {
		report.Status = current.Status
	}
	return cloneReport(report), nil
}

func (s *checkerState) snapshot() (state State, err error) {
	defer func() {
		if recover() != nil {
			state, err = State{}, ErrUnavailable
		}
	}()
	return s.observe(), nil
}

func (s *checkerState) localReport(kind Kind, state State) Report {
	r := Report{Kind: kind, Role: s.role, CheckedAtUnixMilli: time.Now().UnixMilli()}
	switch kind {
	case KindLive:
		r.Status = StatusNotLive
		if state.Live {
			r.Status = StatusLive
		}
	case KindStartup:
		r.Status = StatusStarting
		if state.Started {
			r.Status = StatusStarted
		}
	case KindReady:
		r.Status = StatusNotReady
		if state.Live && state.Started && state.Ready && !state.Stopping {
			r.Status = StatusReady
		}
	}
	return r
}

func cloneReport(report Report) Report {
	report.Checks = slices.Clone(report.Checks)
	return report
}
func validKind(kind Kind) bool { return kind == KindLive || kind == KindStartup || kind == KindReady }
func validLabel(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, b := range []byte(value) {
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-') {
			return false
		}
	}
	return true
}
func contextError(ctx context.Context) (err error) {
	if ctx == nil {
		return ErrInvalid
	}
	v := reflect.ValueOf(ctx)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return ErrInvalid
		}
	}
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	return ctx.Err()
}
