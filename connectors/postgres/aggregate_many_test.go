package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAggregateManyToManyScopesLinksBeforeParentMultiplication(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit"}[explicit], func(t *testing.T) {
			store, first, tags, bridge := setupMany(t, explicit)
			ctx := context.Background()
			root, target := first.Schema(), tags[0].Schema()
			tags = append(tags, saveMap(t, store, target, map[string]any{"tenant": 2}))
			onlyHidden := saveMap(t, store, root, map[string]any{"tenant": 1})
			saveMap(t, store, root, map[string]any{"tenant": 1})
			hiddenRoot := saveMap(t, store, root, map[string]any{"tenant": 2})
			link := func(source, target *models.MapRecord, visible bool) {
				values := map[string]any{"source_id": mustValue(t, source, "id"), "target_id": mustValue(t, target, "id")}
				if explicit {
					tenant := 1
					if !visible {
						tenant = 2
					}
					values = map[string]any{"post": mustValue(t, source, "id"), "tag": mustValue(t, target, "id"), "tenant": tenant, "note": "fixture"}
				}
				saveMap(t, store, bridge, values)
			}
			link(first, tags[0], true)
			link(first, tags[1], true)
			link(first, tags[2], true)
			link(first, tags[3], true)
			link(onlyHidden, tags[2], true)
			link(onlyHidden, tags[3], true)
			link(hiddenRoot, tags[0], true)
			if explicit {
				link(onlyHidden, tags[0], false)
				link(onlyHidden, tags[1], false)
			}
			counted := &countedBackend{Backend: store.Backend}
			store.Backend = counted
			scopeCalls := map[string]int{}
			scope := func(_ context.Context, schema models.Schema) (db.Predicate, error) {
				scopeCalls[schema.Key()]++
				if store.Registry.IsAutomatic(schema.Key()) {
					t.Fatal("automatic bridge was treated as an app resource")
				}
				return orm.Q("tenant", 1), nil
			}
			base := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(root); return r }).WithScope(scope).OrderBy("id")
			integer := models.BigIntegerField("out")
			query := base.Annotate(map[string]orm.ResultExpression{
				"links":      orm.Typed(orm.Count(orm.F("tags__id")), integer),
				"root_count": orm.Typed(orm.Count(orm.F("id")), integer),
				"root_sum":   orm.Typed(orm.Sum(orm.F("tenant")), integer),
				"higher":     orm.Typed(orm.FilteredAggregate(orm.Count(orm.F("tags__id")), orm.Q("tags__tenant", 1)), integer),
			})
			rows, err := query.All(ctx)
			if err != nil || len(rows) != 3 || counted.queries.Load() != 1 {
				t.Fatal("grouped intermediary query", rows, err, counted.queries.Load())
			}
			for i, row := range rows {
				wantLinks, wantRoots := int64(0), int64(1)
				if i == 0 {
					wantLinks, wantRoots = 2, 2
				}
				a := row.State().Annotations
				if a["links"] != wantLinks || a["higher"] != wantLinks || a["root_count"] != wantRoots || a["root_sum"] != wantRoots || len(row.State().Related) != 0 {
					t.Fatal("hidden links altered root multiplicity or hydration", i, a, row.State().Related)
				}
			}
			if scopeCalls[root.Key()] != 1 || scopeCalls[target.Key()] != 1 || explicit && scopeCalls[bridge.Key()] != 1 {
				t.Fatal("independent scopes", scopeCalls)
			}
			if count, err := query.Filter(orm.Q("links", 0)).Count(ctx); err != nil || count != 2 {
				t.Fatal("M2M HAVING count", count, err)
			}
			all, err := base.Aggregate(ctx, map[string]orm.ResultExpression{"links": orm.Typed(orm.Count(orm.F("tags__id")), integer), "roots": orm.Typed(orm.Count(orm.F("id")), integer)})
			if err != nil || all["links"] != int64(2) || all["roots"] != int64(4) {
				t.Fatal("scalar M2M aggregate", all, err)
			}
			nested, err := base.Annotate(map[string]orm.ResultExpression{"links": orm.Typed(orm.Count(orm.F("tags__posts__id")), integer)}).All(ctx)
			if err != nil || len(nested) != 3 || nested[0].State().Annotations["links"] != int64(2) || nested[1].State().Annotations["links"] != int64(0) {
				t.Fatal("nested forward/reverse M2M scopes", nested, err)
			}
			reverse := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(target); return r }).WithScope(scope).Annotate(map[string]orm.ResultExpression{"posts_count": orm.Typed(orm.Count(orm.F("posts__id")), integer)}).OrderBy("id")
			reversed, err := reverse.All(ctx)
			if err != nil || len(reversed) != 2 || reversed[0].State().Annotations["posts_count"] != int64(1) || reversed[1].State().Annotations["posts_count"] != int64(1) {
				t.Fatal("reverse links bypassed root/intermediary scope", reversed, err)
			}
			// A second independent branch is a real collection join. It must preserve
			// SQL multiplication of visible rows, without retaining any hidden links.
			multiple, err := base.Annotate(map[string]orm.ResultExpression{"links": orm.Typed(orm.Count(orm.F("tags__id")), integer), "nested": orm.Typed(orm.Count(orm.F("tags__posts__tags__id")), integer), "unique": orm.Typed(orm.DistinctAggregate(orm.Count(orm.F("tags__id"))), integer)}).All(ctx)
			if err != nil || len(multiple) != 3 || multiple[0].State().Annotations["links"] != int64(4) || multiple[0].State().Annotations["nested"] != int64(4) || multiple[0].State().Annotations["unique"] != int64(2) {
				t.Fatal("visible collection multiplication", multiple, err)
			}
			counted.queries.Store(0)
			denied := errors.New("scope unavailable")
			for _, fail := range []string{target.Key(), bridge.Key()} {
				if fail == bridge.Key() && !explicit {
					continue
				}
				if _, err := query.WithScope(func(ctx context.Context, s models.Schema) (db.Predicate, error) {
					if s.Key() == fail {
						return db.Predicate{}, denied
					}
					return scope(ctx, s)
				}).All(ctx); !errors.Is(err, denied) || counted.queries.Load() != 0 {
					t.Fatal("scope error reached SQL", err)
				}
				cancelCtx, cancel := context.WithCancel(ctx)
				_, err := query.WithScope(func(ctx context.Context, s models.Schema) (db.Predicate, error) {
					if s.Key() == fail {
						cancel()
					}
					return scope(ctx, s)
				}).All(cancelCtx)
				cancel()
				if !errors.Is(err, context.Canceled) || counted.queries.Load() != 0 {
					t.Fatal("scope cancellation reached SQL", err)
				}
			}
			if _, err := base.Annotate(map[string]orm.ResultExpression{"bridge_secret": orm.Typed(orm.Count(orm.F("tags__gogo_bridge_1__note")), integer)}).All(ctx); err == nil || counted.queries.Load() != 0 {
				t.Fatal("bridge exposed as relation path", err)
			}
		})
	}
}

