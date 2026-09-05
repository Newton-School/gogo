// Package testing provides explicit process-local Async backends for tests.
// They are never selected automatically by production configuration.
package testing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Newton-School/gogo/async"
)

type reserved struct {
	delivery async.Delivery
	at       time.Time
}
type Memory struct {
	mu                  sync.Mutex
	Clock               func() time.Time
	Fail                func(string) error
	records             map[string]async.Record
	intents             map[string]async.Intent
	graphs              map[string]async.Graph
	workflowIDs         []string
	delayed             map[string]async.DelayedItem
	workers             map[string]workerPresence
	controls            map[string]map[string]workerControl
	queue               []async.Delivery
	pending             map[string]reserved
	quarantine          []async.QuarantineRecord
	quarantineNamespace string
	sequence            int64
}

func NewMemory() *Memory {
	return &Memory{Clock: time.Now, records: map[string]async.Record{}, intents: map[string]async.Intent{}, graphs: map[string]async.Graph{}, delayed: map[string]async.DelayedItem{}, pending: map[string]reserved{}}
}
func copyOf[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func (m *Memory) check(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Fail != nil {
		return m.Fail(op)
	}
	return nil
}
func (m *Memory) Close() error { return nil }

func (m *Memory) Publish(ctx context.Context, e async.Envelope) error {
	if err := m.check(ctx, "publish"); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(e)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sequence++
	m.queue = append(m.queue, async.Delivery{Receipt: fmt.Sprint(m.sequence), Queue: e.Queue, Body: b})
	return nil
}
func (m *Memory) Consume(ctx context.Context, o async.ConsumeOptions) (async.Delivery, error) {
	if err := m.check(ctx, "consume"); err != nil {
		return async.Delivery{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, d := range m.queue {
		for _, queue := range o.Queues {
			if d.Queue == queue {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				d.DeliveryCount++
				m.pending[d.Receipt] = reserved{delivery: d, at: m.Clock()}
				return copyOf(d), nil
			}
		}
	}
	return async.Delivery{}, async.ErrNotFound
}
func (m *Memory) Ack(ctx context.Context, d async.Delivery) error {
	if err := m.check(ctx, "ack"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, d.Receipt)
	return nil
}
func (m *Memory) Reject(ctx context.Context, d async.Delivery, reason string, requeue bool) error {
	if err := m.check(ctx, "reject"); err != nil {
		return err
	}
	if !async.ValidQuarantineReason(reason) {
		return async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	reserved, exists := m.pending[d.Receipt]
	if !exists {
		return nil
	}
	if reserved.delivery.Queue != d.Queue {
		return async.ErrInvalid
	}
	d = reserved.delivery
	if requeue {
		m.queue = append(m.queue, copyOf(d))
	} else {
		if m.quarantineNamespace == "" {
			id, err := async.NewID()
			if err != nil {
				return err
			}
			m.quarantineNamespace = id
		}
		digest := sha256.Sum256(d.Body)
		safe := async.QuarantineRecord{ID: async.StableID("quarantine", d.Receipt), Queue: d.Queue, SourceReceipt: d.Receipt, Reason: reason, Digest: hex.EncodeToString(digest[:]), FirstSeen: m.Clock().UTC(), Priority: -1}
		if err := async.ValidateQuarantineRecord(safe); err != nil {
			return err
		}
		m.quarantine = append(m.quarantine, safe)
	}
	delete(m.pending, d.Receipt)
	return nil
}
func (m *Memory) Reclaim(ctx context.Context, o async.ConsumeOptions, idle time.Duration) ([]async.Delivery, error) {
	if err := m.check(ctx, "reclaim"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []async.Delivery
	for id, r := range m.pending {
		if m.Clock().Sub(r.at) >= idle {
			for _, queue := range o.Queues {
				if queue == r.delivery.Queue {
					r.at = m.Clock()
					r.delivery.DeliveryCount++
					m.pending[id] = r
					out = append(out, copyOf(r.delivery))
					break
				}
			}
		}
	}
	return out, nil
}
func (m *Memory) Inspect(ctx context.Context, queues []string) ([]async.QueueStats, error) {
	if err := m.check(ctx, "inspect"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]async.QueueStats, 0, len(queues))
	for _, q := range queues {
		s := async.QueueStats{Queue: q}
		for _, d := range m.queue {
			if d.Queue == q {
				s.Queued++
			}
		}
		for _, d := range m.pending {
			if d.delivery.Queue == q {
				s.Pending++
			}
		}
		for _, d := range m.quarantine {
			if d.Queue == q {
				s.Quarantined++
			}
		}
		out = append(out, s)
	}
	return out, nil
}
func (m *Memory) Register(ctx context.Context, e async.Envelope, state async.State) error {
	if err := m.check(ctx, "register"); err != nil {
		return err
	}
	r, err := async.InitialRecord(e, state)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.records[e.ID]; ok {
		if existing.Digest != r.Digest {
			return async.ErrConflict
		}
		return nil
	}
	m.records[e.ID] = r
	return nil
}
func (m *Memory) Lookup(ctx context.Context, id string) (async.Record, error) {
	if err := m.check(ctx, "lookup"); err != nil {
		return async.Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return r, async.ErrNotFound
	}
	if r.State.Terminal() && !r.Pinned && !r.PayloadExpiresAt.After(m.Clock()) {
		r.Output = nil
	}
	return copyOf(r), nil
}
func (m *Memory) Claim(ctx context.Context, e async.Envelope, owner string, lease time.Duration) (async.Claim, error) {
	if err := m.check(ctx, "claim"); err != nil {
		return async.Claim{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[e.ID]
	if !ok {
		var err error
		r, err = async.InitialRecord(e, async.Queued)
		if err != nil {
			return async.Claim{}, err
		}
	}
	c, err := async.ClaimRecord(r, e, owner, m.Clock(), lease)
	if err == nil && c.Acquired {
		m.records[e.ID] = copyOf(c.Record)
	}
	return c, err
}
func (m *Memory) Renew(ctx context.Context, id string, fence uint64, owner string, lease time.Duration) (async.Record, error) {
	if err := m.check(ctx, "renew"); err != nil {
		return async.Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return r, async.ErrNotFound
	}
	if err := async.ValidateLease(r, fence, owner, m.Clock()); err != nil {
		return r, err
	}
	r.LeaseUntil = m.Clock().Add(lease)
	r.Revision++
	m.records[id] = r
	return copyOf(r), nil
}
func (m *Memory) Transition(ctx context.Context, t async.Transition) error {
	if err := m.check(ctx, "transition"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[t.ID]
	if !ok {
		return async.ErrNotFound
	}
	next, err := async.ApplyTransition(r, t, m.Clock(), 24*time.Hour, 7*24*time.Hour)
	if err != nil {
		return err
	}
	m.records[t.ID] = next
	for _, intent := range t.Intents {
		if _, ok := m.intents[intent.ID]; !ok {
			m.intents[intent.ID] = copyOf(intent)
		}
	}
	return nil
}

func (m *Memory) ResolveReplacement(ctx context.Context, id, workflowID string, finalize func(async.Record) (async.Transition, error)) (bool, error) {
	if err := m.check(ctx, "resolve_replacement"); err != nil {
		return false, err
	}
	if finalize == nil {
		return false, async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, exists := m.records[id]
	if !exists {
		return false, async.ErrNotFound
	}
	if record.ReplacementID != workflowID || workflowID == "" {
		return false, async.ErrConflict
	}
	if record.State.Terminal() {
		return false, nil
	}
	transition, err := finalize(copyOf(record))
	if err != nil {
		return false, err
	}
	next, err := async.ApplyReplacementOutcome(record, workflowID, transition, m.Clock(), 24*time.Hour, 7*24*time.Hour)
	if err != nil {
		return false, err
	}
	m.records[id] = next
	for _, intent := range transition.Intents {
		if _, exists := m.intents[intent.ID]; !exists {
			m.intents[intent.ID] = copyOf(intent)
		}
	}
	return true, nil
}
func (m *Memory) RecordProgress(ctx context.Context, id string, fence uint64, owner string, b json.RawMessage) error {
	if err := m.check(ctx, "progress"); err != nil {
		return err
	}
	if len(b) > 16<<10 || !json.Valid(b) {
		return async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[id]
	if err := async.ValidateLease(r, fence, owner, m.Clock()); err != nil {
		return err
	}
	r.Progress = append(json.RawMessage(nil), b...)
	r.Revision++
	m.records[id] = r
	return nil
}
func (m *Memory) RequestCancel(ctx context.Context, id, scope string) error {
	if err := m.check(ctx, "cancel"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return async.ErrNotFound
	}
	if r.Envelope.Scope != scope {
		return async.ErrDenied
	}
	if r.State.Terminal() {
		return nil
	}
	r.CancelRequested = true
	r.Revision++
	m.records[id] = r
	for _, intent := range async.ReplacementCancellationIntents(r) {
		if _, exists := m.intents[intent.ID]; !exists {
			m.intents[intent.ID] = copyOf(intent)
		}
	}
	return nil
}
func (m *Memory) Forget(ctx context.Context, id string) error {
	if err := m.check(ctx, "forget"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return async.ErrNotFound
	}
	if !r.State.Terminal() || r.Pinned {
		return async.ErrPinned
	}
	r.Output = nil
	r.Progress = nil
	r.Revision++
	m.records[id] = r
	return nil
}
func (m *Memory) ReleasePin(ctx context.Context, id string) error {
	if err := m.check(ctx, "release_pin"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return async.ErrNotFound
	}
	if !r.State.Terminal() {
		return async.ErrPinned
	}
	r.Pinned = false
	r.Revision++
	m.records[id] = r
	return nil
}
func (m *Memory) ListIntents(ctx context.Context, limit int) ([]async.Intent, error) {
	if err := m.check(ctx, "intents"); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, i := range m.intents {
		if !i.Delivered {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var out []async.Intent
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		out = append(out, copyOf(m.intents[id]))
	}
	return out, nil
}
func (m *Memory) ClaimIntent(ctx context.Context, source, id, owner string, lease time.Duration) (async.Intent, error) {
	if err := m.check(ctx, "claim_intent"); err != nil {
		return async.Intent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.intents[id]
	if !ok || i.SourceID != source {
		return i, async.ErrNotFound
	}
	if i.Delivered {
		return i, nil
	}
	if i.LeaseUntil.After(m.Clock()) {
		return i, async.ErrBusy
	}
	i.Owner = owner
	i.Fence++
	i.LeaseUntil = m.Clock().Add(lease)
	m.intents[id] = i
	return copyOf(i), nil
}
func (m *Memory) MarkIntentDelivered(ctx context.Context, source, id string, fence uint64, owner string) error {
	if err := m.check(ctx, "mark_intent"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.intents[id]
	if !ok || i.SourceID != source {
		return async.ErrNotFound
	}
	if i.Delivered {
		return nil
	}
	if i.Owner != owner || i.Fence != fence || !i.LeaseUntil.After(m.Clock()) {
		return async.ErrLeaseLost
	}
	i.Delivered = true
	m.intents[id] = i
	return nil
}
func (m *Memory) CreateGraph(ctx context.Context, g async.Graph, intents []async.Intent) error {
	if err := m.check(ctx, "create_graph"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.graphs[g.ID]; ok {
		return async.ErrConflict
	}
	m.graphs[g.ID] = copyOf(g)
	position := sort.SearchStrings(m.workflowIDs, g.ID)
	m.workflowIDs = append(m.workflowIDs, "")
	copy(m.workflowIDs[position+1:], m.workflowIDs[position:])
	m.workflowIDs[position] = g.ID
	for _, i := range intents {
		m.intents[i.ID] = copyOf(i)
	}
	return nil
}
func (m *Memory) ReadGraph(ctx context.Context, id string) (async.Graph, error) {
	if err := m.check(ctx, "graph"); err != nil {
		return async.Graph{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.graphs[id]
	if !ok {
		return g, async.ErrNotFound
	}
	return copyOf(g), nil
}
func (m *Memory) RecordMember(ctx context.Context, id string, c async.Completion, advance func(async.Graph) (async.Graph, []async.Intent, error)) error {
	if err := m.check(ctx, "member"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.graphs[id]
	if !ok {
		return async.ErrNotFound
	}
	if _, exists := g.Members[c.ID]; exists {
		return nil
	}
	expected := c.ID == g.CallbackID
	for _, child := range g.Children {
		expected = expected || child.ID == c.ID
	}
	if !expected || !c.State.Terminal() {
		return async.ErrInvalid
	}
	g = copyOf(g)
	g.Members[c.ID] = c
	next, intents, err := advance(g)
	if err != nil {
		return err
	}
	next.Revision++
	m.graphs[id] = copyOf(next)
	for _, intent := range intents {
		if _, exists := m.intents[intent.ID]; !exists {
			m.intents[intent.ID] = copyOf(intent)
		}
	}
	return nil
}
func (m *Memory) CancelGraph(ctx context.Context, id, scope string, advance func(async.Graph) (async.Graph, []async.Intent, error)) error {
	if err := m.check(ctx, "cancel_graph"); err != nil {
		return err
	}
	if advance == nil {
		return async.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	graph, ok := m.graphs[id]
	if !ok {
		return async.ErrNotFound
	}
	if graph.Scope != scope {
		return async.ErrDenied
	}
	if graph.State.Terminal() {
		return nil
	}
	graph = copyOf(graph)
	graph.CancelRequested = true
	graph, intents, err := advance(graph)
	if err != nil {
		return err
	}
	graph.Revision++
	m.graphs[id] = copyOf(graph)
	for _, intent := range intents {
		if _, ok := m.intents[intent.ID]; !ok {
			m.intents[intent.ID] = copyOf(intent)
		}
	}
	return nil
}
func (m *Memory) Schedule(ctx context.Context, e async.Envelope) error {
	if err := m.check(ctx, "schedule"); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s:%d", e.ID, e.Retries)
	if i, ok := m.delayed[key]; ok {
		if i.Envelope.DispatchDigest() != e.DispatchDigest() {
			return async.ErrConflict
		}
		return nil
	}
	m.delayed[key] = async.DelayedItem{Envelope: copyOf(e)}
	return nil
}
func (m *Memory) LeaseDue(ctx context.Context, owner string, limit int, lease time.Duration) ([]async.DelayedItem, error) {
	if err := m.check(ctx, "lease_due"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []async.DelayedItem
	for key, item := range m.delayed {
		if len(out) >= limit {
			break
		}
		if item.Envelope.ETA.After(m.Clock()) || item.LeaseUntil.After(m.Clock()) {
			continue
		}
		item.Owner = owner
		item.Fence++
		item.LeaseUntil = m.Clock().Add(lease)
		m.delayed[key] = item
		out = append(out, copyOf(item))
	}
	return out, nil
}
func (m *Memory) CommitFire(ctx context.Context, item async.DelayedItem) error {
	if err := m.check(ctx, "commit_fire"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s:%d", item.Envelope.ID, item.Envelope.Retries)
	current, ok := m.delayed[key]
	if !ok {
		return nil
	}
	if current.Owner != item.Owner || current.Fence != item.Fence || !current.LeaseUntil.After(m.Clock()) {
		return async.ErrLeaseLost
	}
	delete(m.delayed, key)
	return nil
}
