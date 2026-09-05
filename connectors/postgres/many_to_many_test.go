package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"sync"
	"testing"
	"time"
)

func m2mSchemas() (models.Schema, models.Schema) {
	target := models.Schema{AppLabel: "tests", Name: "Tag", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	source := models.Schema{AppLabel: "tests", Name: "Post", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ManyToManyField("tags", models.Relation{Target: target.Key(), RelatedName: "posts"})}}
	return source, target
}
func saveMap(t *testing.T, store *orm.Store, schema models.Schema, values map[string]any) *models.MapRecord {
	t.Helper()
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		if err := record.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(context.Background(), record, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAutomaticIntermediaryMigrationAndScopedDeletion(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial}}
	preview, err := engine.SQL(ctx, initial.Key(), false)
	if err != nil || len(preview) != 3 {
		t.Fatal(preview, err)
	}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{source, target} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	store.Registry = registry
	post := saveMap(t, store, source, map[string]any{"tenant": 1})
	tag := saveMap(t, store, target, map[string]any{"tenant": 2})
	postID, _ := post.Get("id")
	tagID, _ := tag.Get("id")
	field, _ := source.Field("tags")
	through, _ := models.ImplicitThrough(source, field, target)
	join := saveMap(t, store, through, map[string]any{"source_id": postID, "target_id": tagID})
	collector := orm.DeleteCollector{Store: store, Scope: func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.AutoCreatedBy != "" {
			t.Fatal("tenant scope called for internal intermediary")
		}
		return orm.Q("tenant", 1), nil
	}}
	if _, err := collector.Collect(ctx, join); err == nil {
		t.Fatal("automatic intermediary accepted as root")
	}
	plan, err := collector.Collect(ctx, post)
	if err != nil || len(plan.Objects) != 1 || len(plan.JoinRemovals) != 1 {
		t.Fatal(plan, err)
	}
	if plan.JoinRemovals[0].Endpoint.Schema().Key() != source.Key() || plan.JoinRemovals[0].Field != "source_id" {
		t.Fatal(plan)
	}
	denied := errors.New("policy denied")
	collector.Authorize = func(_ context.Context, plan orm.DeletionPlan) error {
		if len(plan.JoinRemovals) != 1 {
			t.Fatal(plan)
		}
		return denied
	}
	if _, err := collector.Execute(ctx, post); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	collector.Authorize = nil
	counts, err := collector.Execute(ctx, post)
	if err != nil || len(counts) != 1 || counts[source.Key()] != 1 {
		t.Fatal(counts, err)
	}
	for _, check := range []struct {
		schema models.Schema
		count  int64
	}{{target, 1}, {through, 0}} {
		count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(check.schema); return r }).Count(ctx)
		if err != nil || count != check.count {
			t.Fatal(check.schema.Key(), count, err)
		}
	}
	if err := engine.Reverse(ctx, "tests.zero"); err != nil {
		t.Fatal(err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 1 || tables[0] != "gogo_migrations" {
		t.Fatal(tables, err)
	}
}

func TestAutomaticIntermediaryAddAndRemoveField(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	field, _ := source.Field("tags")
	source.Fields = source.Fields[:2]
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	second := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.AddField(source, field)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, second}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 3 {
		t.Fatal(tables, err)
	}
}

func setupMany(t *testing.T, explicit bool) (*orm.Store, *models.MapRecord, []*models.MapRecord, models.Schema) {
	t.Helper()
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	schemas := []models.Schema{source, target}
	var through models.Schema
	if explicit {
		through = models.Schema{AppLabel: "tests", Name: "Membership", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("post", models.Relation{Target: source.Key(), OnDelete: models.Cascade}), models.ForeignKeyField("tag", models.Relation{Target: target.Key(), OnDelete: models.Cascade}), models.IntegerField("tenant"), models.CharField("note")}, Constraints: []models.Constraint{{Name: "membership_pair", Kind: "unique", Fields: []string{"post", "tag"}}}}
		source.Fields[2].Relation.Through = through.Key()
		source.Fields[2].Relation.ThroughFields = []string{"post", "tag"}
		schemas[0] = source
		schemas = append(schemas, through)
	} else {
		through, _ = models.ImplicitThrough(source, source.Fields[2], target)
	}
	operations := []migrations.Operation{}
	registry := &models.Registry{}
	for _, schema := range schemas {
		operations = append(operations, migrations.CreateModel(schema))
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: operations}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	post := saveMap(t, store, source, map[string]any{"tenant": 1})
	tags := []*models.MapRecord{}
	for _, tenant := range []int{1, 1, 2} {
		tags = append(tags, saveMap(t, store, target, map[string]any{"tenant": tenant}))
	}
	return store, post, tags, through
}

