package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/async"
)

// Periodic operations use the explicit test clock, not wall time or sleeping.
// Clock and Fail are trusted cooperative hooks, captured once per operation
// and called outside mu. Do not concurrently replace those public fields.
var _ async.PeriodicStore = (*Memory)(nil)

const periodicBatchBytes = 8 << 20

func periodicContext(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = async.ErrUnavailable
		}
	}()
	if ctx == nil {
		return async.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return err
		}
		return async.ErrUnavailable
	}
	return nil
}

func periodicID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func periodicOwner(owner string) bool {
	return len(owner) > 0 && len(owner) <= 256 && utf8.ValidString(owner) && !strings.ContainsAny(owner, "\x00\r\n")
}

func periodicTime(value time.Time) time.Time {
	// A distinct Location is required even for zero: callers may retain and
	// replace a Location value without changing this stored observation.
	zone := *time.UTC
	return value.UTC().In(&zone)
}

// Only the concrete framework JSON structs below reach this walker. It calls
// no application methods. Preflight bounds both repeated work and raw text
// before whole-document encoding; time.Time is the only opaque scalar.
type periodicBudget struct{ text, nodes int }

func (b *periodicBudget) visit(value reflect.Value, depth int, detach bool) bool {
	if depth > 32 || b.nodes <= 0 {
		return false
	}
	b.nodes--
	if value.Type() == reflect.TypeFor[time.Time]() {
		t := value.Interface().(time.Time)
		if t.UTC().Year() < 0 || t.UTC().Year() > 9999 {
			return false
		}
		if detach {
			value.Set(reflect.ValueOf(periodicTime(t)))
		}
		return true
	}
	switch value.Kind() {
	case reflect.String:
		if value.Len() > b.text || !utf8.ValidString(value.String()) {
			return false
		}
		b.text -= value.Len()
	case reflect.Pointer:
		return value.IsNil() || b.visit(value.Elem(), depth+1, detach)
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			if value.Len() > b.text {
				return false
			}
			b.text -= value.Len()
			return true
		}
		if value.Len() > b.nodes {
			return false
		}
		for i := 0; i < value.Len(); i++ {
			if !b.visit(value.Index(i), depth+1, detach) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if !value.Type().Field(i).IsExported() || !b.visit(value.Field(i), depth+1, detach) {
				return false
			}
		}
	case reflect.Map:
		if value.Len() > b.nodes/2 {
			return false
		}
		iter := value.MapRange()
		for iter.Next() {
			if !b.visit(iter.Key(), depth+1, false) || !b.visit(iter.Value(), depth+1, false) {
				return false
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint64:
	default:
		return false
	}
	return true
}

func periodicCopy[T any](value T, limit int) (T, error) {
	var out T
	nodes := 16384
	if limit > async.MaxPayloadBytes {
		nodes = 1 << 20 // A 1000-occurrence catch-up batch contains many envelopes.
	}
	budget := periodicBudget{limit, nodes}
	if !budget.visit(reflect.ValueOf(value), 0, false) {
		return out, async.ErrInvalid
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &out) != nil {
		return out, async.ErrInvalid
	}
	budget = periodicBudget{limit, nodes}
	if !budget.visit(reflect.ValueOf(&out).Elem(), 0, true) {
		return *new(T), async.ErrInvalid
	}
	return out, nil
}

type periodicHooks struct {
	clock func() time.Time
	fail  func(string) error
}

func (m *Memory) periodicHooks() (periodicHooks, error) {
	if m == nil || m.Clock == nil {
		return periodicHooks{}, async.ErrInvalid
	}
	return periodicHooks{m.Clock, m.Fail}, nil
}

func (h periodicHooks) before(ctx context.Context, op string, observe bool) (now time.Time, err error) {
	defer func() {
		if recover() != nil {
			now, err = time.Time{}, async.ErrUnavailable
		}
		if canceled := periodicContext(ctx); canceled != nil {
			now, err = time.Time{}, errors.Join(err, canceled)
		}
	}()
	if err = periodicContext(ctx); err != nil {
		return
	}
	if h.fail != nil {
		if err = h.fail(op); err != nil {
			return
		}
	}
	if observe {
		now = periodicTime(h.clock()).Truncate(time.Millisecond)
		// Redis server time and its decoded lease metadata are positive epoch
		// milliseconds. Historical/pre-epoch simulator clocks are not modeled.
		if now.UnixMilli() <= 0 || now.Year() > 9999 {
			return time.Time{}, async.ErrInvalid
		}
	}
	return
}

func (m *Memory) UpsertSchedule(ctx context.Context, schedule async.PeriodicSchedule, expected uint64) error {
	hooks, err := m.periodicHooks()
	if err != nil {
		return err
	}
	p, err := periodicCopy(schedule, async.MaxPayloadBytes)
	if err != nil || expected == math.MaxUint64 || p.Revision != expected+1 || p.Validate() != nil {
		return async.ErrInvalid
	}
	p.Owner, p.LeaseUntil, p.ObservedAt = "", periodicTime(time.Time{}), periodicTime(time.Time{})
	if _, err = hooks.before(ctx, "upsert_schedule", false); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.periodic[p.ID].Revision != expected {
		return async.ErrConflict
	}
	if m.periodic == nil {
		m.periodic = make(map[string]async.PeriodicSchedule)
	}
	m.periodic[p.ID] = p
	return nil // No callback can rewrite a confirmed mutation's outcome.
}

func (m *Memory) LeaseSchedules(ctx context.Context, owner string, limit int, lease time.Duration) ([]async.PeriodicSchedule, error) {
	hooks, err := m.periodicHooks()
	if err != nil || !periodicOwner(owner) || limit < 1 || limit > 1000 || lease < time.Millisecond {
		return nil, async.ErrInvalid
	}
	now, err := hooks.before(ctx, "lease_schedules", true)
	if err != nil {
		return nil, err
	}
	until := periodicTime(now.Add(lease.Truncate(time.Millisecond)))
	if until.Year() > 9999 || !until.After(now) {
		return nil, async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.periodic))
	for id, p := range m.periodic {
		if p.Enabled && p.NextDue.UnixMilli() <= now.UnixMilli() && !p.LeaseUntil.After(now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := m.periodic[ids[i]].NextDue.UnixMilli(), m.periodic[ids[j]].NextDue.UnixMilli()
		return a < b || a == b && ids[i] < ids[j]
	})
	ids = ids[:min(len(ids), limit)]
	out := make([]async.PeriodicSchedule, 0, len(ids))
	for _, id := range ids {
		p := m.periodic[id]
		if p.Revision == math.MaxUint64 || p.Fence == math.MaxUint64 {
			return nil, async.ErrInvalid
		}
		p.Revision++
		p.Fence++
		p.Owner, p.LeaseUntil, p.ObservedAt = owner, until, now
		copy, err := periodicCopy(p, async.MaxPayloadBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, copy)
	}
	// Preflight the complete selected batch before leasing its first member.
	for i, id := range ids {
		p := m.periodic[id]
		p.Revision, p.Fence = out[i].Revision, out[i].Fence
		p.Owner, p.LeaseUntil, p.ObservedAt = owner, periodicTime(until), periodicTime(now)
		m.periodic[id] = p
	}
	return out, nil
}

