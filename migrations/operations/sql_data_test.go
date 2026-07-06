package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/migrations"
)

func TestRunSQLReversibleAndIrreversible(t *testing.T) {
	editor := &fakeEditor{}
	sql := RunSQL{SQL: "CREATE VIEW v AS SELECT 1", ReverseSQL: "DROP VIEW v"}
	if !sql.Reversible() || !sql.ReducesToSQL() || sql.Category() != CategorySQL {
		t.Fatalf("RunSQL metadata mismatch")
	}
	if err := sql.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("RunSQL forwards error = %v", err)
	}
	if err := sql.DatabaseBackwards(context.Background(), editor); err != nil {
		t.Fatalf("RunSQL backwards error = %v", err)
	}
	if editor.SQL[0] != "CREATE VIEW v AS SELECT 1" || editor.SQL[1] != "DROP VIEW v" {
		t.Fatalf("sql calls = %#v", editor.SQL)
	}

	irreversible := RunSQL{SQL: "CREATE VIEW v AS SELECT 1"}
	if irreversible.Reversible() {
		t.Fatalf("irreversible RunSQL marked reversible")
	}
	if err := irreversible.DatabaseBackwards(context.Background(), editor); !errors.Is(err, migrations.ErrIrreversibleOperation) {
		t.Fatalf("irreversible backwards error = %v", err)
	}
}

func TestRunSQLInitialTablesDetectsCreateTable(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{sql: `CREATE TABLE blog_post (id integer)`, want: "blog_post"},
		{sql: `CREATE TABLE blog_post(id integer)`, want: "blog_post"},
		{sql: `CREATE TABLE IF NOT EXISTS "blog_post" (id integer)`, want: "blog_post"},
		{sql: "CREATE INDEX blog_post_title_idx ON blog_post(title)", want: ""},
	}
	for _, tc := range cases {
		tables := RunSQL{SQL: tc.sql}.InitialTables()
		if tc.want == "" {
			if len(tables) != 0 {
				t.Fatalf("InitialTables(%q) = %#v, want none", tc.sql, tables)
			}
			continue
		}
		if len(tables) != 1 || tables[0] != tc.want {
			t.Fatalf("InitialTables(%q) = %#v, want %q", tc.sql, tables, tc.want)
		}
	}
}

func TestRunGoAndSeparateDatabaseAndState(t *testing.T) {
	calledForward := false
	calledReverse := false
	runGo := RunGo{
		Code: func(context.Context, *migrations.ProjectState) error {
			calledForward = true
			return nil
		},
		ReverseCode: func(context.Context, *migrations.ProjectState) error {
			calledReverse = true
			return nil
		},
	}
	state := migrations.NewProjectState()
	if err := runGo.StateForwards(&state); err != nil {
		t.Fatalf("RunGo StateForwards() error = %v", err)
	}
	if err := runGo.DatabaseBackwards(context.Background(), &fakeEditor{}); err != nil {
		t.Fatalf("RunGo DatabaseBackwards() error = %v", err)
	}
	if !calledForward || !calledReverse {
		t.Fatalf("RunGo callbacks not called")
	}

	separate := SeparateDatabaseAndState{
		DatabaseOperations: []migrations.Operation{RunSQL{SQL: "SELECT 1", ReverseSQL: "SELECT 0"}},
		StateOperations:    []migrations.Operation{AddField{AppLabel: "blog", ModelName: "Post", Field: migrations.FieldState{Name: "title", Null: true}}},
	}
	state.AddModel(migrations.ModelState{AppLabel: "blog", Name: "Post", TableName: "blog_post"})
	editor := &fakeEditor{}
	if err := separate.StateForwards(&state); err != nil {
		t.Fatalf("Separate StateForwards() error = %v", err)
	}
	if err := separate.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("Separate DatabaseForwards() error = %v", err)
	}
	if len(state.Models["blog.Post"].Fields) != 1 || editor.SQL[0] != "SELECT 1" {
		t.Fatalf("separate state/sql mismatch")
	}
}
