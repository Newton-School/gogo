package operations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
)

func TestFieldOperationsMutateStateAndRenderSQL(t *testing.T) {
	state := migrations.NewProjectState()
	state.AddModel(migrations.ModelState{AppLabel: "blog", Name: "Post", TableName: "blog_post"})
	editor := &fakeEditor{}

	add := AddField{AppLabel: "blog", ModelName: "Post", Field: migrations.FieldState{Name: "title", Column: "title", Kind: "text", Null: true}}
	if err := add.StateForwards(&state); err != nil {
		t.Fatalf("AddField StateForwards() error = %v", err)
	}
	if err := add.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("AddField DatabaseForwards() error = %v", err)
	}
	if state.Models["blog.Post"].Fields[0].Name != "title" || editor.SQL[0] != `ALTER TABLE blog_post ADD COLUMN title text` {
		t.Fatalf("add field state/sql = %#v / %#v", state.Models["blog.Post"].Fields, editor.SQL)
	}

	alter := AlterField{AppLabel: "blog", ModelName: "Post", OldField: migrations.FieldState{Name: "title", Column: "title", Kind: "text", Null: true}, NewField: migrations.FieldState{Name: "title", Column: "title", Kind: "varchar(255)", Null: false}, UnsafeAcknowledged: true}
	if err := alter.StateForwards(&state); err != nil {
		t.Fatalf("AlterField StateForwards() error = %v", err)
	}
	if state.Models["blog.Post"].Fields[0].Kind != "varchar(255)" || state.Models["blog.Post"].Fields[0].Null {
		t.Fatalf("altered field = %#v", state.Models["blog.Post"].Fields[0])
	}

	rename := RenameField{AppLabel: "blog", ModelName: "Post", OldName: "title", NewName: "headline"}
	if err := rename.StateForwards(&state); err != nil {
		t.Fatalf("RenameField StateForwards() error = %v", err)
	}
	if state.Models["blog.Post"].Fields[0].Name != "headline" {
		t.Fatalf("renamed field = %#v", state.Models["blog.Post"].Fields[0])
	}

	remove := RemoveField{AppLabel: "blog", ModelName: "Post", Field: state.Models["blog.Post"].Fields[0]}
	if err := remove.StateForwards(&state); err != nil {
		t.Fatalf("RemoveField StateForwards() error = %v", err)
	}
	if len(state.Models["blog.Post"].Fields) != 0 {
		t.Fatalf("field still exists: %#v", state.Models["blog.Post"].Fields)
	}
}

func TestAddFieldRejectsUnsafeNonNullWithoutDefault(t *testing.T) {
	op := AddField{AppLabel: "blog", ModelName: "Post", Field: migrations.FieldState{Name: "slug", Kind: "text", Null: false}}
	if err := op.ValidateSafety(); !errors.Is(err, migrations.ErrUnsafeMigration) {
		t.Fatalf("ValidateSafety() error = %v, want ErrUnsafeMigration", err)
	}
	op.HasDefault = true
	if err := op.ValidateSafety(); err != nil {
		t.Fatalf("ValidateSafety() with default error = %v", err)
	}
}

func TestAlterFieldRejectsUnsafeNonNullWithoutDefault(t *testing.T) {
	op := AlterField{
		AppLabel:  "blog",
		ModelName: "Post",
		OldField:  migrations.FieldState{Name: "slug", Kind: "text", Null: true},
		NewField:  migrations.FieldState{Name: "slug", Kind: "text", Null: false},
	}
	if err := op.ValidateSafety(); !errors.Is(err, migrations.ErrUnsafeMigration) {
		t.Fatalf("ValidateSafety() error = %v, want ErrUnsafeMigration", err)
	}
	op.NewField.DBDefault = databaseDefaultPtr(models.DefaultValue("draft"))
	if err := op.ValidateSafety(); err != nil {
		t.Fatalf("ValidateSafety() with default error = %v", err)
	}
	op.NewField.DBDefault = nil
	op.UnsafeAcknowledged = true
	if err := op.ValidateSafety(); err != nil {
		t.Fatalf("ValidateSafety() with acknowledgement error = %v", err)
	}
}

