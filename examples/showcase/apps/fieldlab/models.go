// Package fieldlab is an executable catalog of the release's field APIs.
// It keeps validation demonstrations separate from database-backed records.
package fieldlab

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Newton-School/gogo/core/models"
)

// ModelCase supplies both a descriptor and explicit validation examples.
// DescriptorOnly means Clean has no kind-specific validation implementation;
// a declared kind must not be mistaken for a complete storage/UI feature.
type ModelCase struct {
	Constructor    string
	Field          models.Field
	Valid, Invalid any
	DescriptorOnly bool
	Limitation     string
}

// ModelCases calls every named model field constructor and NewField for each
// remaining declared kind. Callers receive fresh descriptor graphs each time.
func ModelCases() []ModelCase {
	relation := models.Relation{Target: "fieldlab.Specimen", OnDelete: models.Protect}
	cases := []ModelCase{
		{"SmallIntegerField", models.SmallIntegerField("small_integer"), int16(-12), 32768, false, ""},
		{"IntegerField", models.IntegerField("integer"), int32(-42), int64(2147483648), false, ""},
		{"BigIntegerField", models.BigIntegerField("big_integer"), int64(9007199254740993), "9223372036854775808", false, ""},
		{"PositiveSmallIntegerField", models.PositiveSmallIntegerField("positive_small_integer"), int16(12), -1, false, ""},
		{"PositiveIntegerField", models.PositiveIntegerField("positive_integer"), int32(42), -1, false, ""},
		{"PositiveBigIntegerField", models.PositiveBigIntegerField("positive_big_integer"), int64(9007199254740993), -1, false, ""},
		{"SmallAutoField", models.SmallAutoField("small_auto"), nil, 32768, false, "Separate table; the database allocates an omitted identity."},
		{"AutoField", models.AutoField("auto"), nil, int64(2147483648), false, "Separate table; the database allocates an omitted identity."},
		{"BigAutoField", models.BigAutoField("id"), nil, "9223372036854775808", false, "Specimen primary key; the database allocates an omitted identity."},
		{"UUIDField", models.UUIDField("uuid"), "12345678-1234-1234-1234-123456789abc", "not-a-uuid", false, ""},
		{"DecimalField", models.DecimalField("decimal", 12, 2), "1234567890.25", "1.001", false, "Base-10 value stays a string, never a float."},
		{"FloatField", models.FloatField("float"), 1.25, "NaN", false, "Approximate floating point; not for money."},
		{"BooleanField", models.BooleanField("boolean"), false, "perhaps", false, "False is a valid model value, distinct from absence."},
		{"CharField", models.CharField("char", models.WithMaxLength(120), models.WithMinLength(2)), "Field specimen", "x", false, ""},
		{"TextField", models.TextField("text"), "Long-form sample text.", 123, false, ""},
		{"SlugField", models.SlugField("slug"), "field-specimen", "has spaces", false, "ASCII by default; the Unicode option has a separate test."},
		{"EmailField", models.EmailField("email"), "developer@example.com", "Developer <developer@example.com>", false, "No email is sent by validation."},
		{"URLField", models.URLField("url"), "https://example.com/showcase", "javascript:alert(1)", false, "No remote URL is fetched."},
		{"GenericIPAddressField", models.GenericIPAddressField("ip_address"), "2001:db8::1", "999.1.1.1", false, "Documentation-only IP address."},
		{"FilePathField", models.FilePathField("file_path"), "documents/example.txt", 123, false, "String validation only; no root-scoped filesystem enumeration is implemented here."},
		{"DateField", models.DateField("date"), "2026-01-02", "2026-02-30", false, ""},
		{"DateTimeField", models.DateTimeField("datetime"), "2026-01-02T03:04:05Z", "not-a-date", false, "Stored as a UTC instant."},
		{"TimeField", models.TimeField("time"), "03:04:05.123456", "25:00:00", false, "Wall-clock time, not an instant."},
		{"DurationField", models.DurationField("duration"), time.Hour + 2*time.Microsecond, "forever", false, "PostgreSQL interval persistence uses microsecond precision."},
		{"BinaryField", models.BinaryField("binary"), []byte{0, 1, 2, 255}, "not-bytes", false, "No automatic form mapping; the sample Admin treats this as read-only."},
		{"JSONField", models.JSONField("json"), map[string]any{"enabled": true, "count": json.Number("9007199254740993")}, json.RawMessage(`{"broken":`), false, "Native Go JSON values; RawMessage explicitly opts into parsing."},
		{"FileField", models.FileField("file"), "sample/documents/readme.txt", 123, false, "An opaque storage-key string, not an upload. Upload validation is demonstrated separately."},
		{"ImageField", models.ImageField("image"), "sample/images/pixel.png", 123, false, "An opaque storage-key string; model validation does not decode image bytes."},
		{"ForeignKeyField", models.ForeignKeyField("parent_id", relation), int64(1), nil, true, "Relationship existence and authorization belong to the registry/database/ORM, not scalar Clean."},
		{"OneToOneField", models.OneToOneField("unique_parent_id", relation), int64(1), nil, true, "Unique relationship metadata; scoped target lookup and database uniqueness are separate operations."},
		{"ManyToManyField", models.ManyToManyField("peers", models.Relation{Target: "fieldlab.Specimen"}), []int64{1}, nil, true, "Registry creates an intermediary schema; this is not a stored scalar column."},
	}
	integer := models.IntegerField("element")
	array := models.NewField("array", models.Array)
	array.Element = &integer
	generated := models.NewField("generated", models.Generated)
	generated.Element = &integer
	generated.GeneratedExpression = "1 + 1"
	rangeField := models.NewField("range", models.Range)
	rangeField.RangeType = models.Integer
	cases = append(cases,
		ModelCase{"NewField(Array)", array, []int{1, 2}, []string{"invalid"}, false, "Element validation and PostgreSQL type mapping are demonstrated; this sample does not claim array CRUD round trips."},
		ModelCase{"NewField(Generated)", generated, nil, nil, true, "Read-only descriptor and generated SQL mapping; excluded from the sample's ordinary writable tables."},
		ModelCase{"NewField(HStore)", models.NewField("hstore", models.HStore), nil, nil, true, "PostgreSQL type mapping requires extension:hstore; no HStore CRUD demonstration."},
		ModelCase{"NewField(Range)", rangeField, nil, nil, true, "PostgreSQL int4range type mapping only; no range value codec/CRUD demonstration."},
		ModelCase{"NewField(SearchVector)", models.NewField("search_vector", models.SearchVector), nil, nil, true, "PostgreSQL tsvector type mapping only; not a full-text search API demonstration."},
		ModelCase{"NewField(Geometry)", models.NewField("geometry", models.Geometry), nil, nil, true, "PostGIS-gated type mapping only; no GIS operations or GIS form widget."},
		ModelCase{"NewField(Geography)", models.NewField("geography", models.Geography), nil, nil, true, "PostGIS-gated type mapping only; no GIS operations or GIS form widget."},
		ModelCase{"NewField(Raster)", models.NewField("raster", models.Raster), nil, nil, true, "PostGIS-gated type mapping only; no raster codec or raster UI."},
		ModelCase{"NewField(Custom)", models.NewField("custom", models.Custom), nil, nil, true, "Descriptor accepted; the stock PostgreSQL dialect and default forms explicitly reject this kind."},
	)
	return cases
}

