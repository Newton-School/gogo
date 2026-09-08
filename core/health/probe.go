package health

import (
	"context"
	"sync"
	"time"
)

func (s *checkerState) runFlight(flight *probeFlight) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	checks := make([]CheckResult, len(s.dependencies))
	var group sync.WaitGroup
	for i := range s.dependencies {
		group.Go(func() { checks[i] = s.runDependency(ctx, i) })
	}
	group.Wait()
	report := Report{Kind: KindReady, Status: StatusReady, Role: s.role, CheckedAtUnixMilli: time.Now().UnixMilli(), Duration: time.Since(started), Checks: checks}
	for _, check := range checks {
		if check.Status == CheckPassed {
			continue
		}
		if !check.Bypassed {
			report.Status = StatusNotReady
		} else if report.Status == StatusReady {
			report.Status = StatusDegraded
		}
	}
	s.mu.Lock()
	flight.report = report
	if !s.disableCache {
		s.cached = report
		s.expires = time.Now().Add(s.cacheTTL)
	}
	s.flight = nil
	close(flight.done)
	s.mu.Unlock()
}

func (s *checkerState) runDependency(flightCtx context.Context, index int) (result CheckResult) {
	started := time.Now()
	dep := &s.dependencies[index]
	result = CheckResult{ID: dep.ID, Optional: dep.optional}
	defer func() {
		result.Duration = time.Since(started)
		result.Bypassed = result.Status != CheckPassed && dep.optional && dep.allowDegraded
	}()
	s.mu.Lock()
	if dep.running {
		s.mu.Unlock()
		result.Status = CheckBusy
		return result
	}
	dep.running = true
	s.mu.Unlock()
	reserved := false
	select {
	case s.slots <- struct{}{}:
		reserved = true
	case <-flightCtx.Done():
	}
	if !reserved || flightCtx.Err() != nil {
		if reserved {
			<-s.slots
		}
		s.mu.Lock()
		dep.running = false
		s.mu.Unlock()
		result.Status = CheckTimeout
		return result
	}
	ctx, cancel := context.WithTimeout(flightCtx, dep.timeout)
	defer cancel()
	done := make(chan CheckStatus, 1)
	go func() {
		status := CheckPassed
		completed := false
		defer func() {
			if recovered := recover(); recovered != nil || !completed {
				status = CheckFailed
			}
			// Only actual callback exit releases ownership. A timed-out caller
			// has already published its own value; this cannot amend that value,
			// the completed flight, or the cached diagnostic report.
			<-s.slots
			s.mu.Lock()
			dep.running = false
			s.mu.Unlock()
			done <- status
		}()
		if err := dep.probe(ctx); err != nil {
			status = CheckFailed
		}
		completed = true
	}()
	select {
	case result.Status = <-done:
		if ctx.Err() != nil {
			result.Status = CheckTimeout
		}
	case <-ctx.Done():
		result.Status = CheckTimeout
	}
	return result
}
