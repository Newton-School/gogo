package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type subqueryDialect struct {
	updateDialect
	supported bool
}

func (d subqueryDialect) SupportsFeature(name string) bool {
	return name == "correlated_subqueries" && d.supported
}

func (subqueryDialect) CastExpression(value string, field models.Field) (string, error) {
	switch field.Kind {
	case models.JSON:
		return "CAST(" + value + " AS jsonb)", nil
	case models.Text:
		return "CAST(" + value + " AS text)", nil
	}
	return "", errors.New("test dialect does not implement this cast")
}

type subqueryBackend struct {
	*updateBackend
	aliases   int
	alias     string
	supported bool
}

func (b *subqueryBackend) Alias() string       { b.aliases++; return b.alias }
func (b *subqueryBackend) Dialect() db.Dialect { return subqueryDialect{supported: b.supported} }

func subqueryFixture() (*subqueryBackend, *Store, Query[*models.MapRecord]) {
	backend := &subqueryBackend{updateBackend: &updateBackend{caps: db.Capabilities{"correlated_subqueries": true, "transactions": true, "update_matched_rows": true}, maximum: 500}, alias: "default", supported: true}
	store := New(backend, nil)
	schema := models.Schema{AppLabel: "test", Name: "SubqueryRow", Fields: []models.Field{
		models.BigAutoField("id"), models.BigIntegerField("parent_id"), models.DecimalField("amount", 20, 2), models.BooleanField("visible"), models.JSONField("payload"),
	}}
	schema.Fields[1].Column = "parent_link"
	query := For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record })
	return backend, store, query
}

func TestSubqueryLazyScopesTypedSQLAndCopies(t *testing.T) {
	backend, _, outer := subqueryFixture()
	outerCalls, innerCalls := 0, 0
	inner := outer.Filter(Q("parent_id", OuterRef("id"))).OrderBy("-id").Limit(1).WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
		innerCalls++
		return Q("visible", true), nil
	})
	expression := Subquery(inner, "amount")
	outer = outer.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { outerCalls++; return Q("id__gt", 7), nil }).Annotate(map[string]ResultExpression{
		"latest": Typed(expression, models.DecimalField("latest", 20, 2, models.Nullable)),
	})
	expression.Output.Kind = models.Text
	if backend.aliases != 0 || outerCalls != 0 || innerCalls != 0 {
		t.Fatal("construction invoked a provider")
	}
	statement, args, err := outer.SQLContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`FROM "test_subqueryrow" AS "gogo_outer"`, `"gogo_subquery_1"."parent_link" = "gogo_outer"."id"`, `ORDER BY "gogo_subquery_1"."id" DESC`, `LIMIT $2`} {
		if !strings.Contains(statement, part) {
			t.Fatalf("missing %s in %s", part, statement)
		}
	}
	if !reflect.DeepEqual(args, []any{true, 1, 7}) || outerCalls != 1 || innerCalls != 1 {
		t.Fatalf("args=%#v scopes=%d/%d", args, outerCalls, innerCalls)
	}
	if backend.begins != 0 {
		t.Fatal("SQL construction started a transaction")
	}
	if _, _, err := outer.SQLContext(context.Background()); err != nil || innerCalls != 2 || outerCalls != 2 {
		t.Fatal("operation shared its prepared scope", err, innerCalls, outerCalls)
	}
}

func TestSubqueryExistsKeepsSliceAndDiscardsProjectionOrder(t *testing.T) {
	_, _, outer := subqueryFixture()
	expression := Exists(outer.Filter(Q("parent_id", OuterRef("id"))).Only("payload").OrderBy("unknown").Offset(2).Limit(0))
	query := outer.Filter(db.Predicate{Expression: &expression, Value: false})
	statement, args, err := query.SQLContext(context.Background())
	if err != nil || !strings.Contains(statement, "EXISTS (SELECT 1") || strings.Contains(statement, "ORDER BY") || !reflect.DeepEqual(args, []any{0, 2, false}) {
		t.Fatal(statement, args, err)
	}
}

