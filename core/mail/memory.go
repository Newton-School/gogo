package mail

import (
	"context"
	"fmt"
	"sync"
)

// Memory is a bounded, concurrent test outbox. It never contacts a provider.
// Outbox intentionally exposes plaintext including Sensitive messages to tests;
// do not use this backend as durable production storage.
type Memory struct {
	*memoryState
}
type memoryState struct {
	mu                           sync.Mutex
	limits                       Limits
	maximum, maximumBytes, bytes int
	outbox                       []Prepared
}

func NewMemory(maximum, maximumBytes int, limits Limits) (*Memory, error) {
	if maximum < 1 || maximumBytes < 1 {
		return nil, ErrLimit
	}
	l, err := limits.defaults()
	if err != nil {
		return nil, err
	}
	return &Memory{memoryState: &memoryState{limits: l, maximum: maximum, maximumBytes: maximumBytes}}, nil
}

func (Memory) String() string                      { return "mail.Memory{outbox:redacted}" }
func (m Memory) GoString() string                  { return m.String() }
func (m Memory) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, m.String()) }
func (Memory) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }

func (m *Memory) Send(ctx context.Context, message Message) (Receipt, error) {
	p, err := Prepare(ctx, message, m.limits)
	if err != nil {
		return Receipt{}, err
	}
	r := receiptFor("memory", p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if len(m.outbox) >= m.maximum || len(p.data) > m.maximumBytes-m.bytes {
		return r, ErrLimit
	}
	m.outbox = append(m.outbox, p)
	m.bytes += len(p.data)
	r.Simulated = true
	for i := range r.Recipients {
		r.Recipients[i].State = Simulated
	}
	return r, nil
}

func (m *Memory) Outbox() []Prepared {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Prepared(nil), m.outbox...)
}

func (m *Memory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outbox = nil
	m.bytes = 0
}