func TestFieldOperationsUseExplicitTableNameInSQLAndSpecs(t *testing.T) {
	editor := &fakeEditor{}
	field := migrations.FieldState{Name: "status", Column: "status", Kind: "text", Null: true}
	add := AddField{AppLabel: "sales", ModelName: "Order", TableName: "orders", Field: field}
	if err := add.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("AddField DatabaseForwards() error = %v", err)
	}
	rename := RenameField{AppLabel: "sales", ModelName: "Order", TableName: "orders", OldName: "status", NewName: "state"}
	if err := rename.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("RenameField DatabaseForwards() error = %v", err)
	}
	remove := RemoveField{AppLabel: "sales", ModelName: "Order", TableName: "orders", Field: field}
	if err := remove.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("RemoveField DatabaseForwards() error = %v", err)
	}
	want := []string{
		`ALTER TABLE orders ADD COLUMN status text`,
		`ALTER TABLE orders RENAME COLUMN status TO state`,
		`ALTER TABLE orders DROP COLUMN status`,
	}
	for i, sql := range want {
		if editor.SQL[i] != sql {
			t.Fatalf("SQL[%d] = %q, want %q; all SQL %#v", i, editor.SQL[i], sql, editor.SQL)
		}
	}
	if spec := migrations.OperationSpecFor(add); spec.TableName != "orders" {
		t.Fatalf("AddField spec TableName = %q", spec.TableName)
	}
}

func TestFieldOperationsUseSchemaRendererWhenAvailable(t *testing.T) {
	editor := &renderingEditor{}
	add := AddField{AppLabel: "sales", ModelName: "Order", TableName: "orders", Field: migrations.FieldState{Name: "status", Column: "status", Kind: "text", Null: true}}
	if err := add.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("AddField DatabaseForwards() error = %v", err)
	}
	rename := RenameField{AppLabel: "sales", ModelName: "Order", TableName: "orders", OldName: "status", NewName: "state"}
	if err := rename.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("RenameField DatabaseForwards() error = %v", err)
	}

	want := []string{
		`ALTER TABLE "orders" ADD COLUMN "status" text`,
		`ALTER TABLE "orders" RENAME COLUMN "status" TO "state"`,
	}
	for i, sql := range want {
		if editor.SQL[i] != sql {
			t.Fatalf("SQL[%d] = %q, want %q; all SQL %#v", i, editor.SQL[i], sql, editor.SQL)
		}
	}
}

func TestAlterFieldRendersChangedAttributesInDeterministicOrder(t *testing.T) {
	editor := &renderingEditor{}
	oldDefault := models.DefaultValue("draft")
	newDefault := models.DefaultValue("published")
	op := AlterField{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		OldField:  migrations.FieldState{Name: "status", Column: "status", Kind: "text", Null: true, DBDefault: &oldDefault, DBCollation: "C"},
		NewField:  migrations.FieldState{Name: "status", Column: "status", Kind: "varchar(32)", Null: false, DBDefault: &newDefault, DBCollation: "en_US"},
	}
	if err := op.DatabaseForwards(context.Background(), editor); err != nil {
		t.Fatalf("AlterField DatabaseForwards() error = %v", err)
	}
	wantForwards := []string{
		`ALTER TABLE "orders" ALTER COLUMN "status" TYPE varchar(32)`,
		`ALTER TABLE "orders" ALTER COLUMN "status" SET DEFAULT 'published'`,
		`ALTER TABLE "orders" ALTER COLUMN "status" SET NOT NULL`,
		`ALTER TABLE "orders" ALTER COLUMN "status" TYPE varchar(32) COLLATE "en_US"`,
	}
	if strings.Join(editor.SQL, "\n") != strings.Join(wantForwards, "\n") {
		t.Fatalf("forwards SQL = %#v", editor.SQL)
	}

	editor.SQL = nil
	if err := op.DatabaseBackwards(context.Background(), editor); err != nil {
		t.Fatalf("AlterField DatabaseBackwards() error = %v", err)
	}
	wantBackwards := []string{
		`ALTER TABLE "orders" ALTER COLUMN "status" TYPE text COLLATE "C"`,
		`ALTER TABLE "orders" ALTER COLUMN "status" DROP NOT NULL`,
		`ALTER TABLE "orders" ALTER COLUMN "status" SET DEFAULT 'draft'`,
		`ALTER TABLE "orders" ALTER COLUMN "status" TYPE text`,
	}
	if strings.Join(editor.SQL, "\n") != strings.Join(wantBackwards, "\n") {
		t.Fatalf("backwards SQL = %#v", editor.SQL)
	}
}