func TestSubqueryRejectsUnsupportedBoundariesBeforeDatabase(t *testing.T) {
	for _, mode := range []string{"missing_limit", "missing_inner_scope", "different_backend", "unknown_outer", "outside_outer", "grouped", "window", "lock", "nested", "raw", "parameter", "missing_capability", "missing_dialect", "negative_offset", "nonnull_output"} {
		t.Run(mode, func(t *testing.T) {
			backend, _, query := subqueryFixture()
			inner := query.Filter(Q("parent_id", OuterRef("id"))).Limit(1)
			switch mode {
			case "missing_limit":
				inner.selectAST.Limit = nil
			case "missing_inner_scope":
				query = query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return Q("visible", true), nil })
			case "different_backend":
				_, _, inner = subqueryFixture()
				inner = inner.Limit(1)
			case "unknown_outer":
				inner = inner.Filter(Q("parent_id", OuterRef("unknown")))
			case "nested":
				nested := Exists(query)
				inner = inner.Filter(db.Predicate{Expression: &nested, Value: true})
			case "missing_capability":
				delete(backend.caps, "correlated_subqueries")
			case "missing_dialect":
				backend.supported = false
			case "negative_offset":
				inner = inner.Offset(-1)
			}
			expression := Subquery(inner, "amount")
			output := models.DecimalField("latest", 20, 2, models.Nullable)
			if mode == "nonnull_output" {
				output.Null = false
			}
			query = query.Annotate(map[string]ResultExpression{"latest": Typed(expression, output)})
			switch mode {
			case "outside_outer":
				query = query.Filter(Q("id", OuterRef("id")))
			case "grouped":
				query = query.GroupBy("id")
			case "window":
				query = query.Annotate(map[string]ResultExpression{"rownum": Typed(Window(RowNumber(), db.WindowSpec{}), models.BigIntegerField("rownum"))})
			case "lock":
				query = query.SelectForUpdate(false, false)
			case "raw":
				query = query.Filter(Q("id", db.Expression{Kind: "subquery", Subquery: &db.Subquery{}}))
			case "parameter":
				query = query.Filter(Q("payload", map[string]any{"nested": expression}))
			}
			if _, _, err := query.SQLContext(context.Background()); err == nil {
				t.Fatal("unsupported subquery accepted")
			}
			if backend.begins != 0 {
				t.Fatal("unsupported SQL began a transaction")
			}
		})
	}
}

type subqueryMutationContext struct {
	context.Context
	mutate func()
}

func (c *subqueryMutationContext) Err() error {
	if c.mutate != nil {
		f := c.mutate
		c.mutate = nil
		f()
	}
	return c.Context.Err()
}

func TestSubqueryFreezesStoreBeforeContextAndScopeCallbacks(t *testing.T) {
	backend, store, query := subqueryFixture()
	inner := query.Filter(Q("parent_id", OuterRef("id"))).Limit(1)
	expression := Subquery(inner, "amount")
	query = query.Annotate(map[string]ResultExpression{"latest": Typed(expression, models.DecimalField("latest", 20, 2, models.Nullable))})
	ctx := &subqueryMutationContext{Context: context.Background(), mutate: func() { *store = Store{}; backend.alias = "retargeted" }}
	statement, _, err := query.SQLContext(ctx)
	if err != nil || !strings.Contains(statement, "SELECT") {
		t.Fatal(statement, err)
	}
}

