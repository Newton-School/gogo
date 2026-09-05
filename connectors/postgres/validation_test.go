package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"testing"
	"time"
)

type validatedRow struct {
	models.Base
	Definition models.Schema
	ID         int64
	Code       *string
	Enabled    bool
	Weight     int32
	Published  time.Time
}

func (r *validatedRow) Schema() models.Schema { return r.Definition }
func validatedSchema() models.Schema {
	return models.Schema{AppLabel: "checks", Name: "Row", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("code", models.WithStructField("Code"), models.Nullable, models.Optional, models.WithMinLength(3)), models.BooleanField("enabled", models.WithStructField("Enabled")), models.IntegerField("weight", models.WithStructField("Weight")), models.DateTimeField("published", models.WithStructField("Published"))}}
}

func TestConditionalUniqueNullsAndValidationExclusions(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := validatedSchema()
	distinct := false
	schema.Constraints = []models.Constraint{{Name: "active_code", Kind: "unique", Fields: []string{"code"}, Condition: "enabled", NullsDistinct: &distinct}, {Name: "positive_weight", Kind: "check", Expression: "weight >= 0"}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	code := "alpha"
	save := func(enabled bool, code *string) *validatedRow {
		r := &validatedRow{Definition: schema, Enabled: enabled, Code: code, Published: time.Now()}
		if err := store.Save(ctx, r, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	save(false, &code)
	save(false, &code)
	existing := save(true, &code)
	candidate := &validatedRow{Definition: schema, Enabled: true, Code: &code, Published: time.Now()}
	record, _ := models.Bind(candidate)
	if err := models.FullClean(ctx, record, models.CleanOptions{}, store); err == nil {
		t.Fatal("conditional duplicate accepted")
	}
	candidate.Enabled = false
	if err := models.FullClean(ctx, record, models.CleanOptions{}, store); err != nil {
		t.Fatal("false condition incorrectly unique", err)
	}
	save(true, nil)
	candidate.Code = nil
	candidate.Enabled = true
	if err := store.ValidateConstraints(ctx, record, nil); err == nil {
		t.Fatal("NULLS NOT DISTINCT duplicate accepted")
	}
	if err := store.Save(ctx, candidate, orm.SaveOptions{}); !db.IsCode(err, db.UniqueViolation) {
		t.Fatal("database null uniqueness disagrees", err)
	}
	bad := "x"
	candidate.Code = &bad
	candidate.Weight = -1
	var validation *models.ValidationError
	if err := models.FullClean(ctx, record, models.CleanOptions{}, store); !errors.As(err, &validation) || len(validation.Fields["code"]) == 0 || len(validation.Fields[models.NonFieldErrors]) == 0 {
		t.Fatal("unrelated field error skipped check", validation, err)
	}
	candidate.ID = existing.ID
	if err := store.ValidateUnique(ctx, record, []string{"code"}); err == nil {
		t.Fatal("explicit new-instance auto PK collision accepted")
	}
}

func TestDateScopedUniquenessUsesCurrentTimezone(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := validatedSchema()
	schema.Fields[1].UniqueForDate = "published"
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	code := "news"
	existing := &validatedRow{Definition: schema, Code: &code, Published: time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)}
	if err := store.Save(ctx, existing, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	candidate := &validatedRow{Definition: schema, Code: &code, Published: time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC)}
	record, _ := models.Bind(candidate)
	if err := store.ValidateUnique(ctx, record, nil); err != nil {
		t.Fatal("different UTC days rejected", err)
	}
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	store.Timezone = func(context.Context) *time.Location { return location }
	if err := store.ValidateUnique(ctx, record, nil); err == nil {
		t.Fatal("same local date accepted")
	}
	if err := store.ValidateUnique(ctx, record, []string{"published"}); err != nil {
		t.Fatal("excluded date was checked", err)
	}
	schema.Fields[1].UniqueForDate = ""
	schema.Fields[1].UniqueForMonth = "published"
	candidate.Definition = schema
	candidate.Published = time.Date(2027, 9, 15, 0, 0, 0, 0, time.UTC)
	record, _ = models.Bind(candidate)
	if err := store.ValidateUnique(ctx, record, nil); err == nil {
		t.Fatal("same month across years accepted")
	}
}
