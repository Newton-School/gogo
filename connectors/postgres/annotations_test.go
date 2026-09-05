package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type annotatedRow struct {
	models.Base
	ID      int64
	Tenant  int64
	Score   int64
	Payload any
}

type annotationDurationCodec struct {
	encoded, decoded int
	raw              any
}

type cancelAnnotationCodec struct{ encode, decode func(any) (any, error) }

func (c cancelAnnotationCodec) Encode(value any) (any, error) {
	if c.encode != nil {
		return c.encode(value)
	}
	return value, nil
}
func (c cancelAnnotationCodec) Decode(value any) (any, error) {
	if c.decode != nil {
		return c.decode(value)
	}
	return value, nil
}

func TestRowAnnotationsPreserveCancellationDuringCodecs(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&annotatedRow{}).Schema()
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	saveMap(t, store, schema, map[string]any{"tenant": 1, "score": 1, "payload": nil})
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	base := orm.For(store, func() *annotatedRow { return &annotatedRow{} })
	for _, mode := range []string{"sql", "all", "values"} {
		for _, errorOnly := range []bool{false, true} {
			canceled, cancel := context.WithCancel(ctx)
			codec := cancelAnnotationCodec{encode: func(value any) (any, error) {
				if errorOnly {
					return nil, context.Canceled
				}
				cancel()
				return nil, nil
			}}
			output := models.TextField("out", models.Nullable, func(f *models.Field) { f.Codec = codec })
			query := base.Annotate(map[string]orm.ResultExpression{"value": orm.Typed(orm.Value("text"), output)})
			counted.queries.Store(0)
			var err error
			switch mode {
			case "sql":
				_, _, err = query.SQLContext(canceled)
			case "all":
				_, err = query.All(canceled)
			case "values":
				_, err = query.Values(canceled)
			}
			cancel()
			if !errors.Is(err, context.Canceled) || counted.queries.Load() != 0 {
				t.Fatal("encode cancellation ignored", mode, err)
			}
		}
	}
	for _, mode := range []string{"all", "values"} {
		for _, errorOnly := range []bool{false, true} {
			canceled, cancel := context.WithCancel(ctx)
			codec := cancelAnnotationCodec{decode: func(value any) (any, error) {
				if errorOnly {
					return nil, context.DeadlineExceeded
				}
				cancel()
				return nil, nil
			}}
			output := models.BigIntegerField("out", models.Nullable, func(f *models.Field) { f.Codec = codec })
			query := base.Annotate(map[string]orm.ResultExpression{"value": orm.Typed(orm.F("score"), output)})
			counted.queries.Store(0)
			var err error
			if mode == "all" {
				_, err = query.All(canceled)
			} else {
				_, err = query.Values(canceled)
			}
			cancel()
			want := context.Canceled
			if errorOnly {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || counted.queries.Load() != 1 {
				t.Fatal("decode cancellation ignored", mode, err)
			}
		}
	}
}

func (c *annotationDurationCodec) Encode(any) (any, error) { c.encoded++; return "4 seconds", nil }
func (c *annotationDurationCodec) Decode(value any) (any, error) {
	c.decoded++
	c.raw = value
	return time.Minute, nil
}

func TestRowAnnotationsCustomCodecCancellationAndTypedNil(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&annotatedRow{}).Schema()
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	saveMap(t, store, schema, map[string]any{"tenant": 1, "score": 1, "payload": nil})
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	codec := &annotationDurationCodec{}
	output := models.DurationField("out", func(f *models.Field) { f.Codec = codec })
	query := orm.For(store, func() *annotatedRow { return &annotatedRow{} }).Annotate(map[string]orm.ResultExpression{
		"custom":           orm.Typed(orm.Value(time.Second), output),
		"nil_json_pointer": orm.Typed(orm.Value((*json.RawMessage)(nil)), models.JSONField("out", models.Nullable)),
	}).Filter(orm.Q("custom__gt", 3*time.Second)).OrderBy("custom")
	if codec.encoded != 0 || codec.decoded != 0 {
		t.Fatal("annotation builder invoked provider hooks")
	}
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 1 || rows[0].ModelState().Annotations["custom"] != time.Minute || rows[0].ModelState().Annotations["nil_json_pointer"] != nil || codec.encoded != 1 || codec.decoded != 1 || codec.raw != "00:00:04" {
		t.Fatal("annotation custom codec or SQLNULL semantics", rows, err, codec)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	before := counted.queries.Load()
	if _, err := query.All(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled annotation query", err)
	}
	if _, err := query.Values(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled annotation values", err)
	}
	if _, _, err := query.SQLContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled annotation SQL", err)
	}
	if counted.queries.Load() != before || codec.encoded != 1 {
		t.Fatal("canceled operation invoked database/codec")
	}
}

func (*annotatedRow) Schema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "Annotated", Fields: []models.Field{models.BigAutoField("id", func(f *models.Field) { f.StructField = "ID" }), models.BigIntegerField("tenant", func(f *models.Field) { f.StructField = "Tenant" }), models.BigIntegerField("score", func(f *models.Field) { f.StructField = "Score" }), models.JSONField("payload", models.Nullable, func(f *models.Field) { f.StructField = "Payload" })}}
}

