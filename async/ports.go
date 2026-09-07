package async

import (
	"context"
	"encoding/json"
	"time"
)

type Receipt struct {
	ID         string
	State      State
	AcceptedAt time.Time
}
type Delivery struct {
	Receipt       string
	Queue         string
	Body          []byte
	DeliveryCount int64
}
type QueueStats struct {
	Queue       string
	Queued      int64
	Pending     int64
	Quarantined int64
}
type ConsumeOptions struct {
	Queues   []string
	Consumer string
	Wait     time.Duration
	// ReclaimLimit caps Reclaim's reservation batch at 1..1000. Zero uses the
	// backend's default. Consume still reserves at most one delivery.
	ReclaimLimit int
}

type Broker interface {
	Publish(context.Context, Envelope) error
	Consume(context.Context, ConsumeOptions) (Delivery, error)
	Ack(context.Context, Delivery) error
	Reject(context.Context, Delivery, string, bool) error
	Reclaim(context.Context, ConsumeOptions, time.Duration) ([]Delivery, error)
	// Inspect returns exactly one nonnegative count record per requested queue;
	// ordering may differ. It is a trusted read port without caller grants.
	// Count observations are not a transactionally consistent execution state.
	Inspect(context.Context, []string) ([]QueueStats, error)
	Close() error
}

type Record struct {
	Envelope         Envelope        `json:"envelope"`
	Digest           string          `json:"digest"`
	State            State           `json:"state"`
	Fence            uint64          `json:"fence"`
	Owner            string          `json:"owner"`
	LeaseUntil       time.Time       `json:"lease_until"`
	Revision         uint64          `json:"revision"`
	DeliveryCount    int64           `json:"delivery_count"`
	Output           json.RawMessage `json:"output,omitempty"`
	Failure          *Failure        `json:"failure,omitempty"`
	Progress         json.RawMessage `json:"progress,omitempty"`
	CancelRequested  bool            `json:"cancel_requested"`
	FinishedAt       time.Time       `json:"finished_at,omitempty"`
	PayloadExpiresAt time.Time       `json:"payload_expires_at,omitempty"`
	TombstoneUntil   time.Time       `json:"tombstone_until,omitempty"`
	ReplayUntil      time.Time       `json:"replay_until,omitempty"`
	Pinned           bool            `json:"pinned"`
	// ReplacementID owns logical completion while execution has yielded. The
	// state remains RUNNING, but no worker lease or slot is retained.
	ReplacementID string `json:"replacement_id,omitempty"`
}

type Claim struct {
	Record    Record
	Acquired  bool
	Duplicate bool
}
type Transition struct {
	ID            string
	Fence         uint64
	Owner         string
	State         State
	Output        json.RawMessage
	Failure       *Failure
	Next          *Envelope
	Intents       []Intent
	ReplacementID string
}

// ReplacementStore completes a yielded task with the current record revision.
// finalize is a pure transition builder and may run again after a CAS conflict.
// A terminal matching replacement is an idempotent no-op.
type ReplacementStore interface {
	ResolveReplacement(context.Context, string, string, func(Record) (Transition, error)) (bool, error)
}

type ResultStore interface {
	// Lookup returns the exact requested task identity and valid stored metadata.
	// This trusted provider port does not grant caller access; Result authorizes
	// and detaches the observation before returning it to application callers.
	Lookup(context.Context, string) (Record, error)
	Register(context.Context, Envelope, State) error
	Claim(context.Context, Envelope, string, time.Duration) (Claim, error)
	Renew(context.Context, string, uint64, string, time.Duration) (Record, error)
	Transition(context.Context, Transition) error
	RecordProgress(context.Context, string, uint64, string, json.RawMessage) error
	RequestCancel(context.Context, string, string) error
	Forget(context.Context, string) error
	ReleasePin(context.Context, string) error
	IntentStore
}

type Intent struct {
	ID         string      `json:"id"`
	SourceID   string      `json:"source_id"`
	Kind       string      `json:"kind"`
	Envelope   *Envelope   `json:"envelope,omitempty"`
	WorkflowID string      `json:"workflow_id,omitempty"`
	TargetID   string      `json:"target_id,omitempty"`
	Completion *Completion `json:"completion,omitempty"`
	Failure    *Failure    `json:"failure,omitempty"`
	State      State       `json:"state,omitempty"`
	Graph      *Graph      `json:"graph,omitempty"`
	Fence      uint64      `json:"fence"`
	Owner      string      `json:"owner"`
	LeaseUntil time.Time   `json:"lease_until"`
	Delivered  bool        `json:"delivered"`
}

type IntentStore interface {
	ListIntents(context.Context, int) ([]Intent, error)
	ClaimIntent(context.Context, string, string, string, time.Duration) (Intent, error)
	MarkIntentDelivered(context.Context, string, string, uint64, string) error
}

type Completion struct {
	ID      string          `json:"id"`
	State   State           `json:"state"`
	Output  json.RawMessage `json:"output,omitempty"`
	Failure *Failure        `json:"failure,omitempty"`
}

type Graph struct {
	ID               string                `json:"id"`
	Kind             string                `json:"kind"`
	Scope            string                `json:"scope"`
	Children         []Envelope            `json:"children"`
	Signatures       []Signature           `json:"signatures"`
	Callback         *Signature            `json:"callback,omitempty"`
	CallbackID       string                `json:"callback_id,omitempty"`
	Members          map[string]Completion `json:"members"`
	State            State                 `json:"state"`
	Output           json.RawMessage       `json:"output,omitempty"`
	Failure          *Failure              `json:"failure,omitempty"`
	Next             int                   `json:"next"`
	Revision         uint64                `json:"revision"`
	CallbackClaimed  bool                  `json:"callback_claimed"`
	CallbackEnvelope *Envelope             `json:"callback_envelope,omitempty"`
	CancelRequested  bool                  `json:"cancel_requested"`
	Plan             []CanvasNode          `json:"plan,omitempty"`
	RootNode         string                `json:"root_node,omitempty"`
	OriginTaskID     string                `json:"origin_task_id,omitempty"`
	OriginDigest     string                `json:"origin_digest,omitempty"`
	AbortFailure     *Failure              `json:"abort_failure,omitempty"`
}

// CanvasNode is a portable compiled workflow node. Collect nodes execute no
// application code; they retain ordered dependency outputs in the graph CAS.
type CanvasNode struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Dependencies []string   `json:"dependencies,omitempty"`
	WaitFor      []string   `json:"wait_for,omitempty"`
	Signature    *Signature `json:"signature,omitempty"`
	Dispatched   bool       `json:"dispatched"`
}

type WorkflowStore interface {
	CreateGraph(context.Context, Graph, []Intent) error
	ReadGraph(context.Context, string) (Graph, error)
	// RecordMember validates expected child identity and commits completion and
	// successor intents together. A duplicate must not increment the barrier.
	RecordMember(context.Context, string, Completion, func(Graph) (Graph, []Intent, error)) error
	CancelGraph(context.Context, string, string, func(Graph) (Graph, []Intent, error)) error
	IntentStore
}

type DelayedItem struct {
	Envelope   Envelope  `json:"envelope"`
	Owner      string    `json:"owner"`
	Fence      uint64    `json:"fence"`
	LeaseUntil time.Time `json:"lease_until"`
}
type ScheduleStore interface {
	Schedule(context.Context, Envelope) error
	LeaseDue(context.Context, string, int, time.Duration) ([]DelayedItem, error)
	CommitFire(context.Context, DelayedItem) error
}
