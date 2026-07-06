package sql

import (
	"context"
	stdsql "database/sql"
	"testing"
	"time"

	"github.com/Newton-School/gogo/queue"

	_ "modernc.org/sqlite"
)

func TestSQLBackendMigrationsResultsGroupsAndChords(t *testing.T) {
	db, err := stdsql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open sqlite error = %v", err)
	}
	defer db.Close()
	backend := NewBackend(db, Options{Dialect: "sqlite"})
	if err := backend.ApplyMigrations(context.Background()); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	expires := time.Now().Add(time.Hour).UTC()
	result := queue.Result{
		TaskID:    "task-1",
		State:     queue.StateFailure,
		Error:     "boom",
		Traceback: "trace",
		Children:  []string{"child-1"},
		ExpiresAt: &expires,
	}
	if err := backend.StoreResult(context.Background(), result); err != nil {
		t.Fatalf("StoreResult() error = %v", err)
	}
	stored, err := backend.GetResult(context.Background(), "task-1")
	if err != nil || stored.State != queue.StateFailure || stored.Error != "boom" || stored.Traceback != "trace" {
		t.Fatalf("GetResult() = %#v, %v", stored, err)
	}
	children, err := backend.Children(context.Background(), "task-1")
	if err != nil || len(children) != 1 || children[0] != "child-1" {
		t.Fatalf("Children() = %#v, %v", children, err)
	}
	group, err := backend.GroupResult(context.Background(), "group-1", []string{"task-1"})
	if err != nil || group.ID != "group-1" || len(group.Children) != 1 {
		t.Fatalf("GroupResult() = %#v, %v", group, err)
	}
	count, err := backend.ChordCounter(context.Background(), "chord-1", 3)
	if err != nil || count != 3 {
		t.Fatalf("ChordCounter() = %d, %v", count, err)
	}
	if err := backend.Forget(context.Background(), "task-1"); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	if _, err := backend.GetResult(context.Background(), "task-1"); err == nil {
		t.Fatalf("forgotten result should be missing")
	}
}
