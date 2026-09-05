package orm

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestReverseQueryNamesRemainSeparateFromManagerAccessors(t *testing.T) {
	for _, test := range []struct {
		name, query, accessor, forbidden string
		relation                         models.Relation
	}{
		{"defaults", "child", "child_set", "missing", models.Relation{}},
		{"related name", "children", "children", "child", models.Relation{RelatedName: "children"}},
		{"query override", "child_query", "children", "child", models.Relation{RelatedName: "children", RelatedQueryName: "child_query"}},
		{"query only", "child_query", "child_set", "child", models.Relation{RelatedQueryName: "child_query"}},
		{"hidden", "", "", "private+", models.Relation{RelatedName: "private+"}},
		{"hidden query", "child_query", "", "private+", models.Relation{RelatedName: "private+", RelatedQueryName: "child_query"}},
	} {
		for _, kind := range []models.Kind{models.ForeignKey, models.OneToOne, models.ManyToMany} {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				_, base := updateFixture()
				relation := test.relation
				relation.Target, relation.OnDelete = base.schema.Key(), models.Cascade
				field := models.ForeignKeyField("parent", relation)
				if kind == models.OneToOne {
					field = models.OneToOneField("parent", relation)
				}
				if kind == models.ManyToMany {
					field = models.ManyToManyField("parent", relation)
				}
				accessor := test.accessor
				if kind == models.OneToOne && accessor == "child_set" {
					accessor = "child"
				}
				child := models.Schema{AppLabel: "tests", Name: "Child", Fields: []models.Field{models.BigAutoField("id"), field}}
				registry := &models.Registry{}
				for _, schema := range []models.Schema{base.schema, child} {
					if err := registry.Register(schema); err != nil {
						t.Fatal(err)
					}
				}
				if err := registry.Freeze(); err != nil {
					t.Fatal(err)
				}
				base.store.Registry = registry
				prototype, _ := models.NewRecord(base.schema)
				manager := RelationManager{Store: base.store, Source: prototype}
				for _, name := range []string{"child", "child_set", "children", "child_query", test.forbidden} {
					manager.Name = name
					binding, err := manager.resolveQuery()
					if (err == nil) != (name == test.query) {
						t.Fatal("query namespace", name, err)
					}
					if err == nil && (!binding.reverse || binding.target.Key() != child.Key()) {
						t.Fatal("wrong query relation")
					}
					_, err = manager.resolve()
					if (err == nil) != (name == accessor) {
						t.Fatal("accessor namespace", name, err)
					}
				}
			})
		}
	}
}

func TestReverseQueryNameAmbiguityAndStoredFieldsFailBeforeSQL(t *testing.T) {
	_, base := updateFixture()
	registry := &models.Registry{}
	if err := registry.Register(base.schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Left", "Right"} {
		schema := models.Schema{AppLabel: "tests", Name: name, Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: base.schema.Key(), OnDelete: models.Cascade, RelatedName: name, RelatedQueryName: "children"})}}
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	base.store.Registry = registry
	query := base.Annotate(map[string]ResultExpression{"count": Typed(Count(F("children__id")), models.BigIntegerField("out"))})
	if _, _, err := query.SQLContext(context.Background()); err == nil {
		t.Fatal("ambiguous query relation accepted")
	}
	prototype, _ := models.NewRecord(base.schema)
	if _, err := (RelationManager{Store: base.store, Source: prototype, Name: "value"}).resolveQuery(); err == nil {
		t.Fatal("scalar stored field used as reverse relation")
	}
}

func TestReverseQueryNamesCannotAliasAnAmbiguousEagerAccessor(t *testing.T) {
	_, base := updateFixture()
	registry := &models.Registry{}
	if err := registry.Register(base.schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"left", "right"} {
		schema := models.Schema{AppLabel: "tests", Name: name, Fields: []models.Field{models.BigAutoField("id"), models.OneToOneField("parent", models.Relation{Target: base.schema.Key(), OnDelete: models.Cascade, RelatedName: "same_cache", RelatedQueryName: name})}}
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	base.store.Registry = registry
	for _, name := range []string{"left", "right"} {
		if _, _, err := base.SelectRelated(name).SQL(); err == nil {
			t.Fatal("distinct query name accepted ambiguous accessor cache", name)
		}
	}
}