// Schemas contains only the fieldlab tables intended for ordinary migrations.
// Backend-specific descriptor examples stay in ModelCases, outside migrations.
func Schemas() []models.Schema {
	specimen := models.Schema{AppLabel: "fieldlab", Name: "Specimen", Label: "Field specimen", LabelPlural: "Field specimens"}
	for _, example := range ModelCases() {
		if example.DescriptorOnly || example.Field.Kind == models.Array || example.Field.Kind == models.SmallAuto || example.Field.Kind == models.Auto {
			continue
		}
		specimen.Fields = append(specimen.Fields, example.Field)
	}
	specimen.Ordering = []string{"id"}
	return []models.Schema{
		specimen,
		{AppLabel: "fieldlab", Name: "SmallIdentity", Fields: []models.Field{models.SmallAutoField("id")}},
		{AppLabel: "fieldlab", Name: "StandardIdentity", Fields: []models.Field{models.AutoField("id")}},
		{AppLabel: "fieldlab", Name: "Related", Fields: []models.Field{
			models.BigAutoField("id"),
			models.ForeignKeyField("parent_id", models.Relation{Target: "fieldlab.Specimen", OnDelete: models.Protect, RelatedName: "children"}),
			models.OneToOneField("unique_parent_id", models.Relation{Target: "fieldlab.Specimen", OnDelete: models.Protect, RelatedName: "profile"}),
			models.ManyToManyField("peers", models.Relation{Target: "fieldlab.Specimen", RelatedName: "peer_groups"}),
		}},
	}
}

// Factories demonstrates public schema-bound records without reflection or a
// private framework import. The declarations are static and validated in tests.
func Factories() map[string]func() models.Model {
	result := map[string]func() models.Model{}
	for _, schema := range Schemas() {
		result[schema.Key()] = func() models.Model {
			record, err := models.NewRecord(schema)
			if err != nil {
				panic("fieldlab: invalid static model declaration")
			}
			return record
		}
	}
	return result
}

// NewSpecimen returns validated in-memory values, not a saved row. Caller owns
// transaction scope and persistence. Its file/image keys are illustrative only;
// no uploaded object is created or claimed by this function.
func NewSpecimen() (*models.MapRecord, error) {
	record, err := models.NewRecord(Schemas()[0])
	if err != nil {
		return nil, err
	}
	for _, example := range ModelCases() {
		if _, exists := record.Schema().Field(example.Field.Name); !exists || example.Field.IsAuto() {
			continue
		}
		value, err := example.Field.Clean(context.Background(), example.Valid)
		if err != nil {
			return nil, err
		}
		if err := record.Set(example.Field.Name, value); err != nil {
			return nil, err
		}
	}
	return record, nil
}
