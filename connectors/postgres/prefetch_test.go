package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"strings"
	"testing"
)

func TestPrefetchManyToManyScopeCustomOrderAndEmptyCollections(t *testing.T) {
	store, post, tags, _ := setupMany(t, false)
	ctx := context.Background()
	if err := (orm.RelationManager{Store: store, Source: post, Name: "tags"}).Add(ctx, tags[0], tags[1], tags[2]); err != nil {
		t.Fatal(err)
	}
	saveMap(t, store, post.Schema(), map[string]any{"tenant": 1})
	counted := &countedBackend{Backend: store.Backend}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(post.Schema()); return record }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }).OrderBy("id").Prefetch(orm.Prefetch{Path: "tags", ToAttr: "visible_tags", OrderBy: []string{"-id"}})
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 3 {
		t.Fatal(rows, err, counted.queries.Load())
	}
	related, ok := orm.RelatedMany(rows[0], "visible_tags")
	if !ok || len(related) != 2 || mustValue(t, related[0], "id") != mustValue(t, tags[1], "id") {
		t.Fatal(related, ok)
	}
	if empty, loaded := orm.RelatedMany(rows[1], "visible_tags"); !loaded || empty == nil || len(empty) != 0 {
		t.Fatal("missing explicit empty collection", empty, loaded)
	}
	related[0] = nil
	if original, _ := orm.RelatedMany(rows[0], "visible_tags"); original[0] == nil {
		t.Fatal("cache slice was not isolated")
	}
	if _, err := query.Iterator(ctx); err == nil {
		t.Fatal("streaming prefetch silently accepted")
	}
	if _, err := query.EagerLimit(3).All(ctx); !errors.Is(err, orm.ErrEagerLimit) {
		t.Fatal("graph bound silently truncated", err)
	}
	filtered, err := query.Prefetch(orm.Prefetch{Path: "tags", Where: orm.Q("id", mustValue(t, tags[0], "id"))}).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if values, ok := orm.RelatedMany(filtered[0], "tags"); !ok || len(values) != 1 {
		t.Fatal(values, ok)
	}
	if _, err := query.Prefetch(orm.Prefetch{Path: "tags", ToAttr: "tenant"}).All(ctx); err == nil {
		t.Fatal("cache/model-field conflict accepted")
	}
	if repeated, err := query.PrefetchRelated("tags", "tags__posts").All(ctx); err != nil || len(repeated) != 2 {
		t.Fatal("finite repeated-schema prefetch rejected", repeated, err)
	}
	if err := (orm.RelationManager{Store: store, Source: rows[0], Name: "tags", Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }}).Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, loaded := orm.RelatedMany(rows[0], "visible_tags"); loaded {
		t.Fatal("mutation retained stale prefetch cache")
	}
}

