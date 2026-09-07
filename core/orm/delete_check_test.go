package orm

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestCheckDeletionPlanIsolatesRepeatedPolicyViews(t *testing.T) {
	schema := models.Schema{AppLabel: "test", Name: "DeletionCheck", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("data")}}
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	_ = record.Set("id", int64(1))
	_ = record.Set("data", map[string]any{"tags": []string{"original"}})
	plan := DeletionPlan{Objects: []models.Record{record}, Updates: []FieldUpdate{{Record: record, Field: "data", Value: map[string]any{"next": true}}}}
	var retained models.Record
	if err := CheckDeletionPlan(context.Background(), plan, func(_ context.Context, view DeletionPlan) error { retained = view.Objects[0]; return nil }); err != nil {
		t.Fatal(err)
	}
	_ = retained.Set("id", int64(99))
	data, _ := retained.Get("data")
	data.(map[string]any)["tags"].([]string)[0] = "stale"
	if err := CheckDeletionPlan(context.Background(), plan, func(_ context.Context, view DeletionPlan) error {
		id, _ := view.Objects[0].Get("id")
		data, _ := view.Objects[0].Get("data")
		if id != int64(1) || data.(map[string]any)["tags"].([]string)[0] != "original" {
			t.Fatal("stale view changed later check")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(DeletionPlan){
		func(view DeletionPlan) { _ = view.Objects[0].Set("id", int64(2)) },
		func(view DeletionPlan) { view.Objects[0].State().Persisted = true },
		func(view DeletionPlan) { view.Updates[0].Field = "id" },
		func(view DeletionPlan) { view.Updates[0].Value.(map[string]any)["next"] = false },
	} {
		if err := CheckDeletionPlan(context.Background(), plan, func(_ context.Context, view DeletionPlan) error { mutate(view); return nil }); !errors.Is(err, ErrDeleteCallbackMutation) {
			t.Fatal("mutation not rejected", err)
		}
	}
	id, _ := record.Get("id")
	if id != int64(1) || !plan.Updates[0].Value.(map[string]any)["next"].(bool) {
		t.Fatal("canonical plan changed")
	}
}

func TestCheckDeletionPlanPreflightAndCancellation(t *testing.T) {
	called := false
	check := func(context.Context, DeletionPlan) error { called = true; return nil }
	if err := CheckDeletionPlan(nil, DeletionPlan{}, check); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal(err)
	}
	if err := CheckDeletionPlan(context.Background(), DeletionPlan{}, nil); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckDeletionPlan(ctx, DeletionPlan{}, check); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := CheckDeletionPlan(context.Background(), DeletionPlan{Protected: []ProtectedRelation{{}}}, check); !errors.Is(err, ErrProtectedRelation) {
		t.Fatal(err)
	}
	if err := CheckDeletionPlan(context.Background(), DeletionPlan{Objects: make([]models.Record, 100001)}, check); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal(err)
	}
	if called {
		t.Fatal("callback ran before preflight")
	}
	denied := errors.New("denied")
	if err := CheckDeletionPlan(context.Background(), DeletionPlan{}, func(context.Context, DeletionPlan) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	if err := CheckDeletionPlan(ctx, DeletionPlan{}, func(context.Context, DeletionPlan) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
