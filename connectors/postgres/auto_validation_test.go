package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type nullableAutoID struct {
	models.Base
	Definition models.Schema
	ID         *int64
	Code       string
}

func (r *nullableAutoID) Schema() models.Schema { return r.Definition }

type scalarAutoID struct {
	models.Base
	Definition models.Schema
	ID         int64
	Code       string
}

func (r *scalarAutoID) Schema() models.Schema { return r.Definition }

func TestFullCleanUnallocatedAutomaticPrimaryKeys(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	for _, field := range []models.Field{models.SmallAutoField("id"), models.AutoField("id"), models.BigAutoField("id")} {
		t.Run(string(field.Kind), func(t *testing.T) {
			field.StructField = "ID"
			schema := models.Schema{AppLabel: "checks", Name: string(field.Kind), Fields: []models.Field{field, models.CharField("code", models.WithStructField("Code"), models.UniqueValue)}}
			if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			store := orm.New(b, nil)
			hooks := 0
			store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { hooks++; return nil }}
			query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record })
			for i, representation := range []string{"map", "pointer", "scalar"} {
				t.Run(representation, func(t *testing.T) {
					var model models.Model
					switch representation {
					case "map":
						model, _ = models.NewRecord(schema)
					case "pointer":
						model = &nullableAutoID{Definition: schema}
					default:
						model = &scalarAutoID{Definition: schema}
					}
					record, err := models.Bind(model)
					if err != nil {
						t.Fatal(err)
					}
					if err := record.Set("code", representation); err != nil {
						t.Fatal(err)
					}
					beforeHooks := hooks
					for range 2 {
						if err := models.FullClean(ctx, record, models.CleanOptions{}, store); err != nil {
							t.Fatal("FullClean rejected a database-generated identity", err)
						}
					}
					if hooks != beforeHooks || !record.State().Adding() || !models.IsEmptyValue(mustValue(t, record, "id")) {
						t.Fatal("validation performed Save work")
					}
					if count, err := query.Count(ctx); err != nil || count != int64(i) {
						t.Fatal("validation inserted a row", count, err)
					}
					if err := store.Save(ctx, model, orm.SaveOptions{ForceUpdate: true}); err == nil {
						t.Fatal("ForceUpdate accepted an unallocated identity")
					}
					if err := store.Save(ctx, model, orm.SaveOptions{}); err != nil {
						t.Fatal("INSERT did not allocate an identity after validation", err)
					}
					id := mustValue(t, record, "id")
					if models.IsEmptyValue(id) || record.State().Adding() {
						t.Fatal("successful INSERT did not return its generated identity")
					}
					if err := models.FullClean(ctx, record, models.CleanOptions{}, store); err != nil {
						t.Fatal("persisted record failed revalidation", err)
					}
					duplicate, err := models.NewRecord(schema)
					if err != nil {
						t.Fatal(err)
					}
					if err := duplicate.Set("id", id); err != nil {
						t.Fatal(err)
					}
					if err := duplicate.Set("code", representation+"_duplicate"); err != nil {
						t.Fatal(err)
					}
					var validation *models.ValidationError
					if err := models.FullClean(ctx, duplicate, models.CleanOptions{}, store); !errors.As(err, &validation) || len(validation.Fields["id"]) == 0 {
						t.Fatal("explicit automatic identity collision bypassed uniqueness", err)
					}
					if count, err := query.Count(ctx); err != nil || count != int64(i+1) {
						t.Fatal("unexpected persisted row count", count, err)
					}
				})
			}
		})
	}
}