func TestAggregateExplicitManyToManyUsesUniqueNonPrimaryEndpoints(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	unique := func(f *models.Field) { f.Unique = true }
	root := models.Schema{AppLabel: "tests", Name: "CodeOwner", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code", unique), models.IntegerField("tenant")}}
	target := models.Schema{AppLabel: "tests", Name: "CodeTarget", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code", unique), models.IntegerField("tenant")}}
	bridge := models.Schema{AppLabel: "tests", Name: "CodeBridge", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("source", models.Relation{Target: root.Key(), TargetFields: []string{"code"}, OnDelete: models.Cascade}), models.ForeignKeyField("target", models.Relation{Target: target.Key(), TargetFields: []string{"code"}, OnDelete: models.Cascade}), models.IntegerField("tenant")}}
	root.Fields = append(root.Fields, models.ManyToManyField("targets", models.Relation{Target: target.Key(), Through: bridge.Key(), ThroughFields: []string{"source", "target"}, RelatedName: "owners"}))
	registry := &models.Registry{}
	operations := []migrations.Operation{}
	for _, s := range []models.Schema{root, target, bridge} {
		if err := registry.Register(s); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, migrations.CreateModel(s))
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: operations}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	saveMap(t, store, root, map[string]any{"code": "owner-a", "tenant": 11})
	saveMap(t, store, root, map[string]any{"code": "owner-b", "tenant": 11})
	saveMap(t, store, target, map[string]any{"code": "target-a", "tenant": 22})
	saveMap(t, store, target, map[string]any{"code": "target-b", "tenant": 99})
	for _, code := range []string{"target-a", "target-b"} {
		saveMap(t, store, bridge, map[string]any{"source": "owner-a", "target": code, "tenant": 33})
	}
	scope := func(_ context.Context, s models.Schema) (db.Predicate, error) {
		tenant := map[string]int{root.Key(): 11, target.Key(): 22, bridge.Key(): 33}[s.Key()]
		return orm.Q("tenant", tenant), nil
	}
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(root); return r }).WithScope(scope).Annotate(map[string]orm.ResultExpression{"targets_count": orm.Typed(orm.Count(orm.F("targets__id")), models.BigIntegerField("out"))}).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || rows[0].State().Annotations["targets_count"] != int64(1) || rows[1].State().Annotations["targets_count"] != int64(0) {
		t.Fatal("non-PK intermediary endpoints", rows, err)
	}
	sql, args, err := query.SQLContext(ctx)
	if err != nil || !strings.Contains(sql, `"gogo_root"."code"="gogo_bridge_1"."source"`) || !strings.Contains(sql, `"gogo_bridge_1"."target"="gogo_join_1"."code"`) || len(args) != 3 {
		t.Fatal("non-PK SQL endpoint binding", sql, args, err)
	}
}