func (m *Memory) CommitOccurrence(ctx context.Context, schedule async.PeriodicSchedule, next time.Time, lastID string, intents []async.Intent) error {
	hooks, err := m.periodicHooks()
	if err != nil {
		return err
	}
	p, err := periodicCopy(schedule, async.MaxPayloadBytes)
	if err != nil || p.Validate() != nil || !periodicOwner(p.Owner) || p.Fence == 0 || p.Revision == math.MaxUint64 || next.IsZero() || !next.After(p.NextDue) || lastID != "" && !periodicID(lastID) || len(intents) > 1000 {
		return async.ErrInvalid
	}
	next = periodicTime(next)
	batch, err := periodicCopy(intents, periodicBatchBytes)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(batch))
	for _, intent := range batch {
		if intent.SourceID != p.ID || !periodicID(intent.ID) || seen[intent.ID] || intent.Envelope != nil && intent.Envelope.Validate() != nil {
			return async.ErrInvalid
		}
		seen[intent.ID] = true
	}
	updated := p
	updated.NextDue, updated.LastTaskID = next, lastID
	updated.Revision++
	updated.Owner, updated.LeaseUntil, updated.ObservedAt = "", periodicTime(time.Time{}), periodicTime(time.Time{})
	if !updated.EndAt.IsZero() && !next.Before(updated.EndAt) {
		updated.Enabled = false
	}
	updated, err = periodicCopy(updated, async.MaxPayloadBytes)
	if err != nil {
		return err
	}
	now, err := hooks.before(ctx, "commit_occurrence", true)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.periodic[p.ID]
	if !exists || current.Revision != p.Revision || current.Fence != p.Fence || current.Owner != p.Owner || !current.LeaseUntil.After(now) {
		return async.ErrLeaseLost
	}
	for _, intent := range batch {
		if old, exists := m.intents[intent.ID]; exists && !periodicIntentEqual(old, intent) {
			return async.ErrConflict
		}
	}
	if m.intents == nil {
		m.intents = make(map[string]async.Intent)
	}
	m.periodic[p.ID] = updated
	for _, intent := range batch {
		if _, exists := m.intents[intent.ID]; !exists {
			m.intents[intent.ID] = intent
		}
	}
	return nil
}

func periodicIntentEqual(a, b async.Intent) bool {
	a, err := periodicCopy(a, periodicBatchBytes)
	if err != nil {
		return false
	}
	a.Owner, b.Owner = "", ""
	a.Fence, b.Fence = 0, 0
	a.LeaseUntil, b.LeaseUntil = time.Time{}, time.Time{}
	a.Delivered, b.Delivered = false, false
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

func (m *Memory) Disable(ctx context.Context, id string, expected uint64) error {
	hooks, err := m.periodicHooks()
	if err != nil || !periodicID(id) || expected == math.MaxUint64 {
		return async.ErrInvalid
	}
	if _, err = hooks.before(ctx, "disable_schedule", false); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, exists := m.periodic[id]
	if !exists {
		return async.ErrNotFound
	}
	if p.Revision != expected {
		return async.ErrConflict
	}
	p.Revision++
	p.Enabled = false
	p.Owner, p.LeaseUntil, p.ObservedAt = "", periodicTime(time.Time{}), periodicTime(time.Time{})
	p, err = periodicCopy(p, async.MaxPayloadBytes)
	if err != nil {
		return err
	}
	m.periodic[id] = p
	return nil
}

func (m *Memory) List(ctx context.Context, limit int) ([]async.PeriodicSchedule, error) {
	hooks, err := m.periodicHooks()
	if err != nil || limit < 1 || limit > 1000 {
		return nil, async.ErrInvalid
	}
	if _, err = hooks.before(ctx, "list_schedules", false); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.periodic))
	for id := range m.periodic {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]async.PeriodicSchedule, 0, min(limit, len(ids)))
	for _, id := range ids[:min(limit, len(ids))] {
		p, err := periodicCopy(m.periodic[id], async.MaxPayloadBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