func TestRowAnnotationsTypedSnapshotsFiltersValuesAndSaveIsolation(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&annotatedRow{}).Schema()
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, value := range []map[string]any{{"tenant": 1, "score": 3, "payload": map[string]any{"n": json.Number("9007199254740993"), "null": models.JSONNull}}, {"tenant": 1, "score": 4, "payload": nil}, {"tenant": 2, "score": 99, "payload": nil}} {
		saveMap(t, store, schema, value)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	base := orm.For(store, func() *annotatedRow { return &annotatedRow{} }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	integer := models.BigIntegerField("output")
	jsonOutput := models.JSONField("output", models.Nullable)
	literal := map[string]any{"ids": []int{7, 8}}
	query := base.Annotate(map[string]orm.ResultExpression{
		"plus":           orm.Typed(orm.Add(orm.F("score"), orm.Value(10)), integer),
		"twice":          orm.Typed(orm.Add(orm.F("plus"), orm.F("plus")), integer),
		"data":           orm.Typed(orm.F("payload"), jsonOutput),
		"literal":        orm.Typed(orm.Value(literal), jsonOutput),
		"duration":       orm.Typed(orm.Value(25*time.Hour+3*time.Microsecond), models.DurationField("output")),
		"empty":          orm.Typed(orm.Value(nil), models.TextField("output", models.Nullable)),
		"json_null":      orm.Typed(orm.Value(models.JSONNull), jsonOutput),
		"literal_string": orm.Typed(orm.Value("null"), jsonOutput),
	}).Filter(orm.Q("plus__gte", 13)).OrderBy("-twice")
	literal["ids"].([]int)[0] = 100
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 1 {
		t.Fatal("typed annotation query", len(rows), err)
	}
	for index, row := range rows {
		annotation := row.ModelState().Annotations
		if row.Score != int64(4-index) || annotation["plus"] != row.Score+10 || annotation["twice"] != 2*(row.Score+10) || annotation["duration"] != 25*time.Hour+3*time.Microsecond || annotation["empty"] != nil || annotation["json_null"] != models.JSONNull || annotation["literal_string"] != "null" || !reflect.DeepEqual(annotation["literal"], map[string]any{"ids": []any{json.Number("7"), json.Number("8")}}) {
			t.Fatal("annotation values/precision/null metadata", row, annotation)
		}
	}
	rows[0].ModelState().Annotations["plus"] = int64(1000)
	if rows[1].ModelState().Annotations["plus"] != int64(13) {
		t.Fatal("annotation maps shared between rows")
	}
	rows[0].Score = 5
	if err := store.Save(ctx, rows[0], orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	fresh, err := base.Filter(orm.Q("id", rows[0].ID)).Get(ctx)
	if err != nil || fresh.Score != 5 || len(fresh.ModelState().Annotations) != 0 {
		t.Fatal("annotation leaked to persistence", err)
	}
	values, err := query.Values(ctx, "twice", "id", "data__n", "literal__ids__0")
	if err != nil || len(values) != 2 || len(values[0]) != 4 || values[0]["twice"] != int64(30) || values[0]["literal__ids__0"] != json.Number("7") || values[1]["data__n"] != json.Number("9007199254740993") {
		t.Fatal("selected annotation values", values, err)
	}
	values, err = query.Values(ctx)
	if err != nil || len(values) != 2 || len(values[0]) != len(schema.Fields)+8 {
		t.Fatal("default annotation values", values, err)
	}
	values, err = query.Filter(orm.Q("data__n", json.Number("9007199254740993"))).Values(ctx, "id")
	if err != nil || len(values) != 1 || len(values[0]) != 1 {
		t.Fatal("unselected JSON annotation predicate", values, err)
	}
	values, err = query.Filter(orm.Q("data__null", nil)).Values(ctx, "id")
	if err != nil || len(values) != 1 {
		t.Fatal("JSON alias null lookup", values, err)
	}
	values, err = query.Filter(orm.Q("data__isnull", true)).Values(ctx, "id")
	if err != nil || len(values) != 1 {
		t.Fatal("SQL null alias lookup", values, err)
	}
	deferred, err := query.Only("id", "score").First(ctx)
	if err != nil || deferred.ModelState().Annotations["plus"] != int64(15) || !deferred.ModelState().Deferred["payload"] {
		t.Fatal("deferred annotation row", deferred, err)
	}
	// Lookup candidates against alias output metadata remain native JSON.
	values, err = query.Filter(orm.Q("literal__contains", map[string]any{"ids": []int{7}})).Values(ctx, "id")
	if err != nil || len(values) != 2 {
		t.Fatal("annotation JSON containment", values, err)
	}
	// A NULL result without nullable metadata is a typed decode failure, not
	// a zero-valued successful row; invalid aliases never issue SQL.
	if _, err := base.Annotate(map[string]orm.ResultExpression{"nonnull": orm.Typed(orm.Value(nil), integer)}).All(ctx); err == nil {
		t.Fatal("NULL annotation silently coerced")
	}
	for _, invalid := range []orm.Query[*annotatedRow]{query.Only("plus"), query.Defer("plus"), base.Annotate(map[string]orm.ResultExpression{"loop": orm.Typed(orm.F("loop"), integer)}), base.Annotate(map[string]orm.ResultExpression{"sum": orm.Typed(orm.Sum(orm.F("score")), integer)})} {
		counted.queries.Store(0)
		if _, err := invalid.All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid annotation reached DB", err)
		}
	}
}

func TestRowAnnotationsEagerScopesParameterOrderAndHiddenTargets(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "AnnotationTarget", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("score")}}
	source := models.Schema{AppLabel: "tests", Name: "AnnotationSource", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), RelatedName: "sources", OnDelete: models.Protect}, models.Nullable)}}
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
	visible := saveMap(t, store, target, map[string]any{"tenant": 22, "score": 5})
	hidden := saveMap(t, store, target, map[string]any{"tenant": 44, "score": 99})
	for _, value := range []map[string]any{{"tenant": 33, "target": mustValue(t, visible, "id")}, {"tenant": 33, "target": mustValue(t, hidden, "id")}, {"tenant": 44, "target": mustValue(t, visible, "id")}} {
		saveMap(t, store, source, value)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(source); return record }).WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == source.Key() {
			return orm.Q("tenant", 33), nil
		}
		return orm.Q("tenant", 22), nil
	}).SelectRelated("target").Annotate(map[string]orm.ResultExpression{"visible_score": orm.Typed(orm.Add(orm.F("target__score"), orm.Value(11)), models.BigIntegerField("out", models.Nullable))}).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 1 || rows[0].State().Annotations["visible_score"] != int64(16) || rows[1].State().Annotations["visible_score"] != nil {
		t.Fatal("eager annotation leaked target/scope", rows, err)
	}
	if related, ok := orm.RelatedOne(rows[1], "target"); !ok || related != nil {
		t.Fatal("hidden eager target exposed")
	}
	values, err := query.Filter(orm.Q("visible_score__gte", 15)).Values(ctx, "id", "visible_score", "target__score")
	if err != nil || len(values) != 1 || len(values[0]) != 3 || values[0]["visible_score"] != int64(16) || values[0]["target__score"] != int64(5) {
		t.Fatal("scoped annotation Values binding", values, err)
	}
	values, err = query.Values(ctx)
	if err != nil || len(values) != 2 || len(values[0]) != len(source.Fields)+1 {
		t.Fatal("default Values leaked eager projection", values, err)
	}
	if count, err := query.Filter(orm.Q("visible_score__isnull", true)).Count(ctx); err != nil || count != 1 {
		t.Fatal("hidden target was not NULL", count, err)
	}
	// Without the explicit selected join, no alias may invent a target read.
	counted.queries.Store(0)
	if _, err := query.SelectRelated().All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("annotation invented unscoped join", err)
	}
}

