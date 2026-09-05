package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestScopedQueryUpdateArithmeticJSONHooksAndAtomicFailures(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	validatorCalls, saveCalls := 0, 0
	schema := models.Schema{AppLabel: "tests", Name: "UpdateCounter", Fields: []models.Field{
		models.BigAutoField("id"), models.IntegerField("tenant"), models.BigIntegerField("counter"), models.BigIntegerField("mirror"),
		models.CharField("name", func(f *models.Field) {
			f.MaxLength = 64
			f.Unique = true
			f.Validators = []models.Validator{func(context.Context, any) error { validatorCalls++; return errors.New("explicit validation rejected") }}
		}),
		models.DateTimeField("modified", func(f *models.Field) { f.AutoNow = true }), models.JSONField("payload", models.Nullable),
	}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	first := saveMap(t, store, schema, map[string]any{"tenant": 1, "counter": 5, "mirror": 0, "name": "first", "payload": nil})
	second := saveMap(t, store, schema, map[string]any{"tenant": 1, "counter": 8, "mirror": 0, "name": "second", "payload": nil})
	hidden := saveMap(t, store, schema, map[string]any{"tenant": 2, "counter": 100, "mirror": 0, "name": "hidden", "payload": nil})
	store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error {
		saveCalls++
		return errors.New("instance hook must be skipped")
	}}
	all := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r })
	query := all.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	firstQuery := query.Filter(orm.Q("id", mustValue(t, first, "id")))
	before, argsBefore, err := query.SQLContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := query.OrderBy("-name").Only("id").Update(ctx, map[string]any{"counter": orm.Add(orm.F("counter"), orm.Value(1)), "mirror": orm.F("counter")})
	if err != nil || matched != 2 || saveCalls != 0 || validatorCalls != 0 {
		t.Fatal("query update scope/hooks failed", matched, err, saveCalls, validatorCalls)
	}
	for _, entry := range []struct {
		record          *models.MapRecord
		counter, mirror int64
	}{{first, 6, 5}, {second, 9, 8}, {hidden, 100, 0}} {
		fresh, err := all.Filter(orm.Q("id", mustValue(t, entry.record, "id"))).Get(ctx)
		if err != nil || mustValue(t, fresh, "counter") != entry.counter || mustValue(t, fresh, "mirror") != entry.mirror || !mustValue(t, fresh, "modified").(time.Time).Equal(mustValue(t, entry.record, "modified").(time.Time)) {
			t.Fatal("old-row arithmetic, scope or timestamp changed", err)
		}
	}
	if mustValue(t, first, "counter") != int64(5) {
		t.Fatal("query update silently refreshed loaded model")
	}
	if count, err := query.Update(ctx, map[string]any{"counter": orm.F("counter")}); err != nil || count != 2 {
		t.Fatal("matched count omitted unchanged rows", count, err)
	}
	if count, err := query.Filter(orm.Q("id", -1)).Update(ctx, map[string]any{"counter": 1}); err != nil || count != 0 {
		t.Fatal("missing rows falsely updated", count, err)
	}
	if count, err := query.Update(ctx, nil); err != nil || count != 0 {
		t.Fatal("empty assignment not a no-op", count, err)
	}
	for _, value := range []any{nil, (*json.RawMessage)(nil), models.JSONNull, "null", map[string]any{"precise": json.Number("9007199254740993")}, orm.Value("literal"), orm.JSONPath("payload", "precise")} {
		if _, err := firstQuery.Update(ctx, map[string]any{"payload": value}); err != nil {
			t.Fatal("JSON assignment failed", err)
		}
		fresh, err := firstQuery.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := mustValue(t, fresh, "payload")
		switch value.(type) {
		case nil, *json.RawMessage:
			if got != nil {
				t.Fatal("SQL NULL lost")
			}
		case string:
			if got != "null" {
				t.Fatal("native JSON string changed", got)
			}
		case map[string]any:
			if got.(map[string]any)["precise"] != json.Number("9007199254740993") {
				t.Fatal("JSON number lost precision")
			}
		default:
			if value == models.JSONNull && got != models.JSONNull {
				t.Fatal("JSON null lost")
			}
		}
	}
	integer := models.BigIntegerField("result")
	if n, err := query.Update(ctx, map[string]any{"counter": orm.Case(integer, orm.Value(0), orm.When(orm.Q("counter__gt", 7), orm.Value(20)))}); err != nil || n != 2 {
		t.Fatal("conditional update unavailable", n, err)
	}
	fresh, _ := firstQuery.Get(ctx)
	if mustValue(t, fresh, "counter") != int64(0) {
		t.Fatal("conditional update chose wrong branch")
	}
	rollback := errors.New("outer rollback")
	err = db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		if n, err := firstQuery.Update(ctx, map[string]any{"counter": 99}); err != nil || n != 1 {
			return fmt.Errorf("nested update: %d: %w", n, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	fresh, _ = firstQuery.Get(ctx)
	if mustValue(t, fresh, "counter") != int64(0) {
		t.Fatal("ambient rollback did not own update")
	}
	err = db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		if n, err := query.Update(ctx, map[string]any{"name": "collision"}); !db.IsCode(err, db.UniqueViolation) || n != 0 {
			return fmt.Errorf("constraint failure count=%d: %w", n, err)
		}
		_, err := firstQuery.Update(ctx, map[string]any{"counter": 1})
		return err
	})
	if err != nil {
		t.Fatal("failed nested update poisoned caller transaction", err)
	}
	if count, err := query.Filter(orm.Q("name", "collision")).Count(ctx); err != nil || count != 0 {
		t.Fatal("partial failing update persisted", err)
	}
	for _, invalid := range []orm.Query[*models.MapRecord]{query.Limit(1), query.Offset(0), query.Distinct(), query.SelectRelated("unknown"), query.SelectForUpdate(false, false)} {
		if n, err := invalid.Update(ctx, map[string]any{"counter": 99}); err == nil || n != 0 {
			t.Fatal("unsupported update shape accepted", err)
		}
	}
	for _, fields := range []map[string]any{{"unknown": 1}, {"name__contains": "x"}, {"counter": orm.Sum(orm.F("counter"))}, {"counter": orm.Func("ROW_NUMBER")}, {"payload": make(chan int)}} {
		if n, err := query.Update(ctx, fields); err == nil || n != 0 {
			t.Fatal("invalid assignments accepted", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if n, err := query.Update(canceled, map[string]any{"counter": 99}); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatal("canceled update executed", err)
	}
	after, argsAfter, err := query.SQLContext(ctx)
	if err != nil || before != after || fmt.Sprint(argsBefore) != fmt.Sprint(argsAfter) {
		t.Fatal("Update mutated reusable query", err)
	}
}

func TestQueryUpdateConcurrentArithmeticDoesNotLoseIncrements(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "ConcurrentUpdate", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("value")}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	row := saveMap(t, store, schema, map[string]any{"value": 0})
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).Filter(orm.Q("id", mustValue(t, row, "id")))
	var workers sync.WaitGroup
	errorsChannel := make(chan error, 8)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 20; i++ {
				if n, err := query.Update(ctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(1))}); err != nil || n != 1 {
					errorsChannel <- fmt.Errorf("update count %d: %w", n, err)
					return
				}
			}
		}()
	}
	workers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
	fresh, err := query.Get(ctx)
	if err != nil || mustValue(t, fresh, "value") != int64(160) {
		t.Fatal("concurrent increment lost", err)
	}
}
