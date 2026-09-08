package postgres_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestWindowNumberingPreservesRowsAndScope(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "WindowScore", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("score")}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, values := range []map[string]any{{"tenant": 1, "score": 10}, {"tenant": 2, "score": 99}, {"tenant": 1, "score": 20}} {
		saveMap(t, store, schema, values)
	}
	query := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(schema); return row }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	rows, err := query.Annotate(map[string]orm.ResultExpression{
		"number": orm.Typed(orm.Window(orm.RowNumber(), db.WindowSpec{}), models.BigIntegerField("output")),
	}).OrderBy("id").All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal("window projection must preserve scoped model rows", len(rows), err)
	}
	seen := map[any]bool{}
	for _, row := range rows {
		seen[row.ModelState().Annotations["number"]] = true
	}
	if len(seen) != 2 || !seen[int64(1)] || !seen[int64(2)] {
		t.Fatal("scope must precede window numbering", seen)
	}
}

func TestWindowNativeFunctionsFramesPeersAndPagination(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "WindowNative", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("team"), models.IntegerField("score", models.Nullable), models.DecimalField("amount", 30, 9)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, values := range []map[string]any{
		{"tenant": 1, "team": 1, "score": 10, "amount": "9007199254740993.000000001"},
		{"tenant": 1, "team": 1, "score": 10, "amount": "0.000000002"},
		{"tenant": 2, "team": 1, "score": 999, "amount": "99.000000000"},
		{"tenant": 1, "team": 1, "score": 20, "amount": "0.000000003"},
		{"tenant": 1, "team": 2, "score": 30, "amount": "1.000000000"},
		{"tenant": 1, "team": 2, "score": nil, "amount": "2.000000000"},
	} {
		saveMap(t, store, schema, values)
	}
	query := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(schema); return row }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	integer := models.BigIntegerField("output")
	nullable := models.BigIntegerField("output", models.Nullable)
	floating := models.FloatField("output")
	byID := db.WindowSpec{PartitionBy: []db.Expression{orm.F("team")}, OrderBy: []db.WindowOrder{{Expression: orm.F("id")}}}
	byScore := db.WindowSpec{PartitionBy: []db.Expression{orm.F("team")}, OrderBy: []db.WindowOrder{{Expression: orm.F("score"), NullsLast: true}}}
	full := byID
	full.Frame = &db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowUnboundedPreceding}, End: db.WindowBound{Kind: db.WindowUnboundedFollowing}}
	previous := byID
	previous.Frame = &db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowPreceding, Offset: 1}, End: db.WindowBound{Kind: db.WindowCurrentRow}}
	peers := byScore
	peers.Frame = &db.WindowFrame{Mode: db.WindowRange, Start: db.WindowBound{Kind: db.WindowCurrentRow}, End: db.WindowBound{Kind: db.WindowCurrentRow}}
	excluded := full
	frame := *full.Frame
	frame.Exclusion = db.WindowExcludeCurrentRow
	excluded.Frame = &frame
	annotated := query.Annotate(map[string]orm.ResultExpression{
		"number":       orm.Typed(orm.Window(orm.RowNumber(), byID), integer),
		"rank":         orm.Typed(orm.Window(orm.Rank(), byScore), integer),
		"dense":        orm.Typed(orm.Window(orm.DenseRank(), byScore), integer),
		"percent":      orm.Typed(orm.Window(orm.PercentRank(), byScore), floating),
		"cume":         orm.Typed(orm.Window(orm.CumeDist(), byScore), floating),
		"tile":         orm.Typed(orm.Window(orm.NTile(2), byID), integer),
		"lag":          orm.Typed(orm.Window(orm.Lag(orm.F("score")), byID), nullable),
		"lead":         orm.Typed(orm.Window(orm.Lead(orm.F("score"), orm.Value(1), orm.Value(-1)), byID), nullable),
		"first":        orm.Typed(orm.Window(orm.FirstValue(orm.F("score")), full), nullable),
		"last":         orm.Typed(orm.Window(orm.LastValue(orm.F("score")), full), nullable),
		"nth":          orm.Typed(orm.Window(orm.NthValue(orm.F("score"), 2), full), nullable),
		"running":      orm.Typed(orm.Window(orm.Sum(orm.F("score")), byID), nullable),
		"native_peers": orm.Typed(orm.Window(orm.Sum(orm.F("score")), byScore), nullable),
		"previous":     orm.Typed(orm.Window(orm.Sum(orm.F("score")), previous), nullable),
		"peers":        orm.Typed(orm.Window(orm.Count(orm.F("*")), peers), integer),
		"excluded":     orm.Typed(orm.Window(orm.Sum(orm.F("score")), excluded), nullable),
		"decimal":      orm.Typed(orm.Window(orm.Sum(orm.F("amount")), full), models.DecimalField("output", 40, 9)),
		"filtered":     orm.Typed(orm.Window(orm.FilteredAggregate(orm.Count(orm.F("*")), orm.Q("score__gte", 20)), full), integer),
	}).OrderBy("id")
	rows, err := annotated.All(ctx)
	if err != nil || len(rows) != 5 {
		t.Fatal(len(rows), err)
	}
	want := map[string][]any{
		"number": {int64(1), int64(2), int64(3), int64(1), int64(2)}, "rank": {int64(1), int64(1), int64(3), int64(1), int64(2)}, "dense": {int64(1), int64(1), int64(2), int64(1), int64(2)},
		"tile": {int64(1), int64(1), int64(2), int64(1), int64(2)}, "lag": {nil, int64(10), int64(10), nil, int64(30)}, "lead": {int64(10), int64(20), int64(-1), nil, int64(-1)},
		"first": {int64(10), int64(10), int64(10), int64(30), int64(30)}, "last": {int64(20), int64(20), int64(20), nil, nil}, "nth": {int64(10), int64(10), int64(10), nil, nil},
		"running": {int64(10), int64(20), int64(40), int64(30), int64(30)}, "native_peers": {int64(20), int64(20), int64(40), int64(30), int64(30)}, "previous": {int64(10), int64(20), int64(30), int64(30), int64(30)},
		"peers": {int64(2), int64(2), int64(1), int64(1), int64(1)}, "excluded": {int64(30), int64(30), int64(20), nil, int64(30)}, "filtered": {int64(1), int64(1), int64(1), int64(1), int64(1)},
	}
	for name, expected := range want {
		for i, row := range rows {
			if !reflect.DeepEqual(row.ModelState().Annotations[name], expected[i]) {
				t.Fatal(name, i, row.ModelState().Annotations[name], expected[i])
			}
		}
	}
	for i, row := range rows {
		percent := []float64{0, 0, 1, 0, 1}[i]
		cume := []float64{2.0 / 3, 2.0 / 3, 1, 0.5, 1}[i]
		if math.Abs(row.ModelState().Annotations["percent"].(float64)-percent) > 1e-12 || math.Abs(row.ModelState().Annotations["cume"].(float64)-cume) > 1e-12 {
			t.Fatal("distribution", i)
		}
		decimal := "9007199254740993.000000006"
		if i >= 3 {
			decimal = "3.000000000"
		}
		if row.ModelState().Annotations["decimal"] != decimal {
			t.Fatal("precision lost", row.ModelState().Annotations["decimal"])
		}
	}
	page, err := annotated.Offset(1).Limit(1).Values(ctx, "number", "running")
	if err != nil || len(page) != 1 || page[0]["number"] != int64(2) || page[0]["running"] != int64(20) {
		t.Fatal("pagination occurred before window", page, err)
	}
	if count, err := annotated.Count(ctx); err != nil || count != 5 {
		t.Fatal(count, err)
	}
	if _, _, err := annotated.SQLContext(ctx); err != nil {
		t.Fatal(err)
	}
	// EXCLUDE GROUP/TIES retain PostgreSQL's peer semantics even for ROWS.
	for _, test := range []struct {
		exclusion db.WindowExclusion
		expected  int64
	}{{db.WindowExcludeGroup, 1}, {db.WindowExcludeTies, 2}, {db.WindowExcludeNoOthers, 3}} {
		spec := byScore
		spec.Frame = &db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowUnboundedPreceding}, End: db.WindowBound{Kind: db.WindowUnboundedFollowing}, Exclusion: test.exclusion}
		values, err := query.Annotate(map[string]orm.ResultExpression{"counted": orm.Typed(orm.Window(orm.Count(orm.F("*")), spec), integer)}).OrderBy("id").Limit(1).Values(ctx, "counted")
		if err != nil || len(values) != 1 || values[0]["counted"] != test.expected {
			t.Fatal(test, values, err)
		}
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	for _, invalid := range []orm.Query[*models.MapRecord]{
		annotated.Filter(orm.Q("number__gt", 1)), annotated.Having(orm.Q("rank", 1)), annotated.GroupBy("team"), annotated.SelectForUpdate(false, false),
		query.Annotate(map[string]orm.ResultExpression{"bad": orm.Typed(orm.Window(orm.NTile(0), byID), integer)}),
	} {
		if _, _, err := invalid.SQLContext(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid window reached SQL", err)
		}
	}
	if _, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"bad": orm.Typed(orm.Sum(orm.Window(orm.RowNumber(), byID)), integer)}); err == nil || counted.queries.Load() != 0 {
		t.Fatal("window aggregate reached SQL", err)
	}
	if _, err := query.Update(ctx, map[string]any{"score": orm.Window(orm.RowNumber(), byID)}); err == nil || counted.queries.Load() != 0 {
		t.Fatal("window update reached SQL", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := annotated.All(canceled); !errors.Is(err, context.Canceled) || counted.queries.Load() != 0 {
		t.Fatal(err)
	}
	if _, err := query.Annotate(map[string]orm.ResultExpression{"invalid_output": orm.Typed(orm.Window(orm.Lag(orm.F("score")), byID), integer)}).All(ctx); err == nil || !strings.Contains(err.Error(), "could not be decoded") {
		t.Fatal("nonnullable output accepted null", err)
	}
}

func TestWindowExplicitToOneJoinsPreserveIndependentScopes(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "WindowTarget", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("score")}}
	source := models.Schema{AppLabel: "tests", Name: "WindowSource", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), OnDelete: models.Protect}, models.Nullable)}}
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
	visible := saveMap(t, store, target, map[string]any{"tenant": 2, "score": 5})
	hidden := saveMap(t, store, target, map[string]any{"tenant": 3, "score": 99})
	for _, values := range []map[string]any{{"tenant": 1, "target": mustValue(t, visible, "id")}, {"tenant": 1, "target": mustValue(t, hidden, "id")}, {"tenant": 4, "target": mustValue(t, visible, "id")}} {
		saveMap(t, store, source, values)
	}
	query := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(source); return row }).WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == source.Key() {
			return orm.Q("tenant", 1), nil
		}
		return orm.Q("tenant", 2), nil
	})
	expressions := map[string]orm.ResultExpression{"total": orm.Typed(orm.Window(orm.Sum(orm.F("target__score")), db.WindowSpec{}), models.BigIntegerField("out"))}
	if _, _, err := query.Annotate(expressions).SQLContext(ctx); err == nil {
		t.Fatal("window inferred an undeclared relation join")
	}
	rows, err := query.SelectRelated("target").Annotate(expressions).OrderBy("id").All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal(len(rows), err)
	}
	for _, row := range rows {
		if row.ModelState().Annotations["total"] != int64(5) {
			t.Fatal("hidden target or root contributed to window", row.ModelState().Annotations)
		}
	}
}