func TestManyToManyManagersAutomaticAndExplicit(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit"}[explicit], func(t *testing.T) {
			store, post, tags, through := setupMany(t, explicit)
			ctx := context.Background()
			scope := func(_ context.Context, schema models.Schema) (db.Predicate, error) {
				if schema.AutoCreatedBy != "" {
					t.Fatal("automatic join got tenant predicate")
				}
				return orm.Q("tenant", 1), nil
			}
			manager := orm.RelationManager{Store: store, Source: post, Name: "tags", Scope: scope}
			defaultsCalls := 0
			if explicit {
				if err := manager.Add(ctx, tags[0]); err == nil {
					t.Fatal("required intermediary values ignored")
				}
				manager.ThroughDefaults = orm.Defaults{"tenant": 1, "note": orm.DefaultFactory(func(context.Context) (any, error) { defaultsCalls++; return "joined", nil })}
			}
			post.State().Related = map[string]any{"tags": "stale"}
			if err := manager.Add(ctx, tags[0], tags[1], tags[0]); err != nil {
				t.Fatal(err)
			}
			if len(post.State().Related) != 0 {
				t.Fatal("source cache not invalidated")
			}
			if explicit && defaultsCalls != 1 {
				t.Fatal("through callable not once per batch", defaultsCalls)
			}
			if err := manager.Add(ctx, tags[0]); err != nil {
				t.Fatal("add not idempotent", err)
			}
			if explicit && defaultsCalls != 1 {
				t.Fatal("defaults called without insert", defaultsCalls)
			}
			rows, err := manager.All(ctx)
			if err != nil || len(rows) != 2 {
				t.Fatal(rows, err)
			}
			reverse := orm.RelationManager{Store: store, Source: tags[0], Name: "posts", Scope: scope}
			if rows, err := reverse.All(ctx); err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			if err := manager.Add(ctx, tags[2]); !errors.Is(err, orm.ErrNotFound) {
				t.Fatal("hidden target linked", err)
			}
			if err := manager.Remove(ctx, tags[0]); err != nil {
				t.Fatal(err)
			}
			if err := manager.Remove(ctx, tags[0]); err != nil {
				t.Fatal("missing remove not idempotent", err)
			}
			if err := manager.Set(ctx, tags[0]); err != nil {
				t.Fatal(err)
			}
			if rows, err := manager.All(ctx); err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			failure := errors.New("after relation failure")
			manager.AfterChange = []orm.RelationReceiver{func(context.Context, orm.RelationChange) error { return failure }}
			if err := manager.Clear(ctx); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if rows, err := manager.All(ctx); err != nil || len(rows) != 1 {
				t.Fatal("clear did not rollback", rows, err)
			}
			manager.AfterChange = nil
			if err := reverse.Clear(ctx); err != nil {
				t.Fatal(err)
			}
			if rows, err := manager.All(ctx); err != nil || len(rows) != 0 {
				t.Fatal(rows, err)
			}
			for _, schema := range []models.Schema{post.Schema(), tags[0].Schema()} {
				count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).Count(ctx)
				if err != nil || count == 0 {
					t.Fatal("clear deleted endpoint", count, err)
				}
			}
			count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(through); return r }).Count(ctx)
			if err != nil || count != 0 {
				t.Fatal("clear retained visible joins", count, err)
			}
		})
	}
}

func TestManyToManyHooksCannotMoveEndpointsOutsideScope(t *testing.T) {
	store, post, tags, _ := setupMany(t, false)
	ctx := context.Background()
	manager := orm.RelationManager{Store: store, Source: post, Name: "tags", Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }}
	// Identifiers preserve case; quote the exact trusted test table.
	manager.BeforeChange = []orm.RelationReceiver{func(ctx context.Context, change orm.RelationChange) error {
		id, _ := tags[0].Get("id")
		table, err := store.Backend.Dialect().QuoteIdentifier(tags[0].Schema().DBTable())
		if err != nil {
			return err
		}
		_, err = db.ExecutorFor(ctx, store.Backend).Exec(ctx, "UPDATE "+table+" SET tenant=2 WHERE id=$1", id)
		return err
	}}
	if err := manager.Add(ctx, tags[0]); !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("hook scope mutation accepted", err)
	}
	if rows, err := manager.All(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	visible, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(tags[0].Schema()); return r }).Filter(orm.Q("id", mustValue(t, tags[0], "id")), orm.Q("tenant", 1)).Exists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !visible {
		t.Fatal("hook did not rollback")
	}
}
func mustValue(t *testing.T, record models.Record, name string) any {
	t.Helper()
	value, err := record.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSelfManyToManySymmetryAndDirectedReverse(t *testing.T) {
	for _, symmetric := range []bool{true, false} {
		t.Run(map[bool]string{true: "symmetric", false: "directed"}[symmetric], func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			relation := models.Relation{Target: "tests.Person", RelatedName: "followers"}
			if !symmetric {
				relation.Symmetrical = &symmetric
			}
			schema := models.Schema{AppLabel: "tests", Name: "Person", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("friends", relation)}}
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
			first := saveMap(t, store, schema, nil)
			second := saveMap(t, store, schema, nil)
			a := orm.RelationManager{Store: store, Source: first, Name: "friends"}
			other := orm.RelationManager{Store: store, Source: second, Name: "friends"}
			if err := a.Add(ctx, second, first); err != nil {
				t.Fatal(err)
			}
			rows, err := other.All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if symmetric && len(rows) != 1 || !symmetric && len(rows) != 0 {
				t.Fatal(rows)
			}
			reverse := orm.RelationManager{Store: store, Source: second, Name: "followers"}
			rows, err = reverse.All(ctx)
			if symmetric && err == nil || !symmetric && (err != nil || len(rows) != 1) {
				t.Fatal(rows, err)
			}
			if err := a.Remove(ctx, second); err != nil {
				t.Fatal(err)
			}
			if rows, err := other.All(ctx); err != nil || len(rows) != 0 {
				t.Fatal("mirrored removal failed", rows, err)
			}
			if err := a.Clear(ctx); err != nil {
				t.Fatal(err)
			}
			if rows, err := a.All(ctx); err != nil || len(rows) != 0 {
				t.Fatal("self link retained", rows, err)
			}
		})
	}
}

