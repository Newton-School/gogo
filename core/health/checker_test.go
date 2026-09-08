package health

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readyState() State    { return State{Live: true, Started: true, Ready: true} }
func healthConfig() Config { return Config{Role: "web", State: readyState} }
func mustChecker(t *testing.T, config Config) *Checker {
	t.Helper()
	checker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}
func requireReport(t *testing.T, checker *Checker, kind Kind) Report {
	t.Helper()
	report, err := checker.Check(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestHealthConfigurationBoundsAndDefaults(t *testing.T) {
	c := mustChecker(t, healthConfig())
	if c.state.timeout != 2*time.Second || c.state.cacheTTL != 250*time.Millisecond || cap(c.state.slots) != 4 {
		t.Fatal("wrong defaults")
	}
	for _, alter := range []func(*Config){
		func(c *Config) { c.Role = "" }, func(c *Config) { c.Role = "bad.role" }, func(c *Config) { c.Role = strings.Repeat("x", 65) },
		func(c *Config) { c.State = nil }, func(c *Config) { c.Concurrency = -1 }, func(c *Config) { c.Concurrency = 17 },
		func(c *Config) { c.Timeout = -time.Second }, func(c *Config) { c.Timeout = time.Nanosecond }, func(c *Config) { c.Timeout = 31 * time.Second },
		func(c *Config) { c.CacheTTL = -time.Second }, func(c *Config) { c.CacheTTL = time.Nanosecond }, func(c *Config) { c.CacheTTL = 6 * time.Second },
		func(c *Config) { c.Dependencies = make([]Dependency, 65) },
		func(c *Config) { c.Dependencies = []Dependency{{ID: "valid"}} },
		func(c *Config) {
			c.Dependencies = []Dependency{{ID: "invalid host", Probe: func(context.Context) error { return nil }}}
		},
		func(c *Config) {
			c.Dependencies = []Dependency{{ID: "db", AllowDegraded: true, Probe: func(context.Context) error { return nil }}}
		},
		func(c *Config) {
			c.Dependencies = []Dependency{{ID: "db", Roles: []string{"web", "web"}, Probe: func(context.Context) error { return nil }}}
		},
		func(c *Config) {
			c.Dependencies = []Dependency{{ID: "db", Roles: make([]string, 17), Probe: func(context.Context) error { return nil }}}
		},
		func(c *Config) {
			c.Dependencies = []Dependency{{ID: "db", Timeout: 3 * time.Second, Probe: func(context.Context) error { return nil }}}
		},
		func(c *Config) {
			c.Dependencies = []Dependency{{ID: "db", Probe: func(context.Context) error { return nil }}, {ID: "db", Probe: func(context.Context) error { return nil }}}
		},
	} {
		config := healthConfig()
		alter(&config)
		if checker, err := New(config); err != ErrConfiguration || checker != nil {
			t.Fatal("invalid configuration accepted", err)
		}
	}
}

func TestHealthLiveAndStartupNeverProbe(t *testing.T) {
	for _, state := range []State{{}, {Live: true}, {Live: true, Started: true, Stopping: true}, {Started: true, Ready: true}, readyState()} {
		config := healthConfig()
		config.State = func() State { return state }
		calls := 0
		config.Dependencies = []Dependency{{ID: "database", Probe: func(context.Context) error { calls++; return errors.New("offline") }}}
		checker := mustChecker(t, config)
		live := requireReport(t, checker, KindLive)
		startup := requireReport(t, checker, KindStartup)
		if live.Success() != state.Live || startup.Success() != state.Started || calls != 0 || len(live.Checks) != 0 || len(startup.Checks) != 0 || live.Cached || startup.Cached {
			t.Fatal("local health used a dependency or changed semantics", live, startup, calls)
		}
		if !state.Live || !state.Started || !state.Ready || state.Stopping {
			ready := requireReport(t, checker, KindReady)
			if ready.Status != StatusNotReady || calls != 0 || len(ready.Checks) != 0 {
				t.Fatal("unready lifecycle probed external dependencies", ready, calls)
			}
		}
	}
}

func TestHealthRoleSelectionAndConfigurationSnapshots(t *testing.T) {
	var calls atomic.Int32
	roles := []string{"web"}
	deps := []Dependency{
		{ID: "database", Roles: roles, Probe: func(context.Context) error { calls.Add(1); return nil }},
		{ID: "worker_queue", Roles: []string{"worker"}, Probe: func(context.Context) error { t.Error("web probed a worker dependency"); return nil }},
	}
	config := healthConfig()
	config.Dependencies = deps
	checker := mustChecker(t, config)
	roles[0] = "worker"
	deps[0] = Dependency{ID: "replaced", Probe: func(context.Context) error { panic("replaced") }}
	config.Role, config.State = "worker", func() State { return State{} }
	report := requireReport(t, checker, KindReady)
	if report.Status != StatusReady || report.Role != "web" || len(report.Checks) != 1 || report.Checks[0].ID != "database" || calls.Load() != 1 {
		t.Fatal("role or dependency selection changed", report, calls.Load())
	}
}

type secretHealthError struct{}

func (secretHealthError) Error() string { panic("provider error must not be inspected") }

func TestHealthOptionalFailureRequiresExplicitBypass(t *testing.T) {
	for _, optional := range []bool{false, true} {
		for _, bypass := range []bool{false, true} {
			if bypass && !optional {
				continue
			}
			config := healthConfig()
			config.Dependencies = []Dependency{{ID: "cache", Optional: optional, AllowDegraded: bypass, Probe: func(context.Context) error { return secretHealthError{} }}}
			checker := mustChecker(t, config)
			report := requireReport(t, checker, KindReady)
			want := StatusNotReady
			if bypass {
				want = StatusDegraded
			}
			if report.Status != want || len(report.Checks) != 1 || report.Checks[0].Status != CheckFailed || report.Checks[0].Optional != optional || report.Checks[0].Bypassed != bypass {
				t.Fatal("optional policy changed", report)
			}
			encoded, err := json.Marshal(report)
			if err != nil || !strings.Contains(string(encoded), `"optional":`) || !strings.Contains(string(encoded), `"bypassed":`) || strings.Contains(string(encoded), "provider") {
				t.Fatal("private report field or error boundary failed", string(encoded), err)
			}
		}
	}
}

func TestHealthCacheIsDetachedAndNeverMasksDrain(t *testing.T) {
	var calls atomic.Int32
	state := readyState()
	config := healthConfig()
	config.State = func() State { return state }
	config.CacheTTL = time.Second
	config.Dependencies = []Dependency{{ID: "database", Probe: func(context.Context) error { calls.Add(1); return nil }}}
	checker := mustChecker(t, config)
	first := requireReport(t, checker, KindReady)
	first.Checks[0].ID, first.Checks[0].Status = "changed", CheckFailed
	second := requireReport(t, checker, KindReady)
	if !second.Cached || second.Status != StatusReady || second.Checks[0].ID != "database" || second.Checks[0].Status != CheckPassed || calls.Load() != 1 {
		t.Fatal("cache borrowed a caller report", second, calls.Load())
	}
	state.Stopping = true
	draining := requireReport(t, checker, KindReady)
	if draining.Status != StatusNotReady || len(draining.Checks) != 0 || calls.Load() != 1 {
		t.Fatal("cache masked drain", draining, calls.Load())
	}
	if !requireReport(t, checker, KindLive).Success() || !requireReport(t, checker, KindStartup).Success() {
		t.Fatal("drain became process death or undid startup")
	}
	state = readyState()
	checker.state.mu.Lock()
	checker.state.expires = time.Now().Add(-time.Second)
	checker.state.mu.Unlock()
	if report := requireReport(t, checker, KindReady); report.Cached || calls.Load() != 2 {
		t.Fatal("expired cache reused", report, calls.Load())
	}
}

func TestHealthFinalLifecycleRecheckNeverUpgradesOrProbes(t *testing.T) {
	state := readyState()
	config := healthConfig()
	config.State = func() State { return state }
	checker := mustChecker(t, config)
	report := requireReport(t, checker, KindReady)
	state.Stopping = true
	downgraded, err := checker.recheck(context.Background(), KindReady, report)
	if err != nil || downgraded.Status != StatusNotReady {
		t.Fatal(downgraded, err)
	}
	state = readyState()
	if again, err := checker.recheck(context.Background(), KindReady, downgraded); err != nil || again.Status != StatusNotReady {
		t.Fatal("recheck upgraded a failed observation", again, err)
	}
	if got, err := checker.recheck(context.Background(), KindLive, report); err != ErrInvalid || !reflect.DeepEqual(got, Report{}) {
		t.Fatal("mismatched report accepted", got, err)
	}
}

type healthTestContext struct {
	context.Context
	before   func()
	panicErr bool
}

func (c *healthTestContext) Err() error {
	if c.panicErr {
		panic("private context")
	}
	if c.before != nil {
		before := c.before
		c.before = nil
		before()
	}
	return c.Context.Err()
}

func TestHealthInvalidContextsAndStateCancellationDoNotStartProbes(t *testing.T) {
	var calls atomic.Int32
	config := healthConfig()
	config.Dependencies = []Dependency{{ID: "database", Probe: func(context.Context) error { calls.Add(1); return nil }}}
	checker := mustChecker(t, config)
	var typedNil *healthTestContext
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, typedNil, canceled, &healthTestContext{Context: context.Background(), panicErr: true}} {
		if got, err := checker.Check(ctx, KindReady); err == nil || !reflect.DeepEqual(got, Report{}) {
			t.Fatal("invalid context exposed report", got, err)
		}
	}
	if got, err := checker.Check(context.Background(), Kind("invalid")); err != ErrInvalid || !reflect.DeepEqual(got, Report{}) {
		t.Fatal(got, err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	config.State = func() State { stop(); return readyState() }
	checker = mustChecker(t, config)
	if got, err := checker.Check(ctx, KindReady); err != context.Canceled || !reflect.DeepEqual(got, Report{}) {
		t.Fatal("state cancellation returned success", got, err)
	}
	checker.state.mu.Lock()
	flight := checker.state.flight
	checker.state.mu.Unlock()
	if calls.Load() != 0 || flight != nil {
		t.Fatal("invalid/canceled entry started a probe flight", calls.Load())
	}
	config.State = func() State { panic("private lifecycle") }
	checker = mustChecker(t, config)
	if got, err := checker.Check(context.Background(), KindLive); err != ErrUnavailable || !reflect.DeepEqual(got, Report{}) {
		t.Fatal("state panic escaped", got, err)
	}
}

func TestHealthOperationPinsHandleBeforeContextStateAndProbeCallbacks(t *testing.T) {
	for _, at := range []string{"context", "state", "probe"} {
		t.Run(at, func(t *testing.T) {
			other := healthConfig()
			other.Role = "replacement"
			replacement := mustChecker(t, other)
			var checker *Checker
			config := healthConfig()
			config.State = func() State {
				if at == "state" {
					*checker = *replacement
				}
				return readyState()
			}
			config.Dependencies = []Dependency{{ID: "database", Probe: func(context.Context) error {
				if at == "probe" {
					*checker = *replacement
				}
				return nil
			}}}
			checker = mustChecker(t, config)
			ctx := &healthTestContext{Context: context.Background()}
			if at == "context" {
				ctx.before = func() { *checker = *replacement }
			}
			report, err := checker.Check(ctx, KindReady)
			if err != nil || report.Role != "web" || len(report.Checks) != 1 || report.Checks[0].ID != "database" {
				t.Fatal("in-flight checker was replaced", report, err)
			}
			if report := requireReport(t, checker, KindReady); report.Role != "replacement" {
				t.Fatal("next call did not observe replacement", report)
			}
		})
	}
}
