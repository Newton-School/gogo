package postgres_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type durationModel struct {
	models.Base
	ID      int64
	Elapsed time.Duration
}

func (*durationModel) Schema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "Duration", Fields: []models.Field{
		models.BigAutoField("id", func(f *models.Field) { f.StructField = "ID" }),
		models.DurationField("elapsed", func(f *models.Field) { f.StructField = "Elapsed" }),
	}}
}

func TestDurationHydrationAcrossSaveValuesAggregateBulkAndRawSQL(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&durationModel{}).Schema()
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(struct{ db.Backend }{b}, nil)
	first := &durationModel{Elapsed: 25*time.Hour + time.Microsecond}
	if err := store.Save(ctx, first, orm.SaveOptions{}); err != nil {
		t.Fatal("typed returning failed", err)
	}
	if first.Elapsed != 25*time.Hour+time.Microsecond {
		t.Fatal("returning duration changed")
	}
	query := orm.For(store, func() *durationModel { return &durationModel{} })
	fresh, err := query.Get(ctx)
	if err != nil || fresh.Elapsed != first.Elapsed {
		t.Fatal("typed hydration failed", err)
	}
	values, err := query.Values(ctx, "elapsed")
	if err != nil || values[0]["elapsed"] != first.Elapsed {
		t.Fatal("Values duration failed", values, err)
	}
	mapQuery := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r })
	mapRow, err := mapQuery.Get(ctx)
	if err != nil || mustValue(t, mapRow, "elapsed") != first.Elapsed {
		t.Fatal("MapRecord duration failed", err)
	}
	var raw any
	if err := db.QueryRow(ctx, b, `SELECT elapsed FROM tests_duration WHERE id=$1`, []any{first.ID}, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw.(string); !ok {
		t.Fatal("raw driver value was normalized", raw)
	}
	if _, err := b.Exec(ctx, `UPDATE tests_duration SET elapsed=INTERVAL '2 days -00:00:00.000001' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshFromDB(ctx, first, "elapsed"); err != nil || first.Elapsed != 48*time.Hour-time.Microsecond {
		t.Fatal("days/microseconds refresh failed", err)
	}
	// This Backend-only decorator intentionally does not expose the optional
	// AutoKeyAllocator, so bulk inputs provide their stable identities.
	more := []*durationModel{{ID: 100, Elapsed: -time.Second - time.Microsecond}, {ID: 101, Elapsed: 2*time.Second + 2*time.Microsecond}}
	if _, err := orm.BulkCreate(ctx, store, more, orm.BulkOptions{}); err != nil {
		t.Fatal("bulk duration failed", err)
	}
	more[0].Elapsed = -time.Second
	if _, err := orm.BulkUpdate(ctx, store, more, []string{"elapsed"}, orm.BulkOptions{}); err != nil {
		t.Fatal("bulk-update duration failed", err)
	}
	result, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"sum": orm.Typed(orm.Sum(orm.F("elapsed")), models.DurationField("total"))})
	if err != nil || result["sum"] != 48*time.Hour+time.Second+time.Microsecond {
		t.Fatal("aggregate duration failed", result, err)
	}
	for _, predicate := range []db.Predicate{orm.Q("elapsed", -time.Second), orm.Q("elapsed__in", []time.Duration{-time.Second}), orm.Q("elapsed__range", []time.Duration{-2 * time.Second, 0})} {
		if n, err := query.Filter(predicate).Count(ctx); err != nil || n != 1 {
			t.Fatal("duration comparison failed", n, err)
		}
	}
	for _, duration := range []time.Duration{0, time.Microsecond, -time.Microsecond, time.Duration(math.MaxInt64 / 1000 * 1000), -time.Duration(math.MaxInt64 / 1000 * 1000)} {
		first.Elapsed = duration
		if err := store.Save(ctx, first, orm.SaveOptions{ForceUpdate: true}); err != nil || first.Elapsed != duration {
			t.Fatal("Duration endpoint returning lost precision", duration, err)
		}
		fresh, err := query.Filter(orm.Q("id", first.ID)).Get(ctx)
		if err != nil || fresh.Elapsed != duration {
			t.Fatal("Duration endpoint hydration lost precision", duration, err)
		}
	}
	for _, invalid := range []string{"1 mon", "300 years", "2562047:47:16.854776"} {
		if _, err := b.Exec(ctx, `UPDATE tests_duration SET elapsed=$1::interval WHERE id=$2`, invalid, first.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := query.Filter(orm.Q("id", first.ID)).Get(ctx); err == nil {
			t.Fatal("ambiguous/overflowed interval became a Go duration")
		}
	}
}

func TestDurationHydrationInEagerRelationsAndDeletionGraph(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := (&durationModel{}).Schema()
	source := models.Schema{AppLabel: "tests", Name: "DurationOwner", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("duration", models.Relation{Target: target.Key(), OnDelete: models.Protect})}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{target, source} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := b.SchemaEditor().(db.SchemaResolverEditor).WithSchemas([]models.Schema{target, source})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []models.Schema{target, source} {
		if err := editor.CreateModel(ctx, b, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(b, registry)
	row := &durationModel{Elapsed: time.Hour + time.Microsecond}
	if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	saveMap(t, store, source, map[string]any{"duration": row.ID})
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(source); return r }).SelectRelated("duration")
	owner, err := query.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joined, ok := owner.State().Related["duration"].(models.Record)
	if !ok || mustValue(t, joined, "elapsed") != row.Elapsed {
		t.Fatal("joined duration not materialized")
	}
	bound, err := models.Bind(row)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (orm.DeleteCollector{Store: store}).Collect(ctx, bound)
	if err != nil || len(plan.Objects) != 1 || len(plan.Protected) != 1 || mustValue(t, plan.Objects[0], "elapsed") != row.Elapsed {
		t.Fatal("deletion collector duration changed", err)
	}
}