func TestManyToManySetPreservesHiddenLinksAndBounds(t *testing.T) {
	store, post, tags, through := setupMany(t, false)
	ctx := context.Background()
	unscoped := orm.RelationManager{Store: store, Source: post, Name: "tags"}
	if err := unscoped.Add(ctx, tags[0], tags[1], tags[2]); err != nil {
		t.Fatal(err)
	}
	scoped := orm.RelationManager{Store: store, Source: post, Name: "tags", Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }}
	scoped.MaxObjects = 1
	if err := scoped.Clear(ctx); err == nil {
		t.Fatal("unbounded link selection accepted")
	}
	scoped.MaxObjects = 0
	if err := scoped.Set(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, err := scoped.All(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if rows, err := unscoped.All(ctx); err != nil || len(rows) != 1 {
		t.Fatal("hidden target link was removed", rows, err)
	}
	count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(through); return r }).Count(ctx)
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestManyToManyConcurrentAddAndExplicitCreationScope(t *testing.T) {
	t.Run("concurrent add", func(t *testing.T) {
		store, post, tags, through := setupMany(t, false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		results := make(chan error, 2)
		start := make(chan struct{})
		var ready sync.WaitGroup
		ready.Add(2)
		for range 2 {
			go func() {
				// Separate request-local records; never share mutable instance caches.
				source, _ := models.NewRecord(post.Schema())
				_ = source.Set("id", mustValue(t, post, "id"))
				target, _ := models.NewRecord(tags[0].Schema())
				_ = target.Set("id", mustValue(t, tags[0], "id"))
				manager := orm.RelationManager{Store: store, Source: source, Name: "tags"}
				ready.Done()
				<-start
				results <- manager.Add(ctx, target)
			}()
		}
		ready.Wait()
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(through); return r }).Count(ctx)
		if err != nil || count != 1 {
			t.Fatal("duplicate link", count, err)
		}
	})
	t.Run("explicit intermediary creation scope", func(t *testing.T) {
		store, post, tags, through := setupMany(t, true)
		ctx := context.Background()
		manager := orm.RelationManager{Store: store, Source: post, Name: "tags", Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }, ThroughDefaults: orm.Defaults{"tenant": 2, "note": "outside"}}
		if err := manager.Add(ctx, tags[0]); !errors.Is(err, orm.ErrNotFound) {
			t.Fatal("outside-scope join created", err)
		}
		count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(through); return r }).Count(ctx)
		if err != nil || count != 0 {
			t.Fatal("invalid join not rolled back", count, err)
		}
	})
}

