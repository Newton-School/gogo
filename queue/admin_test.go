package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/auth"
	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestQueueAdminRegistrationMetadata(t *testing.T) {
	registry := admin.NewRegistry()
	if err := q.RegisterAdmin(registry, q.QueueAdminOptions{}); err != nil {
		t.Fatalf("RegisterAdmin() error = %v", err)
	}
	want := []string{
		"queue.TaskResult",
		"queue.GroupResult",
		"queue.PeriodicTask",
		"queue.IntervalSchedule",
		"queue.CrontabSchedule",
		"queue.ClockedSchedule",
		"queue.WorkerHeartbeat",
		"queue.QueueHealth",
	}
	for _, label := range want {
		modelAdmin, ok := registry.GetAdmin(label)
		if !ok {
			t.Fatalf("missing admin model %s", label)
		}
		if !modelAdmin.AllowUnmanaged || len(modelAdmin.ListDisplay) == 0 {
			t.Fatalf("admin metadata for %s = %#v", label, modelAdmin)
		}
	}
}

func TestQueueAdminActionsAndPermissions(t *testing.T) {
	ctx := context.Background()
	revocations := q.NewRevocationRegistry()
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	store := q.NewMemoryScheduleStore(q.MemoryScheduleStoreOptions{})
	_ = store.Save(ctx, q.ScheduleEntry{Name: "daily", Signature: q.NewSignature("jobs.run"), Schedule: q.IntervalSchedule{Every: time.Hour}, Enabled: true})
	options := q.QueueAdminOptions{Broker: broker, Store: store, Revocations: revocations}
	actions := q.QueueAdminActions(options)
	revoke := findQueueAdminAction(t, actions, "revoke_tasks")
	denied := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{IsActive: true}}}
	if _, err := admin.ExecuteAction(revoke, admin.ActionContext{User: denied, Selected: []map[string]any{{"task_id": "task-1"}}}); !errors.Is(err, admin.ErrAdminPermissionDenied) {
		t.Fatalf("denied action error = %v", err)
	}
	superuser := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{IsActive: true, IsSuperuser: true}}}
	result, err := admin.ExecuteAction(revoke, admin.ActionContext{User: superuser, Selected: []map[string]any{{"task_id": "task-1"}}})
	if err != nil || result.Message != "Revoked 1 task(s)" {
		t.Fatalf("revoke result = %#v, %v", result, err)
	}
	if !revocations.IsRevoked(q.Envelope{ID: "task-1"}) {
		t.Fatal("task was not revoked")
	}
	purge := findQueueAdminAction(t, actions, "purge_queues")
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-2", Name: "jobs.run"}, brokers.PublishOptions{})
	result, err = admin.ExecuteAction(purge, admin.ActionContext{User: superuser, Selected: []map[string]any{{"queue": "default"}}})
	if err != nil || result.Message != "Purged 1 task(s)" {
		t.Fatalf("purge result = %#v, %v", result, err)
	}
	disable := findQueueAdminAction(t, actions, "disable_schedules")
	if _, err := admin.ExecuteAction(disable, admin.ActionContext{User: superuser, Selected: []map[string]any{{"name": "daily"}}}); err != nil {
		t.Fatalf("disable action error = %v", err)
	}
	entries, _ := store.List(ctx)
	if len(entries) != 1 || entries[0].Enabled {
		t.Fatalf("schedule should be disabled: %#v", entries)
	}
	enable := findQueueAdminAction(t, actions, "enable_schedules")
	if _, err := admin.ExecuteAction(enable, admin.ActionContext{User: superuser, Selected: []map[string]any{{"name": "daily"}}}); err != nil {
		t.Fatalf("enable action error = %v", err)
	}
	entries, _ = store.List(ctx)
	if !entries[0].Enabled {
		t.Fatalf("schedule should be enabled: %#v", entries)
	}
}

func findQueueAdminAction(t *testing.T, actions []admin.Action, name string) admin.Action {
	t.Helper()
	for _, action := range actions {
		if action.Name == name {
			return action
		}
	}
	t.Fatalf("action %s missing", name)
	return admin.Action{}
}
