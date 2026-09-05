package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestJSONPathQueriesDistinguishMissingNullAndNativeScalarTypes(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "JSONPath", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.JSONField("payload", models.Nullable)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	key := `unusual__key') OR TRUE --`
	object := map[string]any{"owner": map[string]any{"name": "Bob", "pets": []any{map[string]any{"name": "Fishy"}, map[string]any{"name": "Milo"}}}, "number": json.Number("9007199254740993"), "nothing": nil, key: true, "has_key": "literal"}
	values := []any{object, map[string]any{"owner": map[string]any{"name": "Alice"}, "number": json.Number("9007199254740992"), "nothing": "null"}, map[string]any{}, nil, models.JSONNull, []any{1, 2, 3}, object}
	ids := make([]any, len(values))
	for i, value := range values {
		tenant := 1
		if i == len(values)-1 {
			tenant = 2
		}
		record := saveMap(t, store, schema, map[string]any{"tenant": tenant, "payload": value})
		ids[i] = mustValue(t, record, "id")
	}
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }).OrderBy("id")
	for _, test := range []struct {
		name  string
		where db.Predicate
		want  []any
	}{
		{"nested_exact", orm.Q("payload__owner__name", "Bob"), ids[:1]},
		{"nested_iexact", orm.Q("payload__owner__name__iexact", "bOB"), ids[:1]},
		{"nested_icontains", orm.Q("payload__owner__name__icontains", "OB"), ids[:1]},
		{"nested_regex", orm.Q("payload__owner__name__regex", "^B.b$"), ids[:1]},
		{"array_index", orm.Q("payload__owner__pets__0__name", "Fishy"), ids[:1]},
		{"negative_array_index", orm.Q("payload__owner__pets__-1__name", "Milo"), ids[:1]},
		{"root_array_index", orm.Q("payload__0", 1), ids[5:6]},
		{"root_negative_index", orm.Q("payload__-1", 3), ids[5:6]},
		{"json_null", orm.Q("payload__nothing", nil), ids[:1]},
		{"json_string_null", orm.Q("payload__nothing", "null"), ids[1:2]},
		{"missing_not_json_null", orm.Q("payload__nothing__isnull", true), ids[2:6]},
		{"existing_even_if_json_null", orm.Q("payload__nothing__isnull", false), ids[:2]},
		{"numeric_gte_exact", orm.Q("payload__number__gte", json.Number("9007199254740993")), ids[:1]},
		{"nested_contains", orm.Q("payload__owner__contains", map[string]any{"name": "Bob"}), ids[:1]},
		{"nested_has_key", orm.Q("payload__owner__has_key", "pets"), ids[:1]},
		{"explicit_literal_key", db.Predicate{Expression: expressionPointer(orm.JSONPath("payload", key)), Value: true}, ids[:1]},
		{"lookup_name_as_key", db.Predicate{Expression: expressionPointer(orm.JSONPath("payload", "has_key")), Value: "literal"}, ids[:1]},
		{"explicit_text_path", db.Predicate{Expression: expressionPointer(orm.JSONTextPath("payload", "owner", "name")), Lookup: "startswith", Value: "Bo"}, ids[:1]},
		{"text_null_is_sql_null", db.Predicate{Expression: expressionPointer(orm.JSONTextPath("payload", "nothing")), Lookup: "isnull", Value: true}, []any{ids[0], ids[2], ids[3], ids[4], ids[5]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := query.Filter(test.where).All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]any, len(rows))
			for i, record := range rows {
				got[i] = mustValue(t, record, "id")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatal("JSON path returned wrong scoped identities", got, test.want)
			}
		})
	}
	projected, err := query.Values(ctx, "id", "payload__number", "payload__nothing", "payload__owner__name")
	if err != nil || len(projected) != 6 {
		t.Fatal(projected, err)
	}
	if projected[0]["payload__number"] != json.Number("9007199254740993") || projected[0]["payload__nothing"] != models.JSONNull || projected[1]["payload__nothing"] != "null" || projected[2]["payload__nothing"] != nil || projected[0]["payload__owner__name"] != "Bob" {
		t.Fatal("Values lost JSON path scalar/null types", projected)
	}
	ordered, err := query.Filter(orm.Q("payload__number__isnull", false)).OrderBy("payload__number").Values(ctx, "id")
	if err != nil || len(ordered) != 2 || ordered[0]["id"] != ids[1] || ordered[1]["id"] != ids[0] {
		t.Fatal("JSON number ordering changed precision", ordered, err)
	}
	counts, err := query.Aggregate(ctx, map[string]orm.ResultExpression{
		"json": orm.Typed(orm.Count(orm.JSONPath("payload", "nothing")), models.BigIntegerField("result")),
		"text": orm.Typed(orm.Count(orm.JSONTextPath("payload", "nothing")), models.BigIntegerField("result")),
	})
	if err != nil || counts["json"] != int64(2) || counts["text"] != int64(1) {
		t.Fatal("JSON and text path aggregate null semantics collapsed", counts, err)
	}
	components := []string{"owner", "name"}
	expression := orm.JSONPath("payload", components...)
	components[0] = "not_present"
	snapshot := query.Filter(db.Predicate{Expression: &expression, Value: "Bob"})
	expression.Value.([]string)[0] = "not_present"
	if count, err := snapshot.Count(ctx); err != nil || count != 1 {
		t.Fatal("explicit JSON path was not snapshotted", count, err)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	for _, invalid := range []db.Predicate{orm.Q("payload__2147483648", 1), orm.Q("payload__number__gt", nil), orm.Q("tenant__not_a_json_key", 1), {Expression: expressionPointer(orm.JSONPath("tenant", "key")), Value: 1}} {
		if _, err := query.Filter(invalid).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid JSON path reached SQL", err, counted.queries.Load())
		}
	}
}

