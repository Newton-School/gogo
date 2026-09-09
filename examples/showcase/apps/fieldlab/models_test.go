package fieldlab_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestEveryModelKindAndConstructorHasAnHonestDemonstration(t *testing.T) {
	kinds := []models.Kind{
		models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger,
		models.SmallAuto, models.Auto, models.BigAuto, models.UUID, models.Decimal, models.Float, models.Boolean,
		models.Char, models.Text, models.Slug, models.Email, models.URL, models.GenericIPAddress, models.FilePath,
		models.Date, models.DateTime, models.Time, models.Duration, models.Binary, models.JSON, models.File, models.Image,
		models.ForeignKey, models.OneToOne, models.ManyToMany, models.Generated, models.Array, models.HStore, models.Range,
		models.SearchVector, models.Geometry, models.Geography, models.Raster, models.Custom,
	}
	cases := fieldlab.ModelCases()
	if len(cases) != len(kinds) {
		t.Fatalf("model inventory: got %d, want %d", len(cases), len(kinds))
	}
	seen := map[models.Kind]bool{}
	for _, example := range cases {
		t.Run(example.Constructor, func(t *testing.T) {
			if seen[example.Field.Kind] {
				t.Fatal("duplicate model kind")
			}
			seen[example.Field.Kind] = true
			if example.DescriptorOnly {
				if example.Limitation == "" {
					t.Fatal("descriptor-only case must disclose its boundary")
				}
			} else {
				if _, err := example.Field.Clean(t.Context(), example.Valid); err != nil {
					t.Fatalf("valid example rejected: %v", err)
				}
				if _, err := example.Field.Clean(t.Context(), example.Invalid); err == nil {
					t.Fatal("invalid example was accepted")
				}
			}
			fields := []models.Field{example.Field}
			if !example.Field.PrimaryKey {
				fields = append([]models.Field{models.BigAutoField("id")}, fields...)
			}
			schema := models.Schema{AppLabel: "fieldlab", Name: "Declaration", Fields: fields}
			if err := schema.Validate(); err != nil {
				t.Fatalf("schema rejected: %v", err)
			}
			if example.Field.Kind == models.ManyToMany {
				if example.Field.IsStored() {
					t.Fatal("many-to-many cannot be a stored scalar")
				}
				return
			}
			native, err := (postgres.Dialect{}).FieldType(example.Field)
			switch example.Field.Kind {
			case models.Custom:
				if err == nil {
					t.Fatal("stock PostgreSQL dialect must reject unmapped custom kind")
				}
			case models.HStore, models.Geometry, models.Geography, models.Raster:
				if err == nil {
					t.Fatal("extension-gated kind must fail without a capability")
				}
				capable := postgres.Dialect{Capabilities: db.Capabilities{"extension:hstore": true, "extension:postgis": true}}
				if native, err = capable.FieldType(example.Field); err != nil || native == "" {
					t.Fatalf("enabled extension mapping: %q %v", native, err)
				}
			default:
				if err != nil || native == "" {
					t.Fatalf("PostgreSQL type mapping: %q %v", native, err)
				}
			}
		})
	}
	for _, kind := range kinds {
		if !seen[kind] {
			t.Errorf("missing model kind %s", kind)
		}
	}
}

func TestMigratableSchemasFactoriesAndScalarFixture(t *testing.T) {
	registry := &models.Registry{}
	for _, schema := range fieldlab.Schemas() {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		factory := fieldlab.Factories()[schema.Key()]
		if factory == nil {
			t.Fatalf("missing factory %s", schema.Key())
		}
		if _, err := models.Bind(factory()); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	record, err := fieldlab.NewSpecimen()
	if err != nil {
		t.Fatal(err)
	}
	if err := models.FullClean(t.Context(), record, models.CleanOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if record.State().Persisted {
		t.Fatal("fixture creation must not claim database persistence")
	}
	for _, field := range record.Schema().Fields {
		value, err := record.Get(field.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := field.Validate(t.Context(), value); err != nil {
			t.Fatalf("%s: %v", field.Name, err)
		}
	}
}

func TestFieldOptionsNullBlankChoicesDefaultsAndCancellation(t *testing.T) {
	field := models.CharField("status", models.WithColumn("record_status"), models.WithStructField("Status"),
		models.WithChoices(models.Choice{Value: "draft", Label: "Draft"}), models.WithLabel("Publication status"),
		models.WithHelpText("Only draft is accepted in this demonstration."), models.WithDefault("draft"),
		models.WithMaxLength(20), models.WithMinLength(2), models.WithDBIndex(true), models.UniqueValue)
	if field.DBColumn() != "record_status" || field.GoField() != "Status" || !field.HasDefault() || !field.DBIndex || !field.Unique {
		t.Fatal("field options were not applied")
	}
	if err := field.Validate(t.Context(), "draft"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, "", "published"} {
		if err := field.Validate(t.Context(), value); err == nil {
			t.Fatalf("invalid choice/presence accepted: %v", value)
		}
	}
	nullable := models.CharField("optional", models.Nullable, models.Optional, models.ReadOnly)
	if nullable.IsEditable() || nullable.Validate(t.Context(), nil) != nil || nullable.Validate(t.Context(), "") != nil {
		t.Fatal("nullable, blank and read-only are independent declaration options")
	}
	unicode := models.SlugField("slug", models.WithAllowUnicode(true))
	if unicode.Validate(t.Context(), "こんにちは") != nil || models.SlugField("slug").Validate(t.Context(), "こんにちは") == nil {
		t.Fatal("Unicode slug opt-in not demonstrated")
	}
	bounded := models.IntegerField("bounded", models.WithBounds(2, 5), models.WithValidators(func(_ context.Context, value any) error {
		if value == int64(3) {
			return models.Invalid("reserved", "Three is reserved.")
		}
		return nil
	}))
	if bounded.Validate(t.Context(), 4) != nil || bounded.Validate(t.Context(), 1) == nil || bounded.Validate(t.Context(), 3) == nil {
		t.Fatal("bounds or custom validator failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if bounded.Validate(ctx, 4) == nil {
		t.Fatal("canceled validation accepted")
	}
	calls := 0
	defaulted := models.CharField("title", models.WithDefaultFunc("fieldlab.title.v1", func() any { calls++; return "Example" }))
	record, err := models.NewRecord(models.Schema{AppLabel: "fieldlab", Name: "Default", Fields: []models.Field{models.BigAutoField("id"), defaulted}})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ApplyDefaults(record); err != nil {
		t.Fatal(err)
	}
	if err := models.ApplyDefaults(record); err != nil || calls != 1 {
		t.Fatalf("default must run once: %d %v", calls, err)
	}
}

func TestCatalogIsJSONSafeAndDisclosesUnimplementedWidgets(t *testing.T) {
	data, err := json.Marshal(fieldlab.Catalog())
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"ClearableFileInput", "SelectDateWidget", "not_implemented", "NewField(Custom)"} {
		if !strings.Contains(string(data), phrase) {
			t.Errorf("missing limitation %q", phrase)
		}
	}
}
