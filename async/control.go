package async

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

type Event struct {
	Kind     string    `json:"kind"`
	TaskID   string    `json:"task_id,omitempty"`
	Task     string    `json:"task,omitempty"`
	WorkerID string    `json:"worker_id,omitempty"`
	Scope    string    `json:"scope,omitempty"`
	State    State     `json:"state,omitempty"`
	Retries  int       `json:"retries,omitempty"`
	At       time.Time `json:"at"`
}
type EventSink interface {
	PublishEvent(context.Context, Event) error
}

type TaskActivity struct {
	ID        string
	Task      string
	Scope     string
	StartedAt time.Time
	Retries   int
}
type WorkerSnapshot struct {
	ID          string
	Queues      []string
	Registered  []string
	Concurrency int
	Active      []TaskActivity
	At          time.Time
}

func (w *Worker) Snapshot() WorkerSnapshot {
	w.activityMu.Lock()
	defer w.activityMu.Unlock()
	out := WorkerSnapshot{ID: w.ID, Queues: append([]string(nil), w.Queues...), Concurrency: w.Concurrency, At: time.Now().UTC()}
	if w.Registry != nil {
		out.Registered = w.Registry.Names()
	}
	for _, activity := range w.active {
		out.Active = append(out.Active, activity)
	}
	sort.Slice(out.Active, func(i, j int) bool { return out.Active[i].ID < out.Active[j].ID })
	return out
}
func (w *Worker) activity(e Envelope) {
	w.activityMu.Lock()
	defer w.activityMu.Unlock()
	if w.active == nil {
		w.active = map[string]TaskActivity{}
	}
	w.active[e.ID] = TaskActivity{ID: e.ID, Task: e.Task, Scope: e.Scope, StartedAt: w.Clock().UTC(), Retries: e.Retries}
}
func (w *Worker) inactive(id string) {
	w.activityMu.Lock()
	defer w.activityMu.Unlock()
	delete(w.active, id)
}
func (w *Worker) event(ctx context.Context, e Envelope, kind string, state State) {
	if w.Events != nil {
		w.observe(func() error {
			return w.Events.PublishEvent(ctx, Event{Kind: kind, TaskID: e.ID, Task: e.Task, WorkerID: w.ID, Scope: e.Scope, State: state, Retries: e.Retries, At: w.Clock().UTC()})
		})
	}
}

// Control checks distinct inspection and mutation grants. It never evaluates
// executable code from a control request and never treats cancellation requested
// as proof that an already-running handler has stopped.
type Control struct{ Client *Client }

func (c Control) Revoke(ctx context.Context, id string) error {
	if c.Client == nil {
		return ErrInvalid
	}
	return RestoreResult[json.RawMessage](c.Client, id).Revoke(ctx)
}
func (c Control) InspectQueues(ctx context.Context, queues []string) ([]QueueStats, error) {
	if c.Client == nil {
		return nil, ErrInvalid
	}
	for _, queue := range queues {
		if err := c.Client.authorize(ctx, "inspect", queue, ""); err != nil {
			return nil, err
		}
	}
	return c.Client.config.Broker.Inspect(ctx, queues)
}
func (c Control) InspectWorker(ctx context.Context, worker *Worker) (WorkerSnapshot, error) {
	if c.Client == nil || worker == nil {
		return WorkerSnapshot{}, ErrInvalid
	}
	snapshot := worker.Snapshot()
	if err := c.Client.authorize(ctx, "inspect", "", snapshot.ID); err != nil {
		return WorkerSnapshot{}, err
	}
	visible := snapshot.Active[:0]
	for _, activity := range snapshot.Active {
		if c.Client.authorize(ctx, "inspect", activity.Scope, activity.ID) == nil {
			visible = append(visible, activity)
		}
	}
	snapshot.Active = visible
	return snapshot, nil
}
