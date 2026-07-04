package constraints

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestIndexMetadata(t *testing.T) {
	index := NewIndex("", Asc("tenant_id"), Desc("created_at").WithNullsLast()).
		WithExpressions("LOWER(title)").
		WithCondition("published_at IS NOT NULL").
		WithInclude("id", "author_id").
		WithOperatorClasses("int8_ops", "timestamp_ops").
		WithTablespace("fastspace").
		WithMethod("btree")

	if !reflect.DeepEqual(index.FieldNames(), []string{"tenant_id", "created_at"}) {
		t.Fatalf("FieldNames() = %#v", index.FieldNames())
	}
	if !index.Fields[1].Descending || !index.Fields[1].NullsLast {
		t.Fatalf("ordered field metadata not preserved: %#v", index.Fields[1])
	}
	if index.Condition == "" || len(index.Expressions) != 1 || len(index.Include) != 2 {
		t.Fatalf("functional/partial/covering metadata missing: %#v", index)
	}
	if index.OpClasses[1] != "timestamp_ops" || index.Tablespace != "fastspace" || index.Method != "btree" {
		t.Fatalf("operator/table/method metadata missing: %#v", index)
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	name := index.NameFor("blog_post")
	if name == "" || len(name) > MaxNameLength {
		t.Fatalf("NameFor() = %q, want non-empty name within max length", name)
	}
	if again := index.NameFor("blog_post"); again != name {
		t.Fatalf("NameFor() = %q then %q, want deterministic", name, again)
	}
}

func TestIndexCloneCopiesSlices(t *testing.T) {
	index := NewIndex("", Asc("slug")).
		WithExpressions("LOWER(slug)").
		WithInclude("id").
		WithOperatorClasses("varchar_pattern_ops")

	cloned := index.Clone()
	cloned.Fields[0].Name = "changed"
	cloned.Expressions[0] = "changed"
	cloned.Include[0] = "changed"
	cloned.OpClasses[0] = "changed"

	if index.Fields[0].Name != "slug" || index.Expressions[0] != "LOWER(slug)" || index.Include[0] != "id" || index.OpClasses[0] != "varchar_pattern_ops" {
		t.Fatalf("Clone() shared backing storage with original: %#v", index)
	}
}

func TestUniqueIndexMetadata(t *testing.T) {
	index := NewUniqueIndex("uniq_lower_email").
		WithExpressions("LOWER(email)").
		WithCondition("deleted_at IS NULL").
		WithInclude("id").
		WithOperatorClasses("text_pattern_ops")

	if !index.Unique {
		t.Fatalf("Unique = false, want true")
	}
	if index.Name != "uniq_lower_email" || index.Expressions[0] != "LOWER(email)" {
		t.Fatalf("index metadata = %#v", index)
	}

	cloned := index.Clone()
	cloned.Unique = false
	cloned.Expressions[0] = "UPPER(email)"
	if !index.Unique || index.Expressions[0] != "LOWER(email)" {
		t.Fatalf("Clone() mutated original index = %#v", index)
	}
}

func TestIndexWithUnique(t *testing.T) {
	index := NewIndex("idx_slug", Asc("slug")).WithUnique()

	if !index.Unique {
		t.Fatalf("WithUnique() did not mark index unique: %#v", index)
	}
}

func TestIndexValidationFailures(t *testing.T) {
	cases := []Index{
		NewIndex(""),
		NewIndex("", IndexField{}),
		NewIndex("", Asc("name").WithNullsFirst().WithNullsLast()),
		NewIndex("", Asc("name")).WithOperatorClasses("one", "two"),
	}

	for _, index := range cases {
		if err := index.Validate(); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("Validate(%#v) error = %v, want ErrInvalidIndex", index, err)
		}
	}
}

func TestDeterministicIndexNamesAreDistinctByParts(t *testing.T) {
	first := NewIndex("", Asc("tenant_id"), Desc("created_at")).NameFor("blog_post")
	second := NewIndex("", Asc("tenant_id"), Desc("updated_at")).NameFor("blog_post")
	if first == second {
		t.Fatalf("expected distinct deterministic names, got %q", first)
	}
	if !strings.HasPrefix(first, "blog_post_tenant_id_created_at_") {
		t.Fatalf("NameFor() = %q, want readable prefix", first)
	}
}