func TestAggregateSelfManyToManySymmetryKeepsScopedEndpoints(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit"}[explicit], func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := models.Schema{AppLabel: "tests", Name: "Friend", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
			bridge := models.Schema{AppLabel: "tests", Name: "Friendship", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("source", models.Relation{Target: schema.Key(), OnDelete: models.Cascade}), models.ForeignKeyField("target", models.Relation{Target: schema.Key(), OnDelete: models.Cascade}), models.IntegerField("tenant")}}
			relation := models.Relation{Target: schema.Key()}
			if explicit {
				relation.Through = bridge.Key()
				relation.ThroughFields = []string{"source", "target"}
			}
			schema.Fields = append(schema.Fields, models.ManyToManyField("friends", relation))
			schemas := []models.Schema{schema}
			if explicit {
				schemas = append(schemas, bridge)
			}
			registry := &models.Registry{}
			operations := []migrations.Operation{}
			for _, s := range schemas {
				if err := registry.Register(s); err != nil {
					t.Fatal(err)
				}
				operations = append(operations, migrations.CreateModel(s))
			}
			if err := registry.Freeze(); err != nil {
				t.Fatal(err)
			}
			engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: operations}}}
			if err := engine.Apply(ctx, ""); err != nil {
				t.Fatal(err)
			}
			store := orm.New(b, registry)
			first := saveMap(t, store, schema, map[string]any{"tenant": 1})
			second := saveMap(t, store, schema, map[string]any{"tenant": 1})
			hidden := saveMap(t, store, schema, map[string]any{"tenant": 2})
			saveMap(t, store, schema, map[string]any{"tenant": 1})
			manager := orm.RelationManager{Store: store, Source: first, Name: "friends"}
			if explicit {
				manager.ThroughDefaults = orm.Defaults{"tenant": 1}
			}
			if err := manager.Add(ctx, first, second, hidden); err != nil {
				t.Fatal(err)
			}
			counted := &countedBackend{Backend: b}
			store.Backend = counted
			base := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).Annotate(map[string]orm.ResultExpression{"friend_count": orm.Typed(orm.Count(orm.F("friends__id")), models.BigIntegerField("out"))}).OrderBy("id")
			scoped := base.WithScope(func(_ context.Context, s models.Schema) (db.Predicate, error) {
				if registry.IsAutomatic(s.Key()) {
					t.Fatal("automatic symmetric bridge was scoped as app")
				}
				return orm.Q("tenant", 1), nil
			})
			rows, err := scoped.All(ctx)
			if explicit {
				if !db.IsCode(err, db.UnsupportedFeature) || counted.queries.Load() != 0 {
					t.Fatal("scoped explicit symmetry silently applied a half-policy", err)
				}
				rows, err = base.All(ctx)
				if err != nil || len(rows) != 4 || rows[0].State().Annotations["friend_count"] != int64(3) || rows[1].State().Annotations["friend_count"] != int64(1) {
					t.Fatal("unscoped explicit symmetric aggregate", rows, err)
				}
				return
			}
			if err != nil || len(rows) != 3 || rows[0].State().Annotations["friend_count"] != int64(2) || rows[1].State().Annotations["friend_count"] != int64(1) || rows[2].State().Annotations["friend_count"] != int64(0) {
				t.Fatal("automatic symmetric scopes/self-link cardinality", rows, err)
			}
		})
	}
}
