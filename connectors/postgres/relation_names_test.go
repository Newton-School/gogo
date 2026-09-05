package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestDefaultOneToOneNamesScopeManagersSelectRelatedAndPrefetch(t *testing.T) {
	for _, override := range []string{"", "profile_query"} {
		t.Run("query="+override, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			root := models.Schema{AppLabel: "tests", Name: "DefaultRoot", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
			profile := models.Schema{AppLabel: "tests", Name: "DefaultProfile", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.OneToOneField("root", models.Relation{Target: root.Key(), RelatedQueryName: override, OnDelete: models.Cascade})}}
			registry := &models.Registry{}
			for _, schema := range []models.Schema{root, profile} {
				if err := registry.Register(schema); err != nil {
					t.Fatal(err)
				}
			}
			if err := registry.Freeze(); err != nil {
				t.Fatal(err)
			}
			engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(root), migrations.CreateModel(profile)}}}}
			if err := engine.Apply(ctx, ""); err != nil {
				t.Fatal(err)
			}
			store := orm.New(b, registry)
			first := saveMap(t, store, root, map[string]any{"tenant": 1})
			second := saveMap(t, store, root, map[string]any{"tenant": 1})
			visible := saveMap(t, store, profile, map[string]any{"tenant": 1, "root": mustValue(t, first, "id")})
			saveMap(t, store, profile, map[string]any{"tenant": 9, "root": mustValue(t, second, "id")})
			counted := &countedBackend{Backend: b}
			store.Backend = counted
			scope := func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }
			manager := orm.RelationManager{Store: store, Source: first, Name: "defaultprofile", Scope: scope}
			value, err := manager.One(ctx)
			if err != nil || mustValue(t, value, "id") != mustValue(t, visible, "id") {
				t.Fatal("default one-to-one accessor", value, err)
			}
			manager.Source = second
			if _, err := manager.One(ctx); !errors.Is(err, orm.ErrNotFound) {
				t.Fatal("default accessor exposed hidden one-to-one", err)
			}
			queryName := override
			if queryName == "" {
				queryName = "defaultprofile"
			}
			base := orm.For(store, func() *models.MapRecord { value, _ := models.NewRecord(root); return value }).WithScope(scope).OrderBy("id")
			for _, selected := range []bool{false, true} {
				query := base.PrefetchRelated("defaultprofile")
				if selected {
					query = query.SelectRelated(queryName)
				}
				counted.queries.Store(0)
				rows, err := query.All(ctx)
				want := int32(2)
				if selected {
					want = 1
				}
				if err != nil || len(rows) != 2 || counted.queries.Load() != want {
					t.Fatal("default cache query count", err, counted.queries.Load())
				}
				for index, row := range rows {
					loaded, ok := orm.RelatedOne(row, "defaultprofile")
					if !ok || (loaded == nil) != (index == 1) {
						t.Fatal("default O2O cache presence", index)
					}
					if loaded != nil && mustValue(t, loaded, "id") != mustValue(t, visible, "id") {
						t.Fatal("wrong default O2O cache")
					}
					if _, ok := orm.RelatedOne(row, "defaultprofile_set"); ok {
						t.Fatal("collection suffix used for O2O cache")
					}
				}
			}
			for _, name := range []string{"defaultprofile_set", "profile_query"} {
				counted.queries.Store(0)
				manager.Name = name
				if _, err := manager.One(ctx); err == nil || counted.queries.Load() != 0 {
					t.Fatal("incorrect O2O accessor accepted", name, err)
				}
				if _, err := base.PrefetchRelated(name).All(ctx); err == nil || counted.queries.Load() != 0 {
					t.Fatal("incorrect O2O prefetch accessor accepted", name, err)
				}
			}
		})
	}
}

