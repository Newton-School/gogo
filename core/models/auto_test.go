package models_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type autoPointerModel struct {
	models.Base
	Definition models.Schema
	ID         *int64
}

func (m *autoPointerModel) Schema() models.Schema { return m.Definition }

func TestAutoFieldCleanAllowsUnallocatedIdentityOnly(t *testing.T) {
	ctx := context.Background()
	for _, field := range []models.Field{models.SmallAutoField("id"), models.AutoField("id"), models.BigAutoField("id")} {
		t.Run(string(field.Kind), func(t *testing.T) {
			if field.Null || field.IsEditable() {
				t.Fatal("automatic identity must remain nonnullable and noneditable")
			}
			value, err := field.Clean(ctx, nil)
			if err != nil || value != nil {
				t.Fatalf("unallocated identity rejected or generated during validation: %v, %v", value, err)
			}
			for _, invalid := range []any{"", "invalid", 1.5, uint64(math.MaxUint64)} {
				if _, err := field.Clean(ctx, invalid); err == nil {
					t.Fatalf("invalid supplied identity accepted: %v", invalid)
				}
			}
			value, err = field.Clean(ctx, int64(27))
			if err != nil || value != int64(27) {
				t.Fatal("supplied identity was not preserved", value, err)
			}
			if field.Kind != models.BigAuto {
				if _, err := field.Clean(ctx, int64(math.MaxInt32)+1); err == nil {
					t.Fatal("supplied identity overflow accepted")
				}
			}
			schema := models.Schema{AppLabel: "test", Name: "Auto", Fields: []models.Field{field}}
			mapped, err := models.NewRecord(schema)
			if err != nil {
				t.Fatal(err)
			}
			pointerSchema := schema.Clone()
			pointerSchema.Fields[0].StructField = "ID"
			pointer := &autoPointerModel{Definition: pointerSchema}
			bound, err := models.Bind(pointer)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range []models.Record{mapped, bound} {
				for range 2 {
					if err := models.FullClean(ctx, record, models.CleanOptions{}, nil); err != nil {
						t.Fatal("unsaved identity failed repeated FullClean", err)
					}
					value, err := record.Get("id")
					if err != nil || value != nil || !record.State().Adding() {
						t.Fatal("FullClean allocated or persisted an identity", value, err)
					}
				}
			}
		})
	}
	for _, field := range []models.Field{models.BigIntegerField("id", models.Primary), models.UUIDField("id", models.Primary), models.CharField("id", models.Primary)} {
		_, err := field.Clean(ctx, nil)
		var validation models.FieldError
		if !errors.As(err, &validation) || validation.Code != "null" {
			t.Fatal("non-generated primary key accepted NULL", field.Kind, err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := models.BigAutoField("id").Clean(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("unallocated identity ignored cancellation", err)
	}
}