func TestSubqueryRejectsMutationAndCancellation(t *testing.T) {
	backend, _, query := subqueryFixture()
	expression := Exists(query)
	if _, err := query.Update(context.Background(), map[string]any{"visible": expression}); err == nil || backend.begins != 0 {
		t.Fatal("expression mutation accepted", err)
	}
	if _, err := query.Update(context.Background(), map[string]any{"payload": map[string]any{"query": expression}}); err == nil || backend.begins != 0 {
		t.Fatal("expression became JSON", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inner := query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { cancel(); return Q("visible", true), nil })
	expression = Exists(inner)
	_, _, err := query.Filter(db.Predicate{Expression: &expression, Value: true}).SQLContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestSubqueryCannotHideInExportedDataBesidePrivateFields(t *testing.T) {
	backend, _, query := subqueryFixture()
	type mixedPayload struct {
		Expression db.Expression
		private    int
	}
	payload := mixedPayload{Expression: Exists(query), private: 1}
	for _, value := range []any{payload, &payload, map[string]any{"payload": payload}, map[string]any{"payload": &payload}, Value(&payload)} {
		if _, err := query.Update(context.Background(), map[string]any{"payload": value}); err == nil || backend.begins != 0 {
			t.Fatalf("lazy expression reached JSON/SQL via %T: %v", value, err)
		}
		if _, _, err := query.Filter(Q("payload", value)).SQLContext(context.Background()); err == nil {
			t.Fatalf("lazy expression reached read parameter via %T", value)
		}
	}
}

func TestSubqueryCannotHideInPromotedFieldsOfPrivateEmbeddings(t *testing.T) {
	backend, _, query := subqueryFixture()
	type embeddedPayload struct{ Expression db.Expression }
	type valueWrapper struct{ embeddedPayload }
	type pointerWrapper struct{ *embeddedPayload }
	payload := embeddedPayload{Expression: Exists(query)}
	for _, value := range []any{valueWrapper{payload}, &valueWrapper{payload}, pointerWrapper{&payload}, &pointerWrapper{&payload}, map[string]any{"wrapper": pointerWrapper{&payload}}} {
		if _, err := query.Update(context.Background(), map[string]any{"payload": value}); err == nil || backend.begins != 0 {
			t.Fatalf("promoted expression reached JSON/SQL via %T: %v", value, err)
		}
		if _, _, err := query.Filter(Q("payload", value)).SQLContext(context.Background()); err == nil {
			t.Fatalf("promoted expression reached read parameter via %T", value)
		}
	}
	type embeddedScalar struct{ Value string }
	type scalarWrapper struct{ *embeddedScalar }
	if _, _, err := query.Filter(Q("payload", scalarWrapper{&embeddedScalar{Value: "ordinary"}})).SQLContext(context.Background()); err != nil {
		t.Fatal("ordinary promoted data rejected", err)
	}
}

type subqueryAnnotationCodec struct{ calls *int }

func (c subqueryAnnotationCodec) Encode(any) (any, error)     { *c.calls++; return "encoded", nil }
func (subqueryAnnotationCodec) Decode(value any) (any, error) { return value, nil }

type subqueryParameterOwnershipCodec struct{ encode func(any) }

func (c subqueryParameterOwnershipCodec) Encode(value any) (any, error) {
	c.encode(value)
	return "encoded", nil
}
func (subqueryParameterOwnershipCodec) Decode(value any) (any, error) { return value, nil }

func TestSubqueryParameterScanDoesNotRewriteOpaqueFilterOrCaseData(t *testing.T) {
	for _, shape := range []string{"filter", "case", "denial_after_filter"} {
		t.Run(shape, func(t *testing.T) {
			_, _, query := subqueryFixture()
			members := []db.Expression{Value(1), Value(2)}
			predicate := db.Predicate{Field: "id", Lookup: "in", Value: members}
			ordinary := db.Expression{Kind: "value", Filter: &predicate}
			if shape == "case" {
				ordinary.Filter = nil
				ordinary.Branches = []db.WhenBranch{{Condition: predicate, Then: Value(3)}}
			}
			if shape == "denial_after_filter" {
				hidden := Exists(query)
				ordinary.Branches = []db.WhenBranch{{Condition: db.Predicate{Expression: &hidden, Value: true}, Then: Value(3)}}
			}
			// Query cloning intentionally preserves opaque provider values. The
			// expression scan must not rewrite their exported nested data either.
			type opaque struct {
				Expression db.Expression
				private    int
			}
			original := &opaque{Expression: ordinary, private: 7}
			check := func(value any) {
				payload, ok := value.(*opaque)
				if !ok || payload != original || payload.private != 7 {
					t.Fatal("opaque parameter identity/type changed")
				}
				actual := payload.Expression.Filter
				if shape == "case" {
					actual = &payload.Expression.Branches[0].Condition
				}
				got, ok := actual.Value.([]db.Expression)
				if !ok || !reflect.DeepEqual(got, members) {
					t.Errorf("parameter membership type/data changed: %T %#v", actual.Value, actual.Value)
				}
			}
			calls := 0
			output := models.JSONField("opaque")
			output.Codec = subqueryParameterOwnershipCodec{encode: func(value any) { calls++; check(value) }}
			query = query.Annotate(map[string]ResultExpression{"opaque": Typed(Value(original), output)})
			_, _, err := query.SQLContext(context.Background())
			check(original)
			if shape == "denial_after_filter" {
				if err == nil || calls != 0 {
					t.Fatal("hidden query reached codec", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatal("ordinary data not encoded once", calls, err)
			}
		})
	}
}

func TestSubqueryParameterOutputMetadataCannotHideQueryNodes(t *testing.T) {
	for _, shape := range []string{"default", "min", "max", "choice", "element"} {
		t.Run(shape, func(t *testing.T) {
			_, _, query := subqueryFixture()
			hidden := Exists(query)
			metadata := models.JSONField("data")
			switch shape {
			case "default":
				metadata.Default = hidden
			case "min":
				metadata.Min = hidden
			case "max":
				metadata.Max = hidden
			case "choice":
				metadata.Choices = []models.Choice{{Value: hidden}}
			case "element":
				metadata.Element = &models.Field{Default: hidden}
			}
			payload := db.Expression{Kind: "value", Output: &metadata}
			calls := 0
			output := models.JSONField("payload_data")
			output.Codec = subqueryParameterOwnershipCodec{encode: func(any) { calls++ }}
			query = query.Annotate(map[string]ResultExpression{"payload_data": Typed(Value(payload), output)})
			_, _, err := query.SQLContext(context.Background())
			if err == nil || calls != 0 {
				t.Fatal("query-bearing output metadata reached codec", calls, err)
			}
		})
	}
}

func TestSubqueryExtractedCarrierCannotReachParameterEncoders(t *testing.T) {
	for _, shape := range []string{"direct", "pointer", "container", "output"} {
		t.Run(shape, func(t *testing.T) {
			backend, _, query := subqueryFixture()
			carrier := Exists(query).Value
			var payload any = carrier
			switch shape {
			case "pointer":
				payload = &carrier
			case "container":
				payload = map[string]any{"carrier": []any{&carrier}}
			case "output":
				payload = map[string]any{"expression": db.Expression{Kind: "value", Output: &models.Field{Default: carrier}}}
			}
			calls := 0
			output := models.JSONField("carrier_data")
			output.Codec = subqueryParameterOwnershipCodec{encode: func(any) { calls++ }}
			annotated := query.Annotate(map[string]ResultExpression{"carrier_data": Typed(Value(payload), output)})
			if _, _, err := annotated.SQLContext(context.Background()); err == nil || calls != 0 {
				t.Error("private carrier reached annotation codec", calls, err)
			}
			if _, _, err := query.Filter(Q("payload", payload)).SQLContext(context.Background()); err == nil {
				t.Error("private carrier reached predicate encoder")
			}
			if _, err := query.Update(context.Background(), map[string]any{"payload": payload}); err == nil || backend.begins != 0 {
				t.Error("private carrier reached mutation encoder", backend.begins, err)
			}
		})
	}
}

func TestSubqueryParameterScanPreservesOrdinaryOutputMetadata(t *testing.T) {
	_, _, query := subqueryFixture()
	metadata := models.Field{Default: map[string]any{"ordinary": 1}, Min: 2, Max: 3, Choices: []models.Choice{{Value: 4}}, Element: &models.Field{Default: "ordinary"}}
	payload := db.Expression{Kind: "value", Output: &metadata}
	parameter := map[string]any{"expression": payload}
	encoded, err := json.Marshal(parameter)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	output := models.JSONField("metadata_data")
	output.Codec = subqueryParameterOwnershipCodec{encode: func(value any) {
		calls++
		actual, ok := value.(db.Expression)
		if !ok || !reflect.DeepEqual(actual, payload) {
			t.Fatal("ordinary metadata type/data changed", value)
		}
	}}
	annotated := query.Annotate(map[string]ResultExpression{"metadata_data": Typed(Value(payload), output)})
	if _, _, err := annotated.SQLContext(context.Background()); err != nil || calls != 1 {
		t.Fatal("ordinary metadata codec rejected", calls, err)
	}
	_, args, err := query.Filter(Q("payload", parameter)).SQLContext(context.Background())
	if err != nil || len(args) != 1 || args[0] != string(encoded) {
		t.Fatal("ordinary metadata JSON changed", err)
	}
}

func TestSubqueryParameterRejectionPrecedesAnnotationEncoding(t *testing.T) {
	for _, terminal := range []string{"sql", "values", "all"} {
		for _, shape := range []string{"map", "pointer", "nested_expression", "struct", "filter", "filter_value", "filter_membership", "case_condition"} {
			for _, custom := range []bool{false, true} {
				t.Run(terminal+"/"+shape+"/"+map[bool]string{false: "json", true: "codec"}[custom], func(t *testing.T) {
					_, _, query := subqueryFixture()
					expression := Exists(query)
					var value any = map[string]any{"expr": expression}
					switch shape {
					case "pointer":
						value = map[string]any{"expr": &expression}
					case "nested_expression":
						nested := Value(map[string]any{"expr": expression})
						value = &nested
					case "filter":
						value = db.Expression{Kind: "value", Filter: &db.Predicate{Expression: &expression, Value: true}}
					case "filter_value":
						value = db.Expression{Kind: "value", Filter: &db.Predicate{Children: []db.Predicate{{Field: "visible", Value: expression}}}}
					case "filter_membership":
						value = db.Expression{Kind: "value", Filter: &db.Predicate{Children: []db.Predicate{{Field: "visible", Lookup: "in", Value: []any{false, expression}}}}}
					case "case_condition":
						value = db.Expression{Kind: "value", Branches: []db.WhenBranch{{Condition: db.Predicate{Expression: &expression, Value: true}, Then: Value(1)}}}
					case "struct":
						value = struct {
							Expression db.Expression
							private    int
						}{Expression: expression}
					}
					calls := 0
					output := models.JSONField("encoded")
					if custom {
						output.Codec = subqueryAnnotationCodec{calls: &calls}
					}
					query = query.Annotate(map[string]ResultExpression{"encoded": Typed(Value(value), output)})
					var err error
					switch terminal {
					case "sql":
						_, _, err = query.SQLContext(context.Background())
					case "values":
						_, err = query.Values(context.Background(), "encoded")
					case "all":
						_, err = query.All(context.Background())
					}
					if err == nil || calls != 0 {
						t.Fatal("query expression encoded before rejection", calls, err)
					}
				})
			}
		}
	}
}

func TestSubqueryScanPreservesLargeOrdinaryJSONParameters(t *testing.T) {
	value := make(map[string]any, 5000)
	for i := 0; i < 5000; i++ {
		value[fmt.Sprintf("key_%04d", i)] = i
	}
	want, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, annotation := range []bool{false, true} {
		t.Run(fmt.Sprintf("annotation=%t", annotation), func(t *testing.T) {
			_, _, query := subqueryFixture()
			if annotation {
				query = query.Annotate(map[string]ResultExpression{"large": Typed(Value(value), models.JSONField("large"))})
			} else {
				query = query.Filter(Q("payload", value))
			}
			_, args, err := query.SQLContext(context.Background())
			if err != nil || len(args) != 1 || args[0] != string(want) {
				t.Fatalf("ordinary JSON changed: argument count=%d error=%v", len(args), err)
			}
		})
	}
}

func TestSubqueryScanLeavesOrdinaryJSONCycleRejectionToEncoder(t *testing.T) {
	value := map[string]any{}
	value["self"] = value
	for _, annotation := range []bool{false, true} {
		_, _, query := subqueryFixture()
		if annotation {
			query = query.Annotate(map[string]ResultExpression{"cycle": Typed(Value(value), models.JSONField("cycle"))})
		} else {
			query = query.Filter(Q("payload", value))
		}
		_, _, err := query.SQLContext(context.Background())
		if annotation {
			// Annotation encoding deliberately exposes only a stable error;
			// predicate encoding retains the original JSON error below.
			if err == nil || err.Error() != "orm: annotation cycle cannot encode its literal" {
				t.Fatal("ordinary cycle no longer reaches annotation encoder", err)
			}
			continue
		}
		var encodingError *json.UnsupportedValueError
		if !errors.As(err, &encodingError) {
			t.Fatal("ordinary cycle no longer reaches JSON encoder", annotation, err)
		}
	}
}

func TestSubquerySQLPlacementIgnoresOrdinaryExpressionShapedJSON(t *testing.T) {
	for _, expression := range []db.Expression{{Kind: "function", Name: "COUNT"}, {Kind: "window"}, {Kind: "invalid_tree"}} {
		t.Run(expression.Kind, func(t *testing.T) {
			_, _, query := subqueryFixture()
			payload := map[string]any{"ordinary": expression}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			query = query.Filter(Q("payload", payload)).Annotate(map[string]ResultExpression{
				"exists": Typed(Exists(query), models.BooleanField("exists")),
			})
			_, args, err := query.SQLContext(context.Background())
			if err != nil || len(args) != 1 || args[0] != string(encoded) {
				t.Fatalf("JSON data treated as a SQL owner: args=%d error=%v", len(args), err)
			}
		})
	}
}

type subqueryAliasingBackend struct {
	*subqueryBackend
	values []string
}

func (b *subqueryAliasingBackend) Alias() string {
	value := b.values[0]
	if len(b.values) > 1 {
		b.values = b.values[1:]
	}
	return value
}

type subqueryNoncomparableBackend struct {
	*subqueryBackend
	values []string
}

func TestSubqueryCapturedBackendIdentityAndAliases(t *testing.T) {
	for _, mode := range []string{"mismatched_alias", "empty_alias", "empty_inner_alias", "noncomparable", "typed_nil_outer", "typed_nil_inner"} {
		t.Run(mode, func(t *testing.T) {
			backend, store, query := subqueryFixture()
			switch mode {
			case "mismatched_alias":
				store.Backend = &subqueryAliasingBackend{subqueryBackend: backend, values: []string{"outer", "inner"}}
			case "empty_alias":
				store.Backend = &subqueryAliasingBackend{subqueryBackend: backend, values: []string{"", ""}}
			case "empty_inner_alias":
				store.Backend = &subqueryAliasingBackend{subqueryBackend: backend, values: []string{"outer", ""}}
			case "noncomparable":
				store.Backend = subqueryNoncomparableBackend{subqueryBackend: backend, values: []string{"ordinary"}}
			case "typed_nil_inner":
				store.Backend = (*subqueryBackend)(nil)
			}
			expression := Exists(query)
			if mode == "typed_nil_inner" {
				store.Backend = backend
			}
			if mode == "typed_nil_outer" {
				store.Backend = (*subqueryBackend)(nil)
			}
			_, args, err := query.Filter(db.Predicate{Expression: &expression, Value: true}).SQLContext(context.Background())
			if err == nil || len(args) != 0 || backend.begins != 0 {
				t.Fatal("invalid backend selection accepted", args, backend.begins, err)
			}
		})
	}
}

func TestSubqueryParameterLimitIncludesOuterAndInnerArguments(t *testing.T) {
	backend, _, query := subqueryFixture()
	inner := query.Filter(Q("parent_id", OuterRef("id")), Q("visible", true)).Limit(1)
	query = query.Filter(Q("id__gt", 7)).Annotate(map[string]ResultExpression{
		"latest": Typed(Subquery(inner, "amount"), models.DecimalField("latest", 20, 2, models.Nullable)),
	})
	backend.maximum = 3
	_, args, err := query.SQLContext(context.Background())
	if err != nil || !reflect.DeepEqual(args, []any{true, 1, 7}) {
		t.Fatal("exact shared parameter limit rejected", args, err)
	}
	backend.maximum = 2
	_, args, err = query.SQLContext(context.Background())
	if !db.IsCode(err, db.UnsupportedFeature) || len(args) != 0 || backend.begins != 0 {
		t.Fatal("outer plus inner overflow accepted", args, backend.begins, err)
	}
}

func TestSubqueryScopeCannotIntroduceUncapturedLazySource(t *testing.T) {
	backend, _, query := subqueryFixture()
	innerCalls := 0
	inner := query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
		innerCalls++
		return Q("visible", true), nil
	})
	query = query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
		expression := Exists(inner)
		return db.Predicate{Expression: &expression, Value: true}, nil
	})
	_, args, err := query.SQLContext(context.Background())
	if !db.IsCode(err, db.UnsupportedFeature) || len(args) != 0 || innerCalls != 0 || backend.begins != 0 {
		t.Fatal("late source invoked inner scope or compiled", args, innerCalls, backend.begins, err)
	}
}
