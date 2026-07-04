package migrations

import (
	"strings"
	"testing"
)

func TestCompareTableShapeReportsIndexesConstraintsAndForeignKeys(t *testing.T) {
	expected := TableSchema{
		Name: "blog_post",
		Columns: []ColumnSchema{
			{Name: "id", NormalizedKind: "bigint", PrimaryKey: true},
			{Name: "slug", NormalizedKind: "text"},
			{Name: "author_id", NormalizedKind: "uuid"},
		},
		Indexes: []IndexSchema{{
			Name:         "idx_blog_post_slug",
			Fields:       []string{"slug"},
			Method:       "gin",
			OpClasses:    []string{"gin_trgm_ops"},
			ConditionSQL: "deleted_at IS NULL",
		}},
		Constraints: []ConstraintSchema{
			{Name: "uniq_blog_post_slug", Type: "unique", Fields: []string{"tenant_id", "slug"}},
			{Name: "fk_blog_post_author", Type: "foreign_key", Fields: []string{"author_id"}, ReferencesTable: "auth_user", ReferencesColumns: []string{"id"}, OnDelete: "CASCADE"},
		},
	}
	actual := TableSchema{
		Name: "blog_post",
		Columns: []ColumnSchema{
			{Name: "id", NormalizedKind: "bigint", PrimaryKey: true},
			{Name: "slug", NormalizedKind: "text"},
			{Name: "author_id", NormalizedKind: "uuid"},
		},
		Indexes: []IndexSchema{{
			Name:   "idx_blog_post_slug",
			Fields: []string{"title"},
		}},
		Constraints: []ConstraintSchema{
			{Name: "uniq_blog_post_slug", Type: "unique", Fields: []string{"tenant_id", "title"}},
			{Name: "fk_blog_post_author", Type: "foreign_key", Fields: []string{"author_id"}, ReferencesTable: "accounts_user", ReferencesColumns: []string{"id"}},
		},
	}

	diffs := CompareTableShape(expected, actual)
	output := schemaDiffOutput(diffs)
	for _, want := range []string{
		"INDEX mismatch blog_post.idx_blog_post_slug",
		"CONSTRAINT mismatch blog_post.uniq_blog_post_slug",
		"FOREIGN KEY mismatch blog_post.fk_blog_post_author",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diff output missing %q:\n%s", want, output)
		}
	}
}

func TestCompareTableShapeReportsMissingObjects(t *testing.T) {
	expected := TableSchema{
		Name:        "blog_post",
		Columns:     []ColumnSchema{{Name: "id", PrimaryKey: true}},
		Indexes:     []IndexSchema{{Name: "idx_blog_post_slug", Fields: []string{"slug"}}},
		Constraints: []ConstraintSchema{{Name: "uniq_blog_post_slug", Type: "unique", Fields: []string{"slug"}}},
	}
	actual := TableSchema{Name: "blog_post", Columns: []ColumnSchema{{Name: "id", PrimaryKey: true}}}

	diffs := CompareTableShape(expected, actual)
	output := schemaDiffOutput(diffs)
	for _, want := range []string{
		"MISSING index blog_post.idx_blog_post_slug",
		"MISSING constraint blog_post.uniq_blog_post_slug",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diff output missing %q:\n%s", want, output)
		}
	}
}

func TestCompareTableSchemaKeepsColumnOnlyCompatibility(t *testing.T) {
	diffs := CompareTableSchema(
		TableSchema{Name: "blog_post", Columns: []ColumnSchema{{Name: "slug", NormalizedKind: "text"}}},
		[]ColumnSchema{{Name: "slug", NormalizedKind: "text"}},
	)
	if len(diffs) != 0 {
		t.Fatalf("column-only diffs = %#v", diffs)
	}
}

func schemaDiffOutput(diffs []SchemaDifference) string {
	lines := make([]string, len(diffs))
	for i, diff := range diffs {
		lines[i] = diff.String()
	}
	return strings.Join(lines, "\n")
}
