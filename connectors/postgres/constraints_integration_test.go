package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func constraintSchema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "Constraint", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", models.WithColumn("stored_title"), models.Nullable), models.BooleanField("enabled"), models.IntegerField("quantity")}}
}

func TestPostgresConstraintLifecycleDetectApplyRemoveAndReverse(t *testing.T) {
	for _, kind := range []string{"unique", "deferred", "check", "partial"} {
		t.Run(kind, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := constraintSchema()
			value := models.Constraint{Name: "rule", Kind: "unique", Fields: []string{"title"}}
			switch kind {
			case "deferred":
				value.Deferrable = true
			case "check":
				value.Kind, value.Expression, value.Fields = "check", "quantity >= 0", []string{"quantity"}
			case "partial":
				value.Condition = "enabled"
				value.NullsDistinct = new(bool)
			}
			constrained := schema.Clone()
			constrained.Constraints = []models.Constraint{value}
			operations, err := migrations.Detect([]models.Schema{schema}, []models.Schema{constrained}, migrations.DetectOptions{})
			if err != nil || len(operations) != 1 || operations[0].Kind != "add_constraint" {
				t.Fatal(operations, err)
			}
			initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
			add := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
			remove := migrations.Migration{App: "tests", Name: "0003", Dependencies: []string{add.Key()}, Operations: []migrations.Operation{migrations.RemoveConstraint(constrained, value)}}
			e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, add, remove}}
			if statements, err := e.SQL(ctx, add.Key(), false); err != nil || len(statements) != 1 || !strings.Contains(statements[0].SQL, "COMMENT ON") {
				t.Fatal("constraint creation lacks atomic ownership", statements, err)
			}
			if err := e.Apply(ctx, add.Key()); err != nil {
				t.Fatal("constraint addition", err)
			}
			if _, err := b.Exec(ctx, `INSERT INTO tests_constraint(stored_title,enabled,quantity) VALUES ('same',true,0)`); err != nil {
				t.Fatal(err)
			}
			invalid := `INSERT INTO tests_constraint(stored_title,enabled,quantity) VALUES ('same',true,0)`
			if kind == "check" {
				invalid = `INSERT INTO tests_constraint(stored_title,enabled,quantity) VALUES ('other',true,-1)`
			}
			if _, err := b.Exec(ctx, invalid); err == nil {
				t.Fatal("declared constraint not enforced")
			}
			if err := e.Apply(ctx, remove.Key()); err != nil {
				t.Fatal("owned constraint removal", err)
			}
			if err := e.Reverse(ctx, add.Key()); err != nil {
				t.Fatal("constraint removal reversal", err)
			}
			if _, err := b.Exec(ctx, invalid); err == nil {
				t.Fatal("reverse did not restore constraint enforcement")
			}
			if err := e.Reverse(ctx, initial.Key()); err != nil {
				t.Fatal("constraint addition reversal", err)
			}
			if _, err := b.Exec(ctx, invalid); err != nil {
				t.Fatal("reversed add retained constraint", err)
			}
		})
	}
}