func TestRelationCallbacksCannotSubstituteAuthorizedEndpoints(t *testing.T) {
	store, post, tags, _ := setupMany(t, false)
	ctx := context.Background()
	authorized := mustValue(t, tags[0], "id")
	manager := orm.RelationManager{Store: store, Source: post, Name: "tags", Authorize: func(_ context.Context, event orm.RelationChange) error {
		for _, record := range event.Added {
			if mustValue(t, record, "id") != authorized {
				return errors.New("wrong endpoint")
			}
		}
		return nil
	}}
	manager.BeforeChange = []orm.RelationReceiver{func(_ context.Context, event orm.RelationChange) error { event.Added[0] = tags[1]; return nil }}
	if err := manager.Add(ctx, tags[0]); err != nil {
		t.Fatal(err)
	}
	rows, err := manager.All(ctx)
	if err != nil || len(rows) != 1 || mustValue(t, rows[0], "id") != authorized {
		t.Fatal("callback replaced authorized endpoint", rows, err)
	}
	manager.BeforeChange = nil
	if err := manager.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	manager.BeforeChange = []orm.RelationReceiver{func(_ context.Context, event orm.RelationChange) error {
		return event.Added[0].Set("id", mustValue(t, tags[1], "id"))
	}}
	if err := manager.Add(ctx, tags[0]); err == nil {
		t.Fatal("callback rewrote endpoint identity")
	}
	if rows, err := manager.All(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestRelationAuthorizationUsesFreshPostHookState(t *testing.T) {
	store, post, tags, _ := setupMany(t, false)
	ctx := context.Background()
	denied := errors.New("target policy denied")
	calls := 0
	manager := orm.RelationManager{Store: store, Source: post, Name: "tags", Authorize: func(_ context.Context, event orm.RelationChange) error {
		calls++
		for _, target := range event.Added {
			value, _ := target.Get("tenant")
			if value != int64(1) {
				return denied
			}
		}
		return nil
	}, BeforeChange: []orm.RelationReceiver{func(ctx context.Context, event orm.RelationChange) error {
		table, err := store.Backend.Dialect().QuoteIdentifier(tags[0].Schema().DBTable())
		if err != nil {
			return err
		}
		_, err = db.ExecutorFor(ctx, store.Backend).Exec(ctx, "UPDATE "+table+" SET tenant=2 WHERE id=$1", mustValue(t, tags[0], "id"))
		return err
	}}}
	if err := manager.Add(ctx, tags[0]); !errors.Is(err, denied) {
		t.Fatal("stale object policy accepted", err)
	}
	if calls != 2 {
		t.Fatal("policy was not checked before and after hook", calls)
	}
	if rows, err := manager.All(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestSymmetricExplicitThroughRejectsInaccessibleMirror(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source := models.Schema{AppLabel: "tests", Name: "Person", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("friends", models.Relation{Target: "tests.Person", Through: "tests.Friendship", ThroughFields: []string{"from_id", "to_id"}})}}
	through := models.Schema{AppLabel: "tests", Name: "Friendship", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("from_id", models.Relation{Target: source.Key(), OnDelete: models.Cascade, RelatedName: "+"}), models.ForeignKeyField("to_id", models.Relation{Target: source.Key(), OnDelete: models.Cascade, RelatedName: "+"}), models.IntegerField("tenant")}, Constraints: []models.Constraint{{Name: "friendship_pair", Kind: "unique", Fields: []string{"from_id", "to_id"}}}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{source, through} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(through)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	first := saveMap(t, store, source, nil)
	second := saveMap(t, store, source, nil)
	firstID := mustValue(t, first, "id")
	secondID := mustValue(t, second, "id")
	saveMap(t, store, through, map[string]any{"from_id": firstID, "to_id": secondID, "tenant": 1})
	saveMap(t, store, through, map[string]any{"from_id": secondID, "to_id": firstID, "tenant": 2})
	manager := orm.RelationManager{Store: store, Source: first, Name: "friends", Scope: func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == through.Key() {
			return orm.Q("tenant", 1), nil
		}
		return db.Predicate{}, nil
	}}
	if err := manager.Remove(ctx, second); !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("removed only visible half", err)
	}
	if err := manager.Clear(ctx); !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("cleared only visible half", err)
	}
	count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(through); return r }).Count(ctx)
	if err != nil || count != 2 {
		t.Fatal("partial symmetric removal", count, err)
	}
}

func TestOppositeManyToManyLocksReturnTypedDeadlockWithoutPartialJoin(t *testing.T) {
	store, post, tags, through := setupMany(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, reverse := range []bool{false, true} {
		go func(reverse bool) {
			results <- db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
				source, target, name := post, tags[0], "tags"
				if reverse {
					source, target, name = tags[0], post, "posts"
				}
				fresh, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(source.Schema()); return r }).Filter(orm.Q("id", mustValue(t, source, "id"))).SelectForUpdate(false, false).Get(ctx)
				if err != nil {
					return err
				}
				ready <- struct{}{}
				<-start
				return (orm.RelationManager{Store: store, Source: fresh, Name: name}).Add(ctx, target)
			})
		}(reverse)
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(start)
	deadlocks, successes := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var backend *db.Error
		if !errors.As(err, &backend) || backend.Code != db.Deadlock {
			t.Fatal("unexpected lock error", err)
		}
		deadlocks++
	}
	if successes != 1 || deadlocks != 1 {
		t.Fatal(successes, deadlocks)
	}
	count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(through); return r }).Count(ctx)
	if err != nil || count != 1 {
		t.Fatal("partial or duplicate link", count, err)
	}
}
