package testing

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/Newton-School/gogo/async"
)

func (m *Memory) ListDashboard(ctx context.Context, kind, cursor string, limit int) (async.DashboardIDs, error) {
	if ctx == nil || (async.DashboardIDs{}).Validate(kind, limit) != nil {
		return async.DashboardIDs{}, async.ErrInvalid
	}
	if err := m.check(ctx, "dashboard_inventory"); err != nil {
		return async.DashboardIDs{}, err
	}
	after := ""
	if cursor != "" {
		prefix := "memory-dashboard/" + kind + "/"
		if !strings.HasPrefix(cursor, prefix) {
			return async.DashboardIDs{}, async.ErrInvalid
		}
		after = strings.TrimPrefix(cursor, prefix)
		if (async.DashboardIDs{IDs: []string{after}}).Validate(kind, 1) != nil {
			return async.DashboardIDs{}, async.ErrInvalid
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	now := m.Clock()
	add := func(id string) {
		if id > after {
			ids = append(ids, id)
		}
	}
	switch kind {
	case "tasks":
		for id := range m.records {
			add(id)
		}
	case "workflows":
		for id := range m.graphs {
			add(id)
		}
	case "beat":
		for id := range m.periodic {
			add(id)
		}
	case "workers":
		for id, p := range m.workers {
			if p.retainedUntil.After(now) {
				add(id)
			}
		}
	case "schedulers":
		for id, p := range m.beats {
			if p.ObservedAt.Add(24 * time.Hour).After(now) {
				add(id)
			}
		}
	}
	sort.Strings(ids)
	page := async.DashboardIDs{}
	if len(ids) > limit {
		ids = ids[:limit]
		page.NextCursor = "memory-dashboard/" + kind + "/" + ids[len(ids)-1]
	}
	page.IDs = ids
	return page, nil
}

func (m *Memory) ReadDashboardSchedule(ctx context.Context, id string) (async.PeriodicSchedule, error) {
	if err := m.check(ctx, "dashboard_schedule"); err != nil {
		return async.PeriodicSchedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.periodic[id]
	if !ok {
		return async.PeriodicSchedule{}, async.ErrNotFound
	}
	return copyOf(p), nil
}

func (m *Memory) ObserveBeat(ctx context.Context, id, status string, ttl time.Duration) error {
	if ctx == nil || !async.ValidWorkerID(id) || ttl < time.Millisecond || ttl > time.Hour || status != "online" && status != "offline" && status != "error" {
		return async.ErrInvalid
	}
	if err := m.check(ctx, "observe_beat"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.beats == nil {
		m.beats = map[string]async.BeatObservation{}
	}
	now := m.Clock().UTC()
	for id, p := range m.beats {
		if !p.ObservedAt.Add(24 * time.Hour).After(now) {
			delete(m.beats, id)
		}
	}
	m.beats[id] = async.BeatObservation{ID: id, Status: status, ObservedAt: now, ExpiresAt: now.Add(ttl)}
	return nil
}

func (m *Memory) ReadDashboardBeat(ctx context.Context, id string) (async.BeatObservation, error) {
	if err := m.check(ctx, "dashboard_beat"); err != nil {
		return async.BeatObservation{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.beats[id]
	now := m.Clock()
	if !ok || !p.ObservedAt.Add(24*time.Hour).After(now) {
		return async.BeatObservation{}, async.ErrNotFound
	}
	if p.Status != "offline" && !p.ExpiresAt.After(now) {
		p.Status = "lost"
	}
	return p, nil
}

var _ async.DashboardCatalog = (*Memory)(nil)
var _ async.BeatMonitor = (*Memory)(nil)