func TestReverseQueryNamesPreserveScopedManagersAndEagerCaches(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	root := models.Schema{AppLabel: "tests", Name: "NamedRoot", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	related := func(name, accessor, query string, one bool) models.Schema {
		relation := models.Relation{Target: root.Key(), OnDelete: models.Cascade, RelatedName: accessor, RelatedQueryName: query}
		field := models.ForeignKeyField("root", relation)
		if one {
			field = models.OneToOneField("root", relation)
		}
		return models.Schema{AppLabel: "tests", Name: name, Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), field}}
	}
	child := related("NamedChild", "children", "child", false)
	note := related("Note", "", "", false)
	profile := related("NamedProfile", "profile", "profile_row", true)
	shadow := related("NamedShadow", "profile_row", "profile", true)
	private := related("NamedPrivate", "private+", "private_row", true)
	registry := &models.Registry{}
	operations := []migrations.Operation{}
	for _, schema := range []models.Schema{root, child, note, profile, shadow, private} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, migrations.CreateModel(schema))
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: operations}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	first := saveMap(t, store, root, map[string]any{"tenant": 1})
	second := saveMap(t, store, root, map[string]any{"tenant": 1})
	hiddenRoot := saveMap(t, store, root, map[string]any{"tenant": 9})
	for _, schema := range []models.Schema{child, note, profile, shadow, private} {
		for index, parent := range []*models.MapRecord{first, second, hiddenRoot} {
			tenant := 1
			if index == 1 {
				tenant = 9
			}
			saveMap(t, store, schema, map[string]any{"root": mustValue(t, parent, "id"), "tenant": tenant})
		}
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	scope := func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }
	base := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(root); return record }).WithScope(scope).OrderBy("id")
	integer := models.BigIntegerField("out")
	query := base.Annotate(map[string]orm.ResultExpression{
		"child_count":   orm.Typed(orm.Count(orm.F("child__id")), integer),
		"note_count":    orm.Typed(orm.Count(orm.F("note__id")), integer),
		"private_count": orm.Typed(orm.Count(orm.F("private_row__id")), integer),
	})
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 1 {
		t.Fatal(rows, err)
	}
	for index, row := range rows {
		for _, name := range []string{"child_count", "note_count", "private_count"} {
			if row.State().Annotations[name] != int64(1-index) {
				t.Fatal("reverse name escaped scope", name, row.State().Annotations)
			}
		}
	}
	for _, accessor := range []string{"children", "note_set"} {
		manager := orm.RelationManager{Store: store, Source: first, Name: accessor, Scope: scope}
		values, err := manager.All(ctx)
		if err != nil || len(values) != 1 {
			t.Fatal("instance accessor changed", accessor, values, err)
		}
		rows, err := base.PrefetchRelated(accessor).All(ctx)
		if err != nil || len(rows) != 2 {
			t.Fatal(err)
		}
		for index, row := range rows {
			values, ok := orm.RelatedMany(row, accessor)
			if !ok || len(values) != 1-index {
				t.Fatal("prefetch accessor escaped scope", accessor, ok, values)
			}
		}
	}
	for _, queryName := range []string{"child", "note", "private_row", "private+"} {
		counted.queries.Store(0)
		if _, err := (orm.RelationManager{Store: store, Source: first, Name: queryName, Scope: scope}).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("query name became an accessor", queryName, err)
		}
	}
	for _, accessor := range []string{"children", "note_set"} {
		counted.queries.Store(0)
		if _, err := base.Annotate(map[string]orm.ResultExpression{"n": orm.Typed(orm.Count(orm.F(accessor+"__id")), integer)}).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("accessor accepted as alternate query name", accessor, err)
		}
	}
	// The SQL query name 'profile_row' resolves NamedProfile, while the
	// accessor 'profile_row' belongs to NamedShadow. Neither namespace may be
	// substituted for the other, including aggregate join reuse and caching.
	counted.queries.Store(0)
	eager := base.SelectRelated("profile_row__root").Annotate(map[string]orm.ResultExpression{"shadow_count": orm.Typed(orm.Count(orm.F("profile__id")), integer)})
	rows, err = eager.PrefetchRelated("profile").All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 1 {
		t.Fatal("eager accessor/query join reuse", rows, err, counted.queries.Load())
	}
	for index, row := range rows {
		loaded, ok := orm.RelatedOne(row, "profile")
		if !ok || (loaded == nil) != (index == 1) {
			t.Fatal("wrong eager accessor presence", index)
		}
		if loaded != nil {
			if loaded.Schema().Key() != profile.Key() {
				t.Fatal("query name selected wrong reverse edge")
			}
			parent, ok := orm.RelatedOne(loaded, "root")
			if !ok || parent == nil || mustValue(t, parent, "id") != mustValue(t, row, "id") {
				t.Fatal("nested cache path lost parent identity")
			}
		}
		if _, ok := orm.RelatedOne(row, "profile_row"); ok {
			t.Fatal("SQL path polluted unrelated accessor cache")
		}
		if row.State().Annotations["shadow_count"] != int64(1-index) {
			t.Fatal("same spelling reused the wrong join")
		}
	}
	for _, path := range []string{"profile", "profile__root"} {
		counted.queries.Store(0)
		if _, err := eager.Prefetch(orm.Prefetch{Path: path, Where: orm.Q("tenant", 1)}).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("renamed eager/custom prefetch conflict missed", path, err)
		}
	}
	for _, spec := range []orm.Prefetch{
		{Path: "profile_row", ToAttr: "profile"},
		{Path: "profile__root__profile_row", ToAttr: "profile"},
	} {
		counted.queries.Store(0)
		if _, err := base.SelectRelated("profile_row__root__profile_row").Prefetch(spec).All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("ToAttr overwrote selected destination cache", spec.Path, err)
		}
	}
	// A genuinely separate destination remains supported and does not replace
	// the selected accessor, even when both accessor/query spellings cross.
	rows, err = base.SelectRelated("profile_row").Prefetch(orm.Prefetch{Path: "profile_row", ToAttr: "separate_shadow"}).All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal("independent ToAttr rejected", err)
	}
	selected, selectedOK := orm.RelatedOne(rows[0], "profile")
	separate, separateOK := orm.RelatedOne(rows[0], "separate_shadow")
	if !selectedOK || !separateOK || selected == nil || separate == nil || selected.Schema().Key() != profile.Key() || separate.Schema().Key() != shadow.Key() {
		t.Fatal("separate cache lost relation identity")
	}
	counted.queries.Store(0)
	if _, err := base.SelectRelated("private_row").All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("hidden accessor eagerly exposed", err)
	}
	rows, err = base.SelectRelated("profile").All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	loaded, ok := orm.RelatedOne(rows[0], "profile_row")
	if !ok || loaded == nil || loaded.Schema().Key() != shadow.Key() {
		t.Fatal("second reverse namespace wrong")
	}
}
