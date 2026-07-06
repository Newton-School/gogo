package queue

import (
	"context"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/models"
	"github.com/google/uuid"
)

type QueueAdminOptions struct {
	Broker      Broker
	Store       ScheduleStore
	Revocations *RevocationRegistry
	Inspector   *Inspector
}

type QueueAdminModel struct {
	Metadata models.Metadata
	Admin    admin.ModelAdmin
}

type QueueAdminView struct {
	Name       string
	ModelLabel string
	Columns    []string
	Actions    []string
}

func RegisterAdmin(registry *admin.Registry, options QueueAdminOptions) error {
	if registry == nil {
		return fmt.Errorf("admin registry is required")
	}
	for _, model := range QueueAdminModels(options) {
		if err := registry.RegisterMetadata(model.Metadata, model.Admin); err != nil {
			return err
		}
	}
	return nil
}

func QueueAdminModels(options QueueAdminOptions) []QueueAdminModel {
	actions := QueueAdminActions(options)
	return []QueueAdminModel{
		queueAdminModel("TaskResult", "queue_task_results", []string{"task_id", "state", "updated_at"}, []admin.Action{findAdminAction(actions, "revoke_tasks"), findAdminAction(actions, "retry_tasks")}),
		queueAdminModel("GroupResult", "queue_group_results", []string{"group_id", "children", "created_at"}, nil),
		queueAdminModel("PeriodicTask", "queue_periodic_tasks", []string{"name", "enabled", "last_run_at", "total_run_count"}, []admin.Action{findAdminAction(actions, "enable_schedules"), findAdminAction(actions, "disable_schedules")}),
		queueAdminModel("IntervalSchedule", "queue_interval_schedules", []string{"every", "start_at"}, nil),
		queueAdminModel("CrontabSchedule", "queue_crontab_schedules", []string{"minute", "hour", "day_of_week", "timezone"}, nil),
		queueAdminModel("ClockedSchedule", "queue_clocked_schedules", []string{"run_at"}, nil),
		queueAdminModel("WorkerHeartbeat", "queue_worker_heartbeats", []string{"hostname", "last_seen_at", "status"}, nil),
		queueAdminModel("QueueHealth", "queue_health", []string{"name", "ready", "in_flight", "durable"}, []admin.Action{findAdminAction(actions, "purge_queues")}),
	}
}

func QueueAdminViews(options QueueAdminOptions) []QueueAdminView {
	models := QueueAdminModels(options)
	views := make([]QueueAdminView, len(models))
	for i, model := range models {
		actions := make([]string, len(model.Admin.ActionDefinitions))
		for index, action := range model.Admin.ActionDefinitions {
			actions[index] = action.Name
		}
		views[i] = QueueAdminView{
			Name:       model.Metadata.ModelName,
			ModelLabel: model.Metadata.Label(),
			Columns:    append([]string(nil), model.Admin.ListDisplay...),
			Actions:    actions,
		}
	}
	return views
}

