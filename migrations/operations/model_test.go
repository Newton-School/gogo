package operations

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/migrations"
)

func TestModelOperationsMutateStateAndRenderSQL(t *testing.T) {
	state := migrations.NewProjectState()
	editor := &fakeEditor{}
	create := CreateModel{Model: migrations.ModelState{AppLabel: "blog", Name: "Post", TableName: "blog_post"}}
	if err := create.StateForwards(&state); err != nil {
		t.Fatalf("CreateModel StateForwards() error = %v", err)
	}
	if _, ok := state.Models["blog.Post"]; !ok {
		t.Fatalf("model was not created in state")
	}
	if err := create.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("CreateModel DatabaseForwards() error = %v", err)
	}
	if editor.SQL[0] != `CREATE TABLE IF NOT EXISTS blog_post (id bigint PRIMARY KEY)` {
		t.Fatalf("create SQL = %q", editor.SQL[0])
	}

	for _, op := range []migrations.Operation{
		RenameModel{AppLabel: "blog", OldName: "Post", NewName: "Article"},
		AlterModelTable{AppLabel: "blog", ModelName: "Article", OldTable: "blog_post", NewTable: "blog_article"},
		AlterModelTableComment{AppLabel: "blog", ModelName: "Article", Comment: "stores articles"},
		AlterModelOptions{AppLabel: "blog", ModelName: "Article", Options: map[string]any{"ordering": []string{"title"}}},
		AlterModelManagers{AppLabel: "blog", ModelName: "Article", Managers: []string{"objects", "published"}},
		AlterOrderWithRespectTo{AppLabel: "blog", ModelName: "Article", Field: "author"},
		AlterTogether{AppLabel: "blog", ModelName: "Article", UniqueTogether: [][]string{{"slug", "locale"}}, IndexTogether: [][]string{{"author", "created_at"}}},
	} {
		if err := op.StateForwards(&state); err != nil {
			t.Fatalf("%s StateForwards() error = %v", op.Name(), err)
		}
		if err := op.DatabaseForwards(context.Background(), editor); err != nil {
			t.Fatalf("%s DatabaseForwards() error = %v", op.Name(), err)
		}
	}
	if state.Models["blog.Article"].TableName != "blog_article" {
		t.Fatalf("renamed model state = %#v", state.Models["blog.Article"])
	}

	deleteOp := DeleteModel{Model: state.Models["blog.Article"]}
	if err := deleteOp.StateForwards(&state); err != nil {
		t.Fatalf("DeleteModel StateForwards() error = %v", err)
	}
	if _, ok := state.Models["blog.Article"]; ok {
		t.Fatalf("model still exists after DeleteModel")
	}
}

func TestCreateModelCreatesConstraintsThenIndexes(t *testing.T) {
	editor := &fakeEditor{}
	create := CreateModel{Model: migrations.ModelState{
		AppLabel:  "blog",
		Name:      "Post",
		TableName: "blog_post",
		Fields: []migrations.FieldState{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "slug", Column: "slug", Kind: "text"},
		},
		Constraints: []migrations.ConstraintState{{Name: "uniq_blog_post_slug", Type: "unique", Fields: []string{"slug"}}},
		Indexes:     []migrations.IndexState{{Name: "idx_blog_post_slug", Fields: []string{"slug"}}},
	}}
	if err := create.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("CreateModel DatabaseForwards() error = %v", err)
	}
	want := []string{
		`CREATE TABLE IF NOT EXISTS blog_post (id bigint PRIMARY KEY, slug text NOT NULL)`,
		`ALTER TABLE blog_post ADD CONSTRAINT uniq_blog_post_slug UNIQUE (slug)`,
		`CREATE INDEX idx_blog_post_slug ON blog_post (slug)`,
	}
	if len(editor.SQL) != len(want) {
		t.Fatalf("SQL count = %d, want %d: %#v", len(editor.SQL), len(want), editor.SQL)
	}
	for i, statement := range want {
		if editor.SQL[i] != statement {
			t.Fatalf("SQL[%d] = %q, want %q; all SQL %#v", i, editor.SQL[i], statement, editor.SQL)
		}
	}
}

type fakeEditor struct {
	SQL []string
}

func (e *fakeEditor) Execute(ctx context.Context, sql string, args ...any) error {
	_ = ctx
	_ = args
	e.SQL = append(e.SQL, sql)
	return nil
}