func TestPostgresConstraintReplacementFailureRestoresOwnershipAndHistory(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := constraintSchema()
	schema.Constraints = []models.Constraint{{Name: "nonnegative", Kind: "check", Expression: "quantity >= 0"}}
	next := schema.Clone()
	next.Constraints[0].Expression = "quantity > 0"
	operations, err := migrations.Detect([]models.Schema{schema}, []models.Schema{next}, migrations.DetectOptions{})
	if err != nil || len(operations) != 2 || operations[0].Kind != "remove_constraint" || operations[1].Kind != "add_constraint" {
		t.Fatal(operations, err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := e.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	observe := func() string {
		t.Helper()
		var marker string
		if err := db.QueryRow(ctx, b, `SELECT obj_description(c.oid,'pg_constraint') FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=current_schema() AND t.relname='tests_constraint' AND c.conname='nonnegative'`, nil, &marker); err != nil {
			t.Fatal(err)
		}
		return marker
	}
	original := observe()
	if _, err := b.Exec(ctx, `INSERT INTO tests_constraint(stored_title,enabled,quantity) VALUES ('same',true,0)`); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, change.Key()); err == nil {
		t.Fatal("invalid rows accepted replacement check")
	}
	if observe() != original {
		t.Fatal("failed replacement lost original constraint")
	}
	if history, err := e.History(ctx); err != nil || len(history) != 1 {
		t.Fatal("failed replacement changed history", history, err)
	}
	if _, err := b.Exec(ctx, `UPDATE tests_constraint SET quantity=1`); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, change.Key()); err != nil {
		t.Fatal(err)
	}
	if observe() == original {
		t.Fatal("replacement retained obsolete descriptor")
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil || observe() != original {
		t.Fatal("reversal failed to restore exact descriptor", err)
	}
}

func TestPostgresConstraintUniqueColumnRenameRefreshesOwnedDescriptor(t *testing.T) {
	for _, logicalOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "physical", true: "logical"}[logicalOnly], func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := constraintSchema()
			if !logicalOnly {
				schema.Fields[1].Column = ""
			}
			schema.Constraints = []models.Constraint{{Name: "unique_title", Kind: "unique", Fields: []string{"title"}}}
			initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
			rename := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.RenameField(schema, "title", "heading")}}
			e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, rename}}
			if err := e.Apply(ctx, initial.Key()); err != nil {
				t.Fatal(err)
			}
			observe := func() (int64, int64, string) {
				t.Helper()
				var oid, index int64
				var marker string
				if err := db.QueryRow(ctx, b, `SELECT c.oid::bigint,c.conindid::bigint,obj_description(c.oid,'pg_constraint') FROM pg_constraint c WHERE c.conrelid='tests_constraint'::regclass AND c.conname='unique_title'`, nil, &oid, &index, &marker); err != nil {
					t.Fatal(err)
				}
				return oid, index, marker
			}
			oid, index, marker := observe()
			if err := e.Apply(ctx, rename.Key()); err != nil {
				t.Fatal("unique constraint could not follow field rename", err)
			}
			if current, backing, owned := observe(); current != oid || backing != index || (owned == marker) != logicalOnly {
				t.Fatal("rename replaced ownership or physical index identity", current, backing, owned)
			}
			state, err := e.State(rename.Key())
			if err != nil {
				t.Fatal(err)
			}
			remove := migrations.Migration{App: "tests", Name: "0003", Dependencies: []string{rename.Key()}, Operations: []migrations.Operation{migrations.RemoveConstraint(state[0], state[0].Constraints[0])}}
			e.Migrations = append(e.Migrations, remove)
			if err := e.Apply(ctx, remove.Key()); err != nil {
				t.Fatal("post-rename owned constraint removal", err)
			}
			if err := e.Reverse(ctx, initial.Key()); err != nil {
				t.Fatal("constraint remove and rename reversal", err)
			}
			if _, _, restored := observe(); restored != marker {
				t.Fatal("constraint reverse did not restore original marker", restored)
			}
		})
	}
}

func TestPostgresConstraintFieldDependenciesAndTypeTransitionsReverse(t *testing.T) {
	for _, mode := range []string{"new_field", "new_type"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := constraintSchema()
			schema.Constraints = []models.Constraint{{Name: "unique_quantity", Kind: "unique", Fields: []string{"quantity"}}}
			next := schema.Clone()
			if mode == "new_field" {
				next.Fields = append(next.Fields, models.TextField("note", models.Nullable))
				next.Constraints[0].Fields = []string{"note"}
			} else {
				next.Fields[3].Kind = models.BigInteger
			}
			operations, err := migrations.Detect([]models.Schema{schema}, []models.Schema{next}, migrations.DetectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
			change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
			e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
			if err := e.Apply(ctx, change.Key()); err != nil {
				t.Fatal("constraint dependency transition", err)
			}
			if err := e.Reverse(ctx, initial.Key()); err != nil {
				t.Fatal("constraint dependency reversal", err)
			}
			if err := b.SchemaEditor().(db.ConstraintLifecycleEditor).RemoveConstraint(ctx, b, schema, schema.Constraints[0]); err != nil {
				t.Fatal("reverse retained wrong storage descriptor", err)
			}
		})
	}
}

