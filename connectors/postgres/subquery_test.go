package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestSubqueryNativeScopesPrecisionExistenceAndCardinality(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	parent := models.Schema{AppLabel: "tests", Name: "SubqueryParent", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.TextField("name")}}
	child := models.Schema{AppLabel: "tests", Name: "SubqueryChild", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("parent_id"), models.IntegerField("tenant"), models.BooleanField("visible"), models.DecimalField("amount", 30, 9)}}
	child.Fields[1].Column = "ancestor_key"
	for _, schema := range []models.Schema{parent, child} {
		if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(b, nil)
	a := saveMap(t, store, parent, map[string]any{"tenant": 1, "name": "visible"})
	empty := saveMap(t, store, parent, map[string]any{"tenant": 1, "name": "empty"})
	hidden := saveMap(t, store, parent, map[string]any{"tenant": 2, "name": "hidden"})
	aID, _ := a.Get("id")
	emptyID, _ := empty.Get("id")
	hiddenID, _ := hidden.Get("id")
	for _, values := range []map[string]any{
		{"parent_id": aID, "tenant": 1, "visible": true, "amount": "9007199254740993.000000001"},
		{"parent_id": aID, "tenant": 1, "visible": false, "amount": "99.000000000"},
		{"parent_id": aID, "tenant": 2, "visible": true, "amount": "88.000000000"},
		{"parent_id": hiddenID, "tenant": 1, "visible": true, "amount": "77.000000000"},
	} {
		saveMap(t, store, child, values)
	}
	counted := &countedBackend{Backend: b}
	store = orm.New(counted, nil)
	scope := func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }
	parents := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(parent); return row }).WithScope(scope)
	children := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(child); return row }).WithScope(scope).Filter(orm.Q("parent_id", orm.OuterRef("id")), orm.Q("visible", true))
	latest := orm.Subquery(children.OrderBy("-id").Limit(1), "amount")
	exists := orm.Exists(children)
	query := parents.Annotate(map[string]orm.ResultExpression{
		"latest":    orm.Typed(latest, models.DecimalField("latest", 30, 9, models.Nullable)),
		"has_child": orm.Typed(exists, models.BooleanField("has_child")),
	}).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 1 {
		t.Fatal("single scoped statement", len(rows), counted.queries.Load(), err)
	}
	if rows[0].ModelState().Annotations["latest"] != "9007199254740993.000000001" || rows[0].ModelState().Annotations["has_child"] != true || rows[1].ModelState().Annotations["latest"] != nil || rows[1].ModelState().Annotations["has_child"] != false {
		t.Fatal("typed scalar/existence", rows[0].ModelState().Annotations, rows[1].ModelState().Annotations)
	}
	matching, err := parents.Filter(db.Predicate{Expression: &exists, Value: true}).All(ctx)
	if err != nil || len(matching) != 1 {
		t.Fatal("EXISTS filter", len(matching), err)
	}
	got, _ := matching[0].Get("id")
	if got != aID {
		t.Fatal("wrong existence identity", got)
	}
	missing, err := parents.Filter(db.Predicate{Expression: &exists, Value: true, Negated: true}).All(ctx)
	if err != nil || len(missing) != 1 {
		t.Fatal("NOT EXISTS filter", len(missing), err)
	}
	got, _ = missing[0].Get("id")
	if got != emptyID {
		t.Fatal("wrong missing identity", got)
	}
	values, err := query.Values(ctx, "id", "latest")
	if err != nil || len(values) != 2 || values[0]["latest"] != "9007199254740993.000000001" || values[1]["latest"] != nil {
		t.Fatal("Values output", values, err)
	}
	if total, err := query.Count(ctx); err != nil || total != 2 {
		t.Fatal("outer scoped count", total, err)
	}
}

func TestSubqueryNativeSelfCorrelationAndCallerTransaction(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "SubquerySelf", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("value")}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, value := range []int{10, 20} {
		saveMap(t, store, schema, map[string]any{"tenant": 1, "value": value})
	}
	scope := func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }
	base := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(schema); return row }).WithScope(scope)
	previous := orm.Subquery(base.Filter(orm.Q("id__lt", orm.OuterRef("id"))).OrderBy("-id").Limit(1), "value")
	query := base.Annotate(map[string]orm.ResultExpression{"previous": orm.Typed(previous, models.IntegerField("previous", models.Nullable))}).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || rows[0].ModelState().Annotations["previous"] != nil || rows[1].ModelState().Annotations["previous"] != int64(10) {
		t.Fatal("self correlation", rows, err)
	}
	rollback := errors.New("rollback owned test transaction")
	err = db.Atomic(ctx, b, db.AtomicOptions{}, func(txctx context.Context) error {
		record, _ := models.NewRecord(schema)
		_ = record.Set("tenant", 1)
		_ = record.Set("value", 30)
		if err := store.Save(txctx, record, orm.SaveOptions{ForceInsert: true}); err != nil {
			return err
		}
		rows, err := query.All(txctx)
		if err != nil {
			return err
		}
		if len(rows) != 3 || rows[2].ModelState().Annotations["previous"] != int64(20) {
			t.Fatal("did not use caller transaction", rows)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal("caller transaction outcome", err)
	}
	if n, err := base.Count(ctx); err != nil || n != 2 {
		t.Fatal("rollback did not restore rows", n, err)
	}
}
