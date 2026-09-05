package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestJSONContainmentAndKeyLookupsRemainScopedAndTyped(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "JSONContainment", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.JSONField("payload", models.Nullable), models.TextField("probe")}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	key := `x') OR TRUE --`
	object := map[string]any{"owner": "Bob", "breed": "collie", "tricks": []string{"fetch", "dance"}, "nested": map[string]any{"number": json.Number("9007199254740993")}, key: true, "": true}
	values := []any{object, map[string]any{"owner": "Bob", "breed": "lab"}, map[string]any{}, models.JSONNull, nil, []string{"owner", "dance"}, "owner", object}
	ids := make([]any, len(values))
	for i, value := range values {
		tenant := 1
		if i == len(values)-1 {
			tenant = 2
		}
		record := saveMap(t, store, schema, map[string]any{"tenant": tenant, "payload": value, "probe": "owner"})
		ids[i] = mustValue(t, record, "id")
	}
	scope := func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).WithScope(scope).OrderBy("id")
	for _, test := range []struct {
		name  string
		where db.Predicate
		want  []any
	}{
		{"contains_object", orm.Q("payload__contains", map[string]any{"owner": "Bob"}), ids[:2]},
		{"contains_nested_array", orm.Q("payload__contains", map[string]any{"tricks": []string{"dance"}}), ids[:1]},
		{"contains_exact_number", orm.Q("payload__contains", map[string]any{"nested": map[string]any{"number": json.Number("9007199254740993")}}), ids[:1]},
		{"different_exact_number", orm.Q("payload__contains", map[string]any{"nested": map[string]any{"number": json.Number("9007199254740992")}}), []any{}},
		{"contained_by", orm.Q("payload__contained_by", map[string]any{"owner": "Bob", "breed": "lab", "extra": true}), ids[1:3]},
		{"contains_array", orm.Q("payload__contains", []string{"dance"}), ids[5:6]},
		{"contains_native_string", orm.Q("payload__contains", "owner"), ids[5:7]},
		{"contains_json_null", orm.Q("payload__contains", models.JSONNull), ids[3:4]},
		{"has_key", orm.Q("payload__has_key", "owner"), []any{ids[0], ids[1], ids[5], ids[6]}},
		{"has_all_keys", orm.Q("payload__has_keys", []string{"owner", "breed"}), ids[:2]},
		{"has_any_keys", orm.Q("payload__has_any_keys", []string{"breed", "not_present"}), ids[:2]},
		{"has_empty_key", orm.Q("payload__has_key", ""), ids[:1]},
		{"has_bound_key", orm.Q("payload__has_key", key), ids[:1]},
		{"has_no_keys", orm.Q("payload__has_any_keys", []string{}), []any{}},
		{"all_empty_keys", orm.Q("payload__has_keys", []string{}), []any{ids[0], ids[1], ids[2], ids[3], ids[5], ids[6]}},
		{"has_key_expression", orm.Q("payload__has_key", orm.F("probe")), []any{ids[0], ids[1], ids[5], ids[6]}},
		{"json_lhs_expression", db.Predicate{Expression: expressionPointer(orm.F("payload")), Lookup: "contains", Value: map[string]any{"owner": "Bob"}}, ids[:2]},
		{"self_containment_expression", orm.Q("payload__contains", orm.F("payload")), []any{ids[0], ids[1], ids[2], ids[3], ids[5], ids[6]}},
		{"combined_keys", orm.And(orm.Q("payload__has_key", "owner"), orm.Not(orm.Q("payload__has_key", "breed"))), ids[5:7]},
		{"text_contains_unaffected", orm.Q("probe__contains", "wn"), ids[:7]},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := query.Filter(test.where)
			statement, _, err := selected.SQL()
			if err != nil || strings.Contains(statement, key) || strings.Contains(statement, "9007199254740993") {
				t.Fatal("lookup values interpolated into SQL", statement, err)
			}
			rows, err := selected.All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]any, len(rows))
			for i, record := range rows {
				got[i] = mustValue(t, record, "id")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatal("JSON lookup returned incorrect scoped identities", got, test.want)
			}
		})
	}
	keys := []string{"owner", "breed"}
	snapshot := query.Filter(orm.Q("payload__has_keys", keys))
	keys[0] = "not_present"
	if count, err := snapshot.Count(ctx); err != nil || count != 2 {
		t.Fatal("JSON key list was not snapshotted", count, err)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	for _, invalid := range []db.Predicate{orm.Q("payload__contains", nil), orm.Q("payload__contains", json.RawMessage(`{"invalid":`)), orm.Q("payload__has_key", 1), orm.Q("payload__has_keys", []any{"owner", 2}), orm.Q("probe__has_key", "owner")} {
		if _, err := query.Filter(invalid).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid JSON lookup reached SQL", err, counted.queries.Load())
		}
	}
}
