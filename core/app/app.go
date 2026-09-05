// Package app owns explicit app registration and application resource lifetime.
package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
)

type Config struct {
	Name, Label string
	Requires    []string
	Register    func(*Registry) error
	Ready       func(context.Context, *Registry) error
	Shutdown    func(context.Context) error
}
type Registry struct {
	mu     sync.RWMutex
	values map[string]any
	frozen bool
}

func (r *Registry) Register(kind, name string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return errors.New("app registry frozen")
	}
	if kind == "" || name == "" || value == nil {
		return errors.New("invalid registration")
	}
	key := kind + ":" + name
	if r.values == nil {
		r.values = map[string]any{}
	}
	if _, ok := r.values[key]; ok {
		return fmt.Errorf("duplicate registration: %s", key)
	}
	r.values[key] = value
	return nil
}
func (r *Registry) Get(kind, name string) (any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.values[kind+":"+name]
	return v, ok
}
func (r *Registry) Freeze() { r.mu.Lock(); defer r.mu.Unlock(); r.frozen = true }
func (r *Registry) Names(kind string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for k := range r.values {
		prefix := kind + ":"
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			names = append(names, k[len(prefix):])
		}
	}
	slices.Sort(names)
	return names
}

var labelPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TopologicalOrder(configs []Config) ([]Config, error) {
	lookup := map[string]Config{}
	for _, c := range configs {
		if c.Name == "" || !labelPattern.MatchString(c.Label) {
			return nil, errors.New("app name and valid label required")
		}
		if _, ok := lookup[c.Label]; ok {
			return nil, fmt.Errorf("duplicate app label: %s", c.Label)
		}
		c.Requires = slices.Clone(c.Requires)
		lookup[c.Label] = c
	}
	state := map[string]uint8{}
	var order []Config
	var visit func(string) error
	visit = func(label string) error {
		if state[label] == 2 {
			return nil
		}
		if state[label] == 1 {
			return fmt.Errorf("app dependency cycle at %s", label)
		}
		c, ok := lookup[label]
		if !ok {
			return fmt.Errorf("missing app dependency: %s", label)
		}
		state[label] = 1
		for _, dep := range c.Requires {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[label] = 2
		order = append(order, c)
		return nil
	}
	for _, c := range configs {
		if err := visit(c.Label); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Resource has a unique owner. Open must return its closer only on success.
type Resource struct {
	Name string
	Open func(context.Context) (func(context.Context) error, error)
}
type Application struct {
	Registry  *Registry
	ordered   []Config
	configs   []Config
	closers   []func(context.Context) error
	closeOnce sync.Once
	closeErr  error
	ready     atomic.Bool
	startOnce sync.Once
	startErr  error
}

func Bootstrap(ctx context.Context, configs []Config, resources []Resource, freeze func(*Registry) error) (*Application, error) {
	a, err := Prepare(configs, freeze)
	if err != nil {
		return nil, err
	}
	if err = a.Start(ctx, resources); err != nil {
		return nil, err
	}
	return a, nil
}

// Prepare registers and freezes descriptors without I/O or Ready hooks. Command
// resolution and argument validation can therefore happen before any resource opens.
func Prepare(configs []Config, freeze func(*Registry) error) (*Application, error) {
	ordered, err := TopologicalOrder(configs)
	if err != nil {
		return nil, err
	}
	a := &Application{Registry: &Registry{}, ordered: ordered}
	for _, c := range ordered {
		if c.Register != nil {
			if err := c.Register(a.Registry); err != nil {
				return nil, fmt.Errorf("register app %s: %w", c.Label, err)
			}
		}
	}
	if freeze != nil {
		if err := freeze(a.Registry); err != nil {
			return nil, err
		}
	}
	a.Registry.Freeze()
	return a, nil
}

func (a *Application) Start(ctx context.Context, resources []Resource) error {
	a.startOnce.Do(func() { a.startErr = a.start(ctx, resources) })
	return a.startErr
}
func (a *Application) start(ctx context.Context, resources []Resource) error {
	var err error
	seen := map[string]bool{}
	for _, r := range resources {
		if r.Name == "" || r.Open == nil || seen[r.Name] {
			err = errors.New("invalid or duplicate resource")
			break
		}
		seen[r.Name] = true
		var close func(context.Context) error
		close, err = r.Open(ctx)
		if err != nil {
			break
		}
		if close != nil {
			a.closers = append(a.closers, close)
		}
	}
	if err != nil {
		cleanup := a.Close(context.WithoutCancel(ctx))
		return errors.Join(err, cleanup)
	}
	for _, c := range a.ordered {
		a.configs = append(a.configs, c)
		if c.Ready != nil {
			if err := c.Ready(ctx, a.Registry); err != nil {
				return errors.Join(fmt.Errorf("ready app %s: %w", c.Label, err), a.Close(context.WithoutCancel(ctx)))
			}
		}
	}
	a.ready.Store(true)
	return nil
}
func (a *Application) Ready() bool    { return a.ready.Load() }
func (a *Application) StopAdmission() { a.ready.Store(false) }
func (a *Application) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.ready.Store(false)
		var errs []error
		for i := len(a.configs) - 1; i >= 0; i-- {
			if f := a.configs[i].Shutdown; f != nil {
				errs = append(errs, f(ctx))
			}
		}
		for i := len(a.closers) - 1; i >= 0; i-- {
			errs = append(errs, a.closers[i](ctx))
		}
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}
