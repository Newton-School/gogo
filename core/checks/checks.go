// Package checks runs deterministic, read-only system and deployment checks.
package checks

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
)

type Severity int

const (
	Debug Severity = iota
	Info
	Warning
	Error
	Critical
)

type Finding struct {
	ID                    string
	Severity              Severity
	Path, Message, Remedy string
	Security              bool
}
type Options struct {
	Deploy, Probe  bool
	Tags, Suppress []string
}
type Check struct {
	ID                     string
	Tags                   []string
	DeployOnly, NeedsProbe bool
	Run                    func(context.Context) []Finding
}
type Registry struct {
	mu     sync.RWMutex
	checks []Check
	frozen bool
}

func (r *Registry) Register(c Check) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return errors.New("checks registry frozen")
	}
	if c.ID == "" || c.Run == nil {
		return errors.New("invalid check")
	}
	for _, existing := range r.checks {
		if existing.ID == c.ID {
			return errors.New("duplicate check")
		}
	}
	c.Tags = slices.Clone(c.Tags)
	r.checks = append(r.checks, c)
	return nil
}
func (r *Registry) Freeze() { r.mu.Lock(); defer r.mu.Unlock(); r.frozen = true }
func (r *Registry) Run(ctx context.Context, opts Options) ([]Finding, error) {
	r.mu.RLock()
	all := slices.Clone(r.checks)
	r.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	var findings []Finding
	failed := false
	for _, c := range all {
		if err := ctx.Err(); err != nil {
			return findings, err
		}
		if c.DeployOnly && !opts.Deploy || c.NeedsProbe && !opts.Probe {
			continue
		}
		if len(opts.Tags) > 0 && !slices.ContainsFunc(c.Tags, func(tag string) bool { return slices.Contains(opts.Tags, tag) }) {
			continue
		}
		for _, f := range c.Run(ctx) {
			if slices.Contains(opts.Suppress, f.ID) && !(f.Security && f.Severity >= Error) {
				continue
			}
			findings = append(findings, f)
			if f.Severity >= Error {
				failed = true
			}
		}
	}
	if failed {
		return findings, errors.New("system checks failed")
	}
	return findings, nil
}