func TestRowAnnotationsDistinctUsesSelectedOutputIdentity(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&annotatedRow{}).Schema()
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, score := range []int{1, 1, 2} {
		saveMap(t, store, schema, map[string]any{"tenant": 1, "score": score, "payload": nil})
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *annotatedRow { return &annotatedRow{} }).Annotate(map[string]orm.ResultExpression{
		"computed":   orm.Typed(orm.Add(orm.F("score"), orm.Value(2)), models.BigIntegerField("out")),
		"json_value": orm.Typed(orm.Value(map[string]any{"n": 1}), models.JSONField("out")),
	}).OrderBy("computed", "id")
	rows, err := query.DistinctOn("computed").All(ctx)
	if err != nil || len(rows) != 2 || rows[0].ModelState().Annotations["computed"] != int64(3) || rows[1].ModelState().Annotations["computed"] != int64(4) {
		t.Fatal("DISTINCT ON alias differed from ordering", rows, err)
	}
	values, err := query.OrderBy("computed").Distinct().Values(ctx, "computed")
	if err != nil || len(values) != 2 || values[0]["computed"] != int64(3) || values[1]["computed"] != int64(4) {
		t.Fatal("DISTINCT selected alias", values, err)
	}
	values, err = query.DistinctOn("computed").Values(ctx, "computed", "id")
	if err != nil || len(values) != 2 {
		t.Fatal("DISTINCT ON selected Values alias", values, err)
	}
	for _, invalid := range []orm.Query[*annotatedRow]{query.DistinctOn("computed"), query.Distinct(), query.DistinctOn("json_value__n").OrderBy("json_value__n")} {
		counted.queries.Store(0)
		if _, err := invalid.Values(ctx, "id"); !db.IsCode(err, db.UnsupportedFeature) || counted.queries.Load() != 0 {
			t.Fatal("unselected/transformed DISTINCT annotation reached SQL", err)
		}
	}
}