func TestPostgresConstraintColumnChangesRequireExplicitDependencyRemoval(t *testing.T) {
	for _, mode := range []string{"unique_remove", "check_remove", "partial_remove", "check_rename", "check_type"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := constraintSchema()
			value := models.Constraint{Name: "rule", Kind: "check", Expression: "quantity >= 0"}
			if mode == "unique_remove" {
				value.Kind, value.Expression, value.Fields = "unique", "", []string{"quantity"}
			} else if mode == "partial_remove" {
				value.Kind, value.Expression, value.Fields, value.Condition = "unique", "", []string{"title"}, "quantity >= 0"
			}
			schema.Constraints = []models.Constraint{value}
			editor := b.SchemaEditor()
			if err := editor.CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "check_rename":
				err = editor.RenameField(ctx, b, schema, "quantity", "amount")
			case "check_type":
				field := schema.Fields[3]
				field.Kind = models.BigInteger
				err = editor.AlterField(ctx, b, schema, schema.Fields[3], field)
			default:
				err = editor.RemoveField(ctx, b, schema, schema.Fields[3])
			}
			if !db.IsCode(err, db.UnsupportedFeature) {
				t.Fatal("unresolved constraint column dependency changed storage", err)
			}
			if _, err := b.Exec(ctx, `INSERT INTO tests_constraint(stored_title,enabled,quantity) VALUES ('preserved',true,1)`); err != nil {
				t.Fatal("rejected dependency operation changed columns", err)
			}
			if err := editor.(db.ConstraintLifecycleEditor).RemoveConstraint(ctx, b, schema, value); err != nil {
				t.Fatal("rejected dependency operation invalidated ownership", err)
			}
		})
	}
}

func TestPostgresCheckConstraintAndNamedIndexUseIndependentNamespaces(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := constraintSchema()
	schema.Indexes = []models.Index{{Name: "shared_rule", Fields: []string{"title"}}}
	schema.Constraints = []models.Constraint{{Name: "shared_rule", Kind: "check", Expression: "quantity >= 0"}}
	editor := b.SchemaEditor()
	if err := editor.CreateModel(ctx, b, schema); err != nil {
		t.Fatal("valid separate catalog namespaces rejected", err)
	}
	if err := editor.(db.IndexLifecycleEditor).RemoveModelIndex(ctx, b, schema, schema.Indexes[0]); err != nil {
		t.Fatal("ordinary index confused with same-name check constraint", err)
	}
	if _, err := b.Exec(ctx, `INSERT INTO tests_constraint(stored_title,enabled,quantity) VALUES ('invalid',true,-1)`); err == nil {
		t.Fatal("ordinary index removal affected check constraint")
	}
	if err := editor.(db.ConstraintLifecycleEditor).RemoveConstraint(ctx, b, schema, schema.Constraints[0]); err != nil {
		t.Fatal("check constraint removal lost independent ownership", err)
	}
}

func TestPostgresConditionalIndexTypeChangeRequiresExplicitLifecycle(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := constraintSchema()
	schema.Indexes = []models.Index{{Name: "nonempty_title", Fields: []string{"title"}, Condition: "stored_title <> ''"}}
	editor := b.SchemaEditor()
	if err := editor.CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	next := schema.Fields[1]
	next.Kind, next.MaxLength = models.Char, 50
	if err := editor.AlterField(ctx, b, schema, schema.Fields[1], next); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("type conversion rewrote a raw predicate's canonical definition", err)
	}
	if err := editor.(db.IndexLifecycleEditor).RemoveModelIndex(ctx, b, schema, schema.Indexes[0]); err != nil {
		t.Fatal("refused conversion changed ownership", err)
	}
}
