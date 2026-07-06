package sql

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
)

type Options struct {
	Dialect string
	Now     func() time.Time
}

type Backend struct {
	db      *stdsql.DB
	dialect string
	now     func() time.Time
}

func NewBackend(db *stdsql.DB, options Options) *Backend {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Backend{db: db, dialect: options.Dialect, now: now}
}

func Migrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS queue_task_results (
			task_id TEXT PRIMARY KEY,
			state TEXT NOT NULL,
			result_json TEXT,
			error TEXT,
			traceback TEXT,
			children_json TEXT,
			expires_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS queue_group_results (
			group_id TEXT PRIMARY KEY,
			children_json TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS queue_chord_counters (
			chord_id TEXT PRIMARY KEY,
			counter INTEGER NOT NULL
		)`,
	}
}

func (b *Backend) ApplyMigrations(ctx context.Context) error {
	for _, statement := range Migrations() {
		if _, err := b.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) StoreResult(ctx context.Context, result queue.Result) error {
	now := b.now().UTC()
	if result.CreatedAt.IsZero() {
		result.CreatedAt = now
	}
	result.UpdatedAt = now
	resultJSON, err := json.Marshal(result.Result)
	if err != nil {
		return err
	}
	childrenJSON, err := json.Marshal(result.Children)
	if err != nil {
		return err
	}
	expires := ""
	if result.ExpiresAt != nil {
		expires = result.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = b.db.ExecContext(ctx, `INSERT INTO queue_task_results(task_id, state, result_json, error, traceback, children_json, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id) DO UPDATE SET state=excluded.state, result_json=excluded.result_json, error=excluded.error, traceback=excluded.traceback, children_json=excluded.children_json, expires_at=excluded.expires_at, updated_at=excluded.updated_at`,
		result.TaskID, result.State.String(), string(resultJSON), result.Error, result.Traceback, string(childrenJSON), expires, result.CreatedAt.UTC().Format(time.RFC3339Nano), result.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (b *Backend) GetResult(ctx context.Context, taskID string) (queue.Result, error) {
	row := b.db.QueryRowContext(ctx, `SELECT task_id, state, result_json, error, traceback, children_json, expires_at, created_at, updated_at FROM queue_task_results WHERE task_id = ?`, taskID)
	var result queue.Result
	var state string
	var resultJSON string
	var childrenJSON string
	var expires string
	var created string
	var updated string
	if err := row.Scan(&result.TaskID, &state, &resultJSON, &result.Error, &result.Traceback, &childrenJSON, &expires, &created, &updated); err != nil {
		if err == stdsql.ErrNoRows {
			return queue.Result{}, fmt.Errorf("%w: %s", backends.ErrResultNotFound, taskID)
		}
		return queue.Result{}, err
	}
	result.State = queue.State(state)
	if resultJSON != "" && resultJSON != "null" {
		_ = json.Unmarshal([]byte(resultJSON), &result.Result)
	}
	_ = json.Unmarshal([]byte(childrenJSON), &result.Children)
	if expires != "" {
		parsed, err := time.Parse(time.RFC3339Nano, expires)
		if err == nil {
			result.ExpiresAt = &parsed
			if b.now().After(parsed) {
				_ = b.Forget(ctx, taskID)
				return queue.Result{}, fmt.Errorf("%w: %s", backends.ErrResultExpired, taskID)
			}
		}
	}
	result.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	result.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return result, nil
}

func (b *Backend) Forget(ctx context.Context, taskID string) error {
	_, err := b.db.ExecContext(ctx, `DELETE FROM queue_task_results WHERE task_id = ?`, taskID)
	return err
}

func (b *Backend) Wait(ctx context.Context, taskID string, timeout time.Duration) (queue.Result, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := b.GetResult(ctx, taskID)
		if err == nil && result.State.Terminal() {
			return result, nil
		}
		if err != nil && err != stdsql.ErrNoRows {
			return queue.Result{}, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			return queue.Result{}, fmt.Errorf("%w: wait timeout for %s", backends.ErrResultNotFound, taskID)
		}
		select {
		case <-ctx.Done():
			return queue.Result{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (b *Backend) Children(ctx context.Context, taskID string) ([]string, error) {
	result, err := b.GetResult(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), result.Children...), nil
}

func (b *Backend) GroupResult(ctx context.Context, groupID string, children []string) (queue.GroupResult, error) {
	encoded, err := json.Marshal(children)
	if err != nil {
		return queue.GroupResult{}, err
	}
	created := b.now().UTC()
	_, err = b.db.ExecContext(ctx, `INSERT INTO queue_group_results(group_id, children_json, created_at)
VALUES (?, ?, ?)
ON CONFLICT(group_id) DO UPDATE SET children_json=excluded.children_json`,
		groupID, string(encoded), created.Format(time.RFC3339Nano))
	if err != nil {
		return queue.GroupResult{}, err
	}
	return queue.GroupResult{ID: groupID, Children: append([]string(nil), children...), CreatedAt: created}, nil
}

func (b *Backend) ChordCounter(ctx context.Context, chordID string, delta int) (int, error) {
	if _, err := b.db.ExecContext(ctx, `INSERT INTO queue_chord_counters(chord_id, counter) VALUES (?, 0) ON CONFLICT(chord_id) DO NOTHING`, chordID); err != nil {
		return 0, err
	}
	if _, err := b.db.ExecContext(ctx, `UPDATE queue_chord_counters SET counter = counter + ? WHERE chord_id = ?`, delta, chordID); err != nil {
		return 0, err
	}
	row := b.db.QueryRowContext(ctx, `SELECT counter FROM queue_chord_counters WHERE chord_id = ?`, chordID)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