func QueueAdminActions(options QueueAdminOptions) []admin.Action {
	return []admin.Action{
		{
			Name:        "revoke_tasks",
			Label:       "Revoke selected tasks",
			Permissions: []string{"queue.change_taskresult"},
			Handler: func(ctx admin.ActionContext) (admin.ActionResult, error) {
				count := 0
				for _, row := range ctx.Selected {
					taskID := rowString(row, "task_id", "id")
					if taskID == "" {
						continue
					}
					if options.Revocations != nil {
						options.Revocations.RevokeTask(taskID)
					}
					count++
				}
				return admin.ActionResult{Message: fmt.Sprintf("Revoked %d task(s)", count)}, nil
			},
		},
		{
			Name:        "retry_tasks",
			Label:       "Retry selected tasks",
			Permissions: []string{"queue.change_taskresult"},
			Handler: func(ctx admin.ActionContext) (admin.ActionResult, error) {
				count := 0
				for _, row := range ctx.Selected {
					taskName := rowString(row, "task_name", "name")
					if taskName == "" || options.Broker == nil {
						continue
					}
					queueName := rowString(row, "queue")
					if queueName == "" {
						queueName = "default"
					}
					_, err := options.Broker.Publish(context.Background(), queueName, Envelope{ID: uuid.NewString(), Name: taskName}, BrokerPublishOptions{})
					if err != nil {
						return admin.ActionResult{}, err
					}
					count++
				}
				return admin.ActionResult{Message: fmt.Sprintf("Retried %d task(s)", count)}, nil
			},
		},
		{
			Name:        "purge_queues",
			Label:       "Purge selected queues",
			Permissions: []string{"queue.delete_queuehealth"},
			Handler: func(ctx admin.ActionContext) (admin.ActionResult, error) {
				total := 0
				for _, row := range ctx.Selected {
					queueName := rowString(row, "queue", "name")
					if queueName == "" || options.Broker == nil {
						continue
					}
					count, err := options.Broker.PurgeQueue(context.Background(), queueName)
					if err != nil {
						return admin.ActionResult{}, err
					}
					total += count
				}
				return admin.ActionResult{Message: fmt.Sprintf("Purged %d task(s)", total)}, nil
			},
		},
		{
			Name:        "enable_schedules",
			Label:       "Enable selected schedules",
			Permissions: []string{"queue.change_periodictask"},
			Handler: func(ctx admin.ActionContext) (admin.ActionResult, error) {
				count, err := setScheduleEnabled(options.Store, ctx.Selected, true)
				if err != nil {
					return admin.ActionResult{}, err
				}
				return admin.ActionResult{Message: fmt.Sprintf("Enabled %d schedule(s)", count)}, nil
			},
		},
		{
			Name:        "disable_schedules",
			Label:       "Disable selected schedules",
			Permissions: []string{"queue.change_periodictask"},
			Handler: func(ctx admin.ActionContext) (admin.ActionResult, error) {
				count, err := setScheduleEnabled(options.Store, ctx.Selected, false)
				if err != nil {
					return admin.ActionResult{}, err
				}
				return admin.ActionResult{Message: fmt.Sprintf("Disabled %d schedule(s)", count)}, nil
			},
		},
	}
}

func queueAdminModel(modelName string, tableName string, listDisplay []string, actions []admin.Action) QueueAdminModel {
	return QueueAdminModel{
		Metadata: models.Metadata{AppLabel: "queue", ModelName: modelName, TableName: tableName},
		Admin: admin.ModelAdmin{
			AllowUnmanaged:    true,
			ListDisplay:       append([]string(nil), listDisplay...),
			SearchFields:      []string{"id", "name"},
			ListFilter:        []string{"state", "enabled"},
			ReadonlyFields:    append([]string(nil), listDisplay...),
			ActionDefinitions: compactAdminActions(actions),
		},
	}
}

func compactAdminActions(actions []admin.Action) []admin.Action {
	compacted := make([]admin.Action, 0, len(actions))
	for _, action := range actions {
		if action.Name != "" {
			compacted = append(compacted, action)
		}
	}
	return compacted
}

func findAdminAction(actions []admin.Action, name string) admin.Action {
	for _, action := range actions {
		if action.Name == name {
			return action
		}
	}
	return admin.Action{}
}

func rowString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := row[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case fmt.Stringer:
			return strings.TrimSpace(typed.String())
		default:
			return strings.TrimSpace(fmt.Sprint(typed))
		}
	}
	return ""
}

func setScheduleEnabled(store ScheduleStore, selected []map[string]any, enabled bool) (int, error) {
	if store == nil {
		return 0, nil
	}
	entries, err := store.List(context.Background())
	if err != nil {
		return 0, err
	}
	byName := map[string]ScheduleEntry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	count := 0
	for _, row := range selected {
		name := rowString(row, "name")
		entry, ok := byName[name]
		if !ok {
			continue
		}
		entry.Enabled = enabled
		if err := store.Save(context.Background(), entry); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
