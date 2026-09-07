package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type deleteOpaqueBytes struct{ data []byte }
type deleteOpaquePointer struct{ data *string }
type deleteOpaqueCodec string

func (deleteOpaqueCodec) Encode(any) (any, error) { return "opaque", nil }
func (kind deleteOpaqueCodec) Decode(value any) (any, error) {
	text := value.(string)
	if kind == "bytes" {
		return deleteOpaqueBytes{[]byte(text)}, nil
	}
	return deleteOpaquePointer{&text}, nil
}

func setupDeletePayload(t *testing.T, codec models.Codec) (*orm.Store, *models.MapRecord) {
	t.Helper()
	b := openTest(t)
	schema := models.Schema{AppLabel: "tests", Name: "DeletePayload", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload"), models.BinaryField("bytes")}}
	if codec != nil {
		schema.Fields[1] = models.TextField("payload", func(field *models.Field) { field.Codec = codec })
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := b.SchemaEditor().CreateModel(context.Background(), b, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(context.Background(), `INSERT INTO tests_deletepayload (payload, bytes) VALUES ($1, $2)`, `{"nested":[{"value":"original"}]}`, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	_ = record.Set("id", int64(1))
	record.State().Persisted = true
	record.State().Database = b.Alias()
	return orm.New(b, registry), record
}

func deleteTableCount(t *testing.T, store *orm.Store, table string) int64 {
	t.Helper()
	var count int64
	rows, err := store.Backend.Query(context.Background(), "SELECT COUNT(*) FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("missing count row", rows.Err())
	}
	if err := rows.Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDeleteCallbackOpaqueCodecsFailBeforeEffectsOnlyWhenViewsRequired(t *testing.T) {
	for _, codec := range []deleteOpaqueCodec{"bytes", "pointer"} {
		for _, stage := range []string{"authorize", "before", "after", "none"} {
			t.Run(string(codec)+"/"+stage, func(t *testing.T) {
				store, root := setupDeletePayload(t, codec)
				collector := orm.DeleteCollector{Store: store}
				// Public preview collection retains its existing codec contract.
				if preview, err := collector.Collect(context.Background(), root); err != nil || len(preview.Objects) != 1 {
					t.Fatal("opaque preview rejected", err)
				}
				calls := 0
				hook := func(context.Context, orm.DeleteEvent) error { calls++; return nil }
				switch stage {
				case "authorize":
					collector.Authorize = func(context.Context, orm.DeletionPlan) error { calls++; return nil }
				case "before":
					store.BeforeDelete = []orm.DeleteReceiver{hook}
				case "after":
					store.AfterDelete = []orm.DeleteReceiver{hook}
				}
				counts, err := collector.Execute(context.Background(), root)
				if stage == "none" {
					if err != nil || counts[root.Schema().Key()] != 1 || deleteTableCount(t, store, "tests_deletepayload") != 0 {
						t.Fatal("ordinary opaque deletion regressed", counts, err)
					}
				} else if !errors.Is(err, orm.ErrDeleteCallbackView) || calls != 0 || deleteTableCount(t, store, "tests_deletepayload") != 1 || !root.State().Persisted {
					t.Fatal("unsafe value reached callbacks or SQL effects", calls, err)
				}
			})
		}
	}
}

func TestDeleteCallbackMutableValuesRollbackAndTransactionalAudit(t *testing.T) {
	for _, stage := range []string{"before_json", "before_bytes", "after_json", "after_bytes", "state_database", "audit_only"} {
		t.Run(stage, func(t *testing.T) {
			store, root := setupDeletePayload(t, nil)
			if _, err := store.Backend.Exec(context.Background(), "CREATE TABLE deletion_audit (deleted_id bigint NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			hook := func(ctx context.Context, event orm.DeleteEvent) error {
				id, _ := event.Record.Get("id")
				if _, err := db.ExecutorFor(ctx, store.Backend).Exec(ctx, "INSERT INTO deletion_audit (deleted_id) VALUES ($1)", id); err != nil {
					return err
				}
				switch stage {
				case "before_json", "after_json":
					value, _ := event.Record.Get("payload")
					value.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed"
				case "before_bytes", "after_bytes":
					value, _ := event.Record.Get("bytes")
					value.([]byte)[0] = 9
				case "state_database":
					event.Record.State().Database = "another_database"
				}
				return nil
			}
			if stage == "after_json" || stage == "after_bytes" || stage == "audit_only" {
				store.AfterDelete = []orm.DeleteReceiver{hook}
			} else {
				store.BeforeDelete = []orm.DeleteReceiver{hook}
			}
			counts, err := (orm.DeleteCollector{Store: store}).Execute(context.Background(), root)
			if stage == "audit_only" {
				if err != nil || counts[root.Schema().Key()] != 1 || deleteTableCount(t, store, "deletion_audit") != 1 || deleteTableCount(t, store, "tests_deletepayload") != 0 {
					t.Fatal("legitimate transactional hook failed", counts, err)
				}
			} else if !errors.Is(err, orm.ErrDeleteCallbackMutation) || deleteTableCount(t, store, "deletion_audit") != 0 || deleteTableCount(t, store, "tests_deletepayload") != 1 || !root.State().Persisted {
				t.Fatal("mutation or audit escaped rollback", counts, err)
			}
		})
	}
}

func TestDeleteAuthorizeRelationUpdateCannotBeRetargeted(t *testing.T) {
	for _, target := range []string{"field", "value", "record"} {
		t.Run(target, func(t *testing.T) {
			store, root, child := setupRelations(t, models.SetNull, false)
			collector := orm.DeleteCollector{Store: store, Authorize: func(_ context.Context, plan orm.DeletionPlan) error {
				if len(plan.Updates) != 1 {
					t.Fatal("missing SET_NULL update")
				}
				switch target {
				case "field":
					plan.Updates[0].Field = "tenant"
				case "value":
					plan.Updates[0].Value = int64(2)
				case "record":
					plan.Updates[0].Record = plan.Objects[0]
				}
				return nil
			}}
			record, _ := models.Bind(root)
			if _, err := collector.Execute(context.Background(), record); !errors.Is(err, orm.ErrDeleteCallbackMutation) {
				t.Fatal("relation update mutation accepted", err)
			}
			loaded, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Get(context.Background())
			if err != nil || loaded.Parent == nil || *loaded.Parent != root.ID || loaded.Tenant != 1 || deleteTableCount(t, store, root.Schema().DBTable()) != 1 {
				t.Fatal("relation update escaped rollback", err)
			}
		})
	}
}

func TestDeleteAuthorizeAutomaticJoinDescriptorsCannotBeRetargeted(t *testing.T) {
	for _, target := range []string{"field", "record", "endpoint"} {
		t.Run(target, func(t *testing.T) {
			store, root, tags, through := setupMany(t, false)
			manager := orm.RelationManager{Store: store, Source: root, Name: "tags"}
			if err := manager.Add(context.Background(), tags[0]); err != nil {
				t.Fatal(err)
			}
			collector := orm.DeleteCollector{Store: store, Authorize: func(_ context.Context, plan orm.DeletionPlan) error {
				if len(plan.JoinRemovals) != 1 {
					t.Fatal("missing automatic join removal")
				}
				switch target {
				case "field":
					plan.JoinRemovals[0].Field = "target_id"
				case "record":
					plan.JoinRemovals[0].Record = plan.Objects[0]
				case "endpoint":
					plan.JoinRemovals[0].Endpoint = tags[0]
				}
				return nil
			}}
			if _, err := collector.Execute(context.Background(), root); !errors.Is(err, orm.ErrDeleteCallbackMutation) {
				t.Fatal("join mutation accepted", err)
			}
			if deleteTableCount(t, store, through.DBTable()) != 1 || deleteTableCount(t, store, root.Schema().DBTable()) != 1 || deleteTableCount(t, store, tags[0].Schema().DBTable()) != 3 {
				t.Fatal("join mutation escaped rollback")
			}
		})
	}
}

func TestDeleteScopeMetadataCannotRetargetCollectionOrMutation(t *testing.T) {
	for _, phase := range []string{"collection", "mutation"} {
		t.Run(phase, func(t *testing.T) {
			store, root, child := setupRelations(t, models.SetNull, false)
			ctx := context.Background()
			root.Tenant = 2
			if err := store.Save(ctx, root, orm.SaveOptions{}); err != nil {
				t.Fatal(err)
			}
			originalID := root.ID
			other := &relationRow{Definition: root.Schema(), Tenant: 1}
			if err := store.Save(ctx, other, orm.SaveOptions{}); err != nil {
				t.Fatal(err)
			}
			authorized := false
			collector := orm.DeleteCollector{Store: store, Authorize: func(_ context.Context, plan orm.DeletionPlan) error {
				authorized = true
				if len(plan.Objects) != 1 {
					t.Fatal("unexpected graph")
				}
				id, _ := plan.Objects[0].Get("id")
				if id != originalID {
					t.Fatal("wrong root authorized", id)
				}
				return nil
			}, Scope: func(_ context.Context, schema models.Schema) (db.Predicate, error) {
				if schema.Key() == root.Schema().Key() && (phase == "collection" || authorized) {
					// If this descriptor reached the compiler, id=1 would become
					// tenant=1 and would delete the other same-scope parent.
					schema.Fields[0].Column = "tenant"
				}
				return orm.Q("tenant__in", []int64{1, 2}), nil
			}}
			record, _ := models.Bind(root)
			counts, err := collector.Execute(ctx, record)
			if err != nil || counts[root.Schema().Key()] != 1 {
				t.Fatal("scope snapshot failed", counts, err)
			}
			remaining, err := orm.For(store, func() *relationRow { return &relationRow{Definition: root.Schema()} }).All(ctx)
			if err != nil || len(remaining) != 1 || remaining[0].ID != other.ID || root.Schema().Fields[0].Column != "" {
				t.Fatal("scope metadata retargeted original graph", remaining, err)
			}
			loaded, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Get(ctx)
			if err != nil || loaded.Parent != nil {
				t.Fatal("original relation update was lost", err)
			}
		})
	}
}

func TestDeleteCallbacksCannotRetargetAuthorizedRoot(t *testing.T) {
	for _, stage := range []string{"authorize_record", "authorize_slice", "before_delete", "after_delete_captured_root", "after_delete_captured_before"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			policy := models.SetNull
			stale := stage == "after_delete_captured_root" || stage == "after_delete_captured_before"
			if stale {
				policy = models.Cascade
			}
			store, root, child := setupRelations(t, policy, false)
			originalID := root.ID
			other := &relationRow{Definition: root.Schema(), Tenant: 1}
			if err := store.Save(ctx, other, orm.SaveOptions{}); err != nil {
				t.Fatal(err)
			}
			otherRecord, err := models.Bind(other)
			if err != nil {
				t.Fatal(err)
			}
			var captured models.Record
			collector := orm.DeleteCollector{Store: store, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", int64(1)), nil }}
			collector.Authorize = func(_ context.Context, plan orm.DeletionPlan) error {
				for i, object := range plan.Objects {
					if object.Schema().Key() == root.Schema().Key() {
						id, _ := object.Get("id")
						if id != originalID {
							t.Fatal("collector authorized a different root", id)
						}
						captured = object
						if stage == "authorize_record" {
							return object.Set("id", other.ID)
						}
						if stage == "authorize_slice" {
							plan.Objects[i] = otherRecord
						}
					}
				}
				return nil
			}
			store.BeforeDelete = []orm.DeleteReceiver{func(_ context.Context, event orm.DeleteEvent) error {
				if stage == "after_delete_captured_before" && event.Record.Schema().Key() == root.Schema().Key() {
					captured = event.Record
				}
				if stage == "before_delete" && event.Record.Schema().Key() == root.Schema().Key() {
					return event.Record.Set("id", other.ID)
				}
				return nil
			}}
			store.AfterDelete = []orm.DeleteReceiver{func(_ context.Context, event orm.DeleteEvent) error {
				if stale && event.Record.Schema().Key() == child.Schema().Key() {
					return captured.Set("id", other.ID)
				}
				return nil
			}}
			rootRecord, err := models.Bind(root)
			if err != nil {
				t.Fatal(err)
			}
			counts, deleteErr := collector.Execute(ctx, rootRecord)
			remaining, err := orm.For(store, func() *relationRow { return &relationRow{Definition: root.Schema()} }).All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var remainingIDs []int64
			for _, row := range remaining {
				remainingIDs = append(remainingIDs, row.ID)
			}
			if stale {
				if deleteErr != nil || len(remainingIDs) != 1 || remainingIDs[0] != other.ID || counts[root.Schema().Key()] != 1 {
					t.Fatal("stale detached view changed canonical deletion", remainingIDs, counts, deleteErr)
				}
			} else if !errors.Is(deleteErr, orm.ErrDeleteCallbackMutation) || len(remainingIDs) != 2 || root.ID != originalID {
				t.Fatalf("callback retargeting was not rejected: authorized=%d other=%d remaining=%v counts=%v err=%v", originalID, other.ID, remainingIDs, counts, deleteErr)
			}
		})
	}
}
