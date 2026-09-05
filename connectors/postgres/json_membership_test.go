package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestJSONMembershipAndRangePreserveNativeRHSValues(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	store := orm.New(b, nil)
	for _, nested := range []bool{false, true} {
		name, field := "RootMembership", "payload"
		if nested {
			name, field = "PathMembership", "payload__value"
		}
		t.Run(name, func(t *testing.T) {
			schema := models.Schema{AppLabel: "tests", Name: name, Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload", models.Nullable)}}
			if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			object := map[string]any{"number": json.Number("9007199254740993")}
			values := []any{"null", models.JSONNull, nil, json.Number("9007199254740993"), object, true, []any{"a"}, json.Number("9007199254740992")}
			ids := make([]any, len(values))
			for i, value := range values {
				if nested {
					if value == nil {
						value = map[string]any{}
					} else {
						value = map[string]any{"value": value}
					}
				}
				record := saveMap(t, store, schema, map[string]any{"payload": value})
				ids[i] = mustValue(t, record, "id")
			}
			query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).OrderBy("id")
			for _, test := range []struct {
				name  string
				where db.Predicate
				want  []any
			}{
				{"native_string", orm.Q(field+"__in", []any{"null"}), ids[:1]},
				{"explicit_json_null", orm.Q(field+"__in", []any{models.JSONNull}), ids[1:2]},
				{"raw_json_null", orm.Q(field+"__in", []any{json.RawMessage(`null`)}), ids[1:2]},
				{"nil_skipped", orm.Q(field+"__in", []any{nil, "null"}), ids[:1]},
				{"nil_only", orm.Q(field+"__in", []any{nil}), []any{}},
				{"empty", orm.Q(field+"__in", []any{}), []any{}},
				{"negated_empty", orm.Not(orm.Q(field+"__in", []any{})), ids},
				{"negated_nil_only", orm.Not(orm.Q(field+"__in", []any{nil})), ids},
				{"precise_number", orm.Q(field+"__in", []any{json.Number("9007199254740993")}), ids[3:4]},
				{"object", orm.Q(field+"__in", []any{object}), ids[4:5]},
				{"boolean", orm.Q(field+"__in", []any{true}), ids[5:6]},
				{"array", orm.Q(field+"__in", []any{[]any{"a"}}), ids[6:7]},
				{"expression_rhs", orm.Q(field+"__in", []any{orm.F(field)}), []any{ids[0], ids[1], ids[3], ids[4], ids[5], ids[6], ids[7]}},
				{"number_range", orm.Q(field+"__range", []any{json.Number("9007199254740992"), json.Number("9007199254740993")}), []any{ids[3], ids[7]}},
				{"string_range", orm.Q(field+"__range", []any{"null", "null"}), ids[:1]},
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
						t.Fatal("JSON membership/range changed literal types", got, test.want)
					}
				})
			}
		})
	}
}
