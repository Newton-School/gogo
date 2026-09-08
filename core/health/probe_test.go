package health

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitHealthSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("health synchronization deadline exceeded")
	}
}
func waitHealthRelease(t *testing.T, c *Checker) {
	t.Helper()
	limit := time.Now().Add(2 * time.Second)
	for time.Now().Before(limit) {
		c.state.mu.Lock()
		running := false
		for i := range c.state.dependencies {
			running = running || c.state.dependencies[i].running
		}
		c.state.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("callback did not release retained ownership")
}

func TestHealthSingleFlightSurvivesCanceledWaiter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	releaseProbe := sync.OnceFunc(func() { close(release) })
	defer releaseProbe()
	var calls atomic.Int32
	config := healthConfig()
	config.CacheTTL = time.Second
	config.Dependencies = []Dependency{{ID: "database", Probe: func(ctx context.Context) error {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}}
	checker := mustChecker(t, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		report, err := checker.Check(ctx, KindReady)
		if err != nil && !reflect.DeepEqual(report, Report{}) {
			t.Error("canceled waiter got partial report")
		}
		first <- err
	}()
	waitHealthSignal(t, entered)
	cancel()
	select {
	case err := <-first:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not cancel promptly")
	}
	const waiters = 16
	results := make(chan Report, waiters)
	var group sync.WaitGroup
	for range waiters {
		group.Go(func() {
			report, err := checker.Check(context.Background(), KindReady)
			if err != nil {
				t.Error(err)
			}
			results <- report
		})
	}
	releaseProbe()
	group.Wait()
	close(results)
	for report := range results {
		if report.Status != StatusReady || len(report.Checks) != 1 {
			t.Fatal("shared flight was poisoned", report)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("probe storm after caller cancellation", calls.Load())
	}
	if report := requireReport(t, checker, KindReady); !report.Cached || report.Status != StatusReady {
		t.Fatal("shared success was not cached", report)
	}
}

func TestHealthProbeTimeoutRetainsOwnershipAndIgnoresLateSuccess(t *testing.T) {
	release := make(chan struct{})
	releaseProbe := sync.OnceFunc(func() { close(release) })
	defer releaseProbe()
	var calls atomic.Int32
	config := healthConfig()
	config.Concurrency = 1
	config.Timeout = 100 * time.Millisecond
	config.CacheTTL = time.Second
	config.Dependencies = []Dependency{{ID: "database", Timeout: 10 * time.Millisecond, Probe: func(context.Context) error { calls.Add(1); <-release; return nil }}}
	checker := mustChecker(t, config)
	first := requireReport(t, checker, KindReady)
	if first.Status != StatusNotReady || first.Checks[0].Status != CheckTimeout || calls.Load() != 1 || len(checker.state.slots) != 1 {
		t.Fatal("timeout lost callback ownership", first, calls.Load())
	}
	// Force expiry under the test's private lock without a wall-clock sleep.
	for range 8 {
		checker.state.mu.Lock()
		checker.state.expires = time.Now().Add(-time.Second)
		checker.state.mu.Unlock()
		report := requireReport(t, checker, KindReady)
		if report.Status != StatusNotReady || report.Checks[0].Status != CheckBusy || calls.Load() != 1 {
			t.Fatal("timed-out dependency spawned replacements", report, calls.Load())
		}
	}
	releaseProbe()
	waitHealthRelease(t, checker)
	// The late successful return cannot rewrite the published cached busy report.
	if report := requireReport(t, checker, KindReady); !report.Cached || report.Checks[0].Status != CheckBusy || calls.Load() != 1 {
		t.Fatal("late success rewrote cache", report, calls.Load())
	}
	if first.Checks[0].Status != CheckTimeout {
		t.Fatal("late completion mutated an earlier caller report")
	}
	checker.state.mu.Lock()
	checker.state.expires = time.Now().Add(-time.Second)
	checker.state.mu.Unlock()
	if report := requireReport(t, checker, KindReady); report.Status != StatusReady || calls.Load() != 2 {
		t.Fatal("released dependency did not recover", report, calls.Load())
	}
}

func TestHealthConcurrentProbesRespectGlobalBoundAndResultOrder(t *testing.T) {
	const count = 8
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	releaseProbes := sync.OnceFunc(func() { close(release) })
	defer releaseProbes()
	var active, peak atomic.Int32
	config := healthConfig()
	config.Concurrency = 2
	config.Timeout = time.Second
	for i := range count {
		config.Dependencies = append(config.Dependencies, Dependency{ID: fmt.Sprintf("dep_%d", i), Probe: func(ctx context.Context) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			entered <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	}
	checker := mustChecker(t, config)
	done := make(chan Report, 1)
	go func() {
		report, err := checker.Check(context.Background(), KindReady)
		if err != nil {
			t.Error(err)
		}
		done <- report
	}()
	waitHealthSignal(t, entered)
	waitHealthSignal(t, entered)
	if active.Load() != 2 || peak.Load() != 2 {
		t.Fatal("callbacks did not run concurrently within bound", active.Load(), peak.Load())
	}
	releaseProbes()
	select {
	case report := <-done:
		if report.Status != StatusReady || len(report.Checks) != count || peak.Load() > 2 {
			t.Fatal("concurrency result changed", report, peak.Load())
		}
		for i, check := range report.Checks {
			if check.ID != fmt.Sprintf("dep_%d", i) || check.Status != CheckPassed {
				t.Fatal("nondeterministic or missing result", report)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe flight did not finish")
	}
}

func TestHealthFailurePanicAndGoexitReleaseProbeCapacity(t *testing.T) {
	config := healthConfig()
	config.Concurrency = 1
	config.DisableCache = true
	var mode atomic.Int32
	config.Dependencies = []Dependency{{ID: "database", Probe: func(context.Context) error {
		switch mode.Load() {
		case 0:
			return errors.New("synthetic-private-provider-text")
		case 1:
			panic("synthetic-private-provider-panic")
		case 2:
			runtime.Goexit()
		}
		return nil
	}}}
	checker := mustChecker(t, config)
	for i := int32(0); i < 4; i++ {
		mode.Store(i)
		report := requireReport(t, checker, KindReady)
		want := CheckFailed
		if i == 3 {
			want = CheckPassed
		}
		if report.Checks[0].Status != want || len(checker.state.slots) != 0 {
			t.Fatal("callback failure retained capacity or reported success", report, len(checker.state.slots))
		}
	}
}

func TestHealthOptionalTimeoutPreservesCompletedRequiredSuccess(t *testing.T) {
	config := healthConfig()
	config.Timeout = 60 * time.Millisecond
	config.Dependencies = []Dependency{
		{ID: "database", Probe: func(context.Context) error { return nil }},
		{ID: "cache", Optional: true, AllowDegraded: true, Probe: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
	}
	checker := mustChecker(t, config)
	report := requireReport(t, checker, KindReady)
	if report.Status != StatusDegraded || report.Checks[0].Status != CheckPassed || report.Checks[1].Status != CheckTimeout || !report.Checks[1].Bypassed {
		t.Fatal("optional timeout lost completed success or bypass", report)
	}
	waitHealthRelease(t, checker)
}

func TestHealthDrainDuringProbeDowngradesWithoutChangingProbeOutcome(t *testing.T) {
	var stopping atomic.Bool
	config := healthConfig()
	config.State = func() State { state := readyState(); state.Stopping = stopping.Load(); return state }
	config.Dependencies = []Dependency{{ID: "database", Probe: func(context.Context) error { stopping.Store(true); return nil }}}
	checker := mustChecker(t, config)
	report := requireReport(t, checker, KindReady)
	if report.Status != StatusNotReady || len(report.Checks) != 1 || report.Checks[0].Status != CheckPassed {
		t.Fatal("drain was masked or altered the actual probe result", report)
	}
}

func TestHealthStuckProbeRetainsGlobalCapacityAcrossFlights(t *testing.T) {
	release := make(chan struct{})
	releaseProbe := sync.OnceFunc(func() { close(release) })
	defer releaseProbe()
	var stuckCalls, otherCalls atomic.Int32
	config := healthConfig()
	config.Concurrency, config.Timeout, config.DisableCache = 1, 50*time.Millisecond, true
	config.Dependencies = []Dependency{
		{ID: "stuck", Timeout: 10 * time.Millisecond, Probe: func(context.Context) error { stuckCalls.Add(1); <-release; return nil }},
		{ID: "other", Probe: func(context.Context) error { otherCalls.Add(1); return nil }},
	}
	checker := mustChecker(t, config)
	first := requireReport(t, checker, KindReady)
	if first.Checks[0].Status != CheckTimeout || stuckCalls.Load() != 1 || len(checker.state.slots) != 1 {
		t.Fatal("stuck callback did not own the sole actual slot", first, stuckCalls.Load())
	}
	before := otherCalls.Load() // Either ordering in the initial flight is valid.
	second := requireReport(t, checker, KindReady)
	if second.Checks[0].Status != CheckBusy || second.Checks[1].Status != CheckTimeout || otherCalls.Load() != before || stuckCalls.Load() != 1 {
		t.Fatal("another dependency bypassed capacity held by a timed-out callback", second, stuckCalls.Load(), otherCalls.Load())
	}
	releaseProbe()
	waitHealthRelease(t, checker)
	if recovered := requireReport(t, checker, KindReady); recovered.Status != StatusReady || stuckCalls.Load() != 2 || otherCalls.Load() != before+1 {
		t.Fatal("released global capacity did not recover", recovered, stuckCalls.Load(), otherCalls.Load())
	}
}