type renderingEditor struct {
	fakeEditor
}

func (e *renderingEditor) CreateTable(table string, fields []migrations.FieldState) string {
	columns := make([]string, len(fields))
	for i, field := range fields {
		columns[i] = e.column(field)
	}
	return "CREATE TABLE " + quote(table) + " (" + strings.Join(columns, ", ") + ")"
}
func (e *renderingEditor) DropTable(table string) string { return "DROP TABLE " + quote(table) }
func (e *renderingEditor) RenameTable(oldName, newName string) string {
	return "ALTER TABLE " + quote(oldName) + " RENAME TO " + quote(newName)
}
func (e *renderingEditor) AddColumn(table string, field migrations.FieldState) string {
	return "ALTER TABLE " + quote(table) + " ADD COLUMN " + e.column(field)
}
func (e *renderingEditor) DropColumn(table, column string) string {
	return "ALTER TABLE " + quote(table) + " DROP COLUMN " + quote(column)
}
func (e *renderingEditor) AlterColumnType(table, column, kind string) string {
	return "ALTER TABLE " + quote(table) + " ALTER COLUMN " + quote(column) + " TYPE " + kind
}
func (e *renderingEditor) AlterNull(table, column string, nullable bool) string {
	action := "SET NOT NULL"
	if nullable {
		action = "DROP NOT NULL"
	}
	return "ALTER TABLE " + quote(table) + " ALTER COLUMN " + quote(column) + " " + action
}
func (e *renderingEditor) AlterDefault(table, column string, value any) string {
	if value == nil {
		return "ALTER TABLE " + quote(table) + " ALTER COLUMN " + quote(column) + " DROP DEFAULT"
	}
	defaultValue, _ := models.NormalizeDatabaseDefault(value)
	if defaultValue.Kind == models.DefaultLiteral {
		return "ALTER TABLE " + quote(table) + " ALTER COLUMN " + quote(column) + " SET DEFAULT '" + defaultValue.Value.(string) + "'"
	}
	return "ALTER TABLE " + quote(table) + " ALTER COLUMN " + quote(column) + " SET DEFAULT " + defaultValue.SQL
}
func (e *renderingEditor) AlterColumnCollation(table, column, kind, collation string) string {
	statement := "ALTER TABLE " + quote(table) + " ALTER COLUMN " + quote(column) + " TYPE " + kind
	if collation != "" {
		statement += " COLLATE " + quote(collation)
	}
	return statement
}
func (e *renderingEditor) RenameColumn(table, oldName, newName string) string {
	return "ALTER TABLE " + quote(table) + " RENAME COLUMN " + quote(oldName) + " TO " + quote(newName)
}
func (e *renderingEditor) AddIndex(table string, index migrations.IndexState) string {
	return "CREATE INDEX " + quote(index.Name) + " ON " + quote(table) + " (" + strings.Join(quoteAll(index.Fields), ", ") + ")"
}
func (e *renderingEditor) DropIndex(name string) string { return "DROP INDEX " + quote(name) }
func (e *renderingEditor) RenameIndex(oldName, newName string) string {
	return "ALTER INDEX " + quote(oldName) + " RENAME TO " + quote(newName)
}
func (e *renderingEditor) AddConstraint(table string, constraint migrations.ConstraintState) string {
	return "ALTER TABLE " + quote(table) + " ADD CONSTRAINT " + quote(constraint.Name) + " UNIQUE (" + strings.Join(quoteAll(constraint.Fields), ", ") + ")"
}
func (e *renderingEditor) DropConstraint(table, name string) string {
	return "ALTER TABLE " + quote(table) + " DROP CONSTRAINT " + quote(name)
}
func (e *renderingEditor) column(field migrations.FieldState) string {
	return quote(columnName(field)) + " " + fieldKind(field)
}

func quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func quoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = quote(value)
	}
	return quoted
}

func databaseDefaultPtr(defaultValue models.DatabaseDefault) *models.DatabaseDefault {
	return &defaultValue
}