func TestEagerFiniteSelfPathsAndRecursivePrefetchConflicts(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "Node", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("parent", models.Relation{Target: "tests.Node", OnDelete: models.Cascade, RelatedName: "children"}, models.Nullable)}}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	a := saveMap(t, store, schema, map[string]any{"tenant": 1, "parent": nil})
	bNode := saveMap(t, store, schema, map[string]any{"tenant": 1, "parent": mustValue(t, a, "id")})
	c := saveMap(t, store, schema, map[string]any{"tenant": 1, "parent": mustValue(t, bNode, "id")})
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).Filter(orm.Q("id", mustValue(t, c, "id")))
	for _, prefetch := range []bool{false, true} {
		counted.queries.Store(0)
		q := query.SelectRelated("parent__parent__parent")
		if prefetch {
			q = query.PrefetchRelated("parent__parent__parent")
		}
		rows, err := q.All(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		var current models.Record = rows[0]
		for _, expected := range []any{mustValue(t, bNode, "id"), mustValue(t, a, "id"), nil} {
			related, loaded := orm.RelatedOne(current, "parent")
			if !loaded || (expected == nil) != (related == nil) {
				t.Fatal("self relation cache missing", related, loaded)
			}
			if related != nil && mustValue(t, related, "id") != expected {
				t.Fatal("wrong self relation identity")
			}
			current = related
		}
		want := int32(1)
		if prefetch {
			want = 3
		}
		if counted.queries.Load() != want {
			t.Fatal("self relation query count", counted.queries.Load(), want)
		}
	}
	// A cycle in database rows is not a cycle in the finite query plan.
	if err := a.Set("parent", mustValue(t, c, "id")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, a, orm.SaveOptions{UpdateFields: []string{"parent"}}); err != nil {
		t.Fatal(err)
	}
	eight := strings.TrimSuffix(strings.Repeat("parent__", 8), "__")
	for _, prefetch := range []bool{false, true} {
		q := query.SelectRelated(eight)
		if prefetch {
			q = query.PrefetchRelated(eight)
		}
		rows, err := q.All(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatal("bounded database cycle failed", rows, err)
		}
		var current models.Record = rows[0]
		for range 8 {
			var loaded bool
			current, loaded = orm.RelatedOne(current, "parent")
			if !loaded || current == nil {
				t.Fatal("explicit eight-segment path truncated")
			}
		}
		if _, loaded := orm.RelatedOne(current, "parent"); loaded {
			t.Fatal("unrequested cyclic edge loaded")
		}
		counted.queries.Store(0)
		q = query.SelectRelated(eight + "__parent")
		if prefetch {
			q = query.PrefetchRelated(eight + "__parent")
		}
		if _, err := q.All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("excessive path reached SQL", err)
		}
	}
	for _, depth := range []int{3, 4} {
		path := strings.TrimSuffix(strings.Repeat("parent__", depth), "__")
		counted.queries.Store(0)
		if _, err := query.Prefetch(orm.Prefetch{Path: path, Where: orm.Q("tenant", 1)}, orm.Prefetch{Path: path, Where: orm.Q("tenant", 2)}).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("deep conflicting cache queries accepted", depth, err)
		}
		counted.queries.Store(0)
		if _, err := query.Prefetch(orm.Prefetch{Path: path}, orm.Prefetch{Path: path}).All(ctx); err != nil || counted.queries.Load() != int32(depth+1) {
			t.Fatal("identical deep paths not merged", depth, counted.queries.Load(), err)
		}
	}
	counted.queries.Store(0)
	if _, err := query.SelectRelated("parent__parent").Prefetch(orm.Prefetch{Path: "parent__parent", Where: orm.Q("tenant", 2)}).All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("custom prefetch overwrote selected cache", err)
	}
	rows, err := query.SelectRelated("parent").Prefetch(orm.Prefetch{Path: "parent", ToAttr: "alternative", Where: orm.Q("tenant", 2)}).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if parent, loaded := orm.RelatedOne(rows[0], "parent"); !loaded || parent == nil {
		t.Fatal("separate prefetch erased selected parent")
	}
	if parent, loaded := orm.RelatedOne(rows[0], "alternative"); !loaded || parent != nil {
		t.Fatal("custom ToAttr cache missing")
	}
	counted.queries.Store(0)
	if _, err := query.Filter(orm.Q("id", -1)).Prefetch(orm.Prefetch{Path: "parent", Where: orm.Q("unknown", 1)}).All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("invalid custom query depended on root rows", err)
	}
}

type limitedParameterBackend struct {
	*countedBackend
	maximum int
}

func (b limitedParameterBackend) MaxParameters() int { return b.maximum }

func TestPrefetchBatchesReverseRelationsUnderParameterLimit(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	for range 4 {
		next := &relationRow{Definition: parent.Schema(), Tenant: 1}
		if err := store.Save(ctx, next, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		id := next.ID
		if err := store.Save(ctx, &relationRow{Definition: child.Schema(), Tenant: 1, Parent: &id}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	counted := &countedBackend{Backend: store.Backend}
	store.Backend = limitedParameterBackend{counted, 3}
	query := orm.For(store, func() *relationRow { return &relationRow{Definition: parent.Schema()} }).PrefetchRelated("child_set").OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 5 {
		t.Fatal(rows, err)
	}
	if counted.queries.Load() != 4 {
		t.Fatal("expected root + 3 batches of at most 2 keys", counted.queries.Load())
	}
	for _, row := range rows {
		record, _ := models.Bind(row)
		children, ok := orm.RelatedMany(record, "child_set")
		if !ok || len(children) != 1 || mustValue(t, children[0], "parent") != row.ID {
			t.Fatal(children, ok)
		}
	}
	if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error { _, err := query.All(ctx); return err }); err != nil {
		t.Fatal("root rows remained open during transactional prefetch", err)
	}
}

func TestPrefetchToOneFailureAndRootScope(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	parent.Tenant = 2
	if err := store.Save(ctx, parent, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	query := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }).PrefetchRelated("parent")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	record, _ := models.Bind(rows[0])
	if related, loaded := orm.RelatedOne(record, "parent"); !loaded || related != nil {
		t.Fatal("hidden prefetch target leaked", related, loaded)
	}
	failure := errors.New("target scope unavailable")
	if _, err := query.WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == parent.Schema().Key() {
			return db.Predicate{}, failure
		}
		return db.Predicate{}, nil
	}).All(ctx); !errors.Is(err, failure) {
		t.Fatal("prefetch scope error swallowed", err)
	}
}