func TestJSONPathsOnSelectedRelationsRetainScopeAndParameterOrder(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "JSONTarget", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.JSONField("payload")}}
	source := models.Schema{AppLabel: "tests", Name: "JSONSource", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), OnDelete: models.Protect}, models.Nullable)}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{target, source} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(source)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	visible := saveMap(t, store, target, map[string]any{"tenant": 2, "payload": map[string]any{"secret": "visible"}})
	hidden := saveMap(t, store, target, map[string]any{"tenant": 3, "payload": map[string]any{"secret": "hidden"}})
	first := saveMap(t, store, source, map[string]any{"tenant": 1, "target": mustValue(t, visible, "id")})
	second := saveMap(t, store, source, map[string]any{"tenant": 1, "target": mustValue(t, hidden, "id")})
	saveMap(t, store, source, map[string]any{"tenant": 4, "target": mustValue(t, visible, "id")})
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(source); return record }).SelectRelated("target").WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == target.Key() {
			return orm.Q("tenant", 2), nil
		}
		return orm.Q("tenant", 1), nil
	}).OrderBy("id")
	selected := query.Filter(orm.Q("target__payload__secret", "visible"))
	_, args, err := selected.SQLContext(ctx)
	if err != nil || !reflect.DeepEqual(args, []any{2, "secret", `"visible"`, 1}) {
		t.Fatal("joined JSON path argument ordering changed", args, err)
	}
	row, err := selected.Get(ctx)
	if err != nil || mustValue(t, row, "id") != mustValue(t, first, "id") {
		t.Fatal("joined path did not select the authorized target", err)
	}
	if count, err := query.Filter(orm.Q("target__payload__secret", "hidden")).Count(ctx); err != nil || count != 0 {
		t.Fatal("joined JSON lookup exposed a hidden target", count, err)
	}
	row, err = query.Filter(orm.Q("target__payload__secret__isnull", true)).Get(ctx)
	if err != nil || mustValue(t, row, "id") != mustValue(t, second, "id") {
		t.Fatal("scoped-out target was not represented as missing", err)
	}
	counts, err := query.Aggregate(ctx, map[string]orm.ResultExpression{
		"readable": orm.Typed(orm.Count(orm.JSONPath("target__payload", "secret")), models.BigIntegerField("result")),
	})
	if err != nil || counts["readable"] != int64(1) {
		t.Fatal("projection JSON path parameters displaced joined/root scopes", counts, err)
	}
}
