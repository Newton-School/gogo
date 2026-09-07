package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestPostgresConstraintDriftNeverRemovesUnownedDefinition(t *testing.T) {
	for _, mode := range []string{"unmarked", "missing", "expression", "not_valid", "no_inherit", "foreign_table", "unique_fields", "unique_deferred", "unique_nulls"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := constraintSchema()
			value := models.Constraint{Name: "rule", Kind: "check", Expression: "quantity >= 0"}
			if strings.HasPrefix(mode, "unique_") {
				value.Kind, value.Expression, value.Fields = "unique", "", []string{"title"}
			}
			schema.Constraints = []models.Constraint{value}
			editor := b.SchemaEditor().(db.ConstraintLifecycleEditor)
			if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			var marker string
			if err := db.QueryRow(ctx, b, `SELECT obj_description(c.oid,'pg_constraint') FROM pg_constraint c WHERE c.conrelid='tests_constraint'::regclass AND c.conname='rule'`, nil, &marker); err != nil {
				t.Fatal(err)
			}
			if mode == "unmarked" {
				if _, err := b.Exec(ctx, "COMMENT ON CONSTRAINT rule ON tests_constraint IS NULL"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := b.Exec(ctx, "ALTER TABLE tests_constraint DROP CONSTRAINT rule"); err != nil {
					t.Fatal(err)
				}
				if mode != "missing" {
					table, definition := "tests_constraint", "CHECK (quantity >= 0)"
					switch mode {
					case "expression":
						definition = "CHECK (quantity > 0)"
					case "not_valid":
						definition += " NOT VALID"
					case "no_inherit":
						definition += " NO INHERIT"
					case "foreign_table":
						table = "foreign_constraint"
						if _, err := b.Exec(ctx, "CREATE TABLE foreign_constraint(quantity integer)"); err != nil {
							t.Fatal(err)
						}
					case "unique_fields":
						definition = "UNIQUE (quantity)"
					case "unique_deferred":
						definition = "UNIQUE (stored_title) DEFERRABLE INITIALLY DEFERRED"
					case "unique_nulls":
						definition = "UNIQUE NULLS NOT DISTINCT (stored_title)"
					}
					if _, err := b.Exec(ctx, "ALTER TABLE "+table+" ADD CONSTRAINT rule "+definition); err != nil {
						t.Fatal(err)
					}
					if _, err := b.Exec(ctx, "COMMENT ON CONSTRAINT rule ON "+table+" IS '"+strings.ReplaceAll(marker, "'", "''")+"'"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := editor.RemoveConstraint(ctx, b, schema, value); err == nil {
				t.Fatal("changed or unowned constraint was removed")
			}
			if mode != "missing" {
				var remaining bool
				if err := db.QueryRow(ctx, b, `SELECT EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname=current_schema() AND c.conname='rule')`, nil, &remaining); err != nil || !remaining {
					t.Fatal("guard destroyed unrelated definition", remaining, err)
				}
			}
		})
	}
}

func TestPostgresConstraintQuotedBlockAndTemporaryNamesRemainIsolated(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := constraintSchema()
	value := models.Constraint{Name: "rule", Kind: "check", Expression: "quantity >= 0 AND stored_title NOT LIKE '%$gogo_constraint$%'"}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	editor := b.SchemaEditor().(db.ConstraintLifecycleEditor)
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		executor := db.ExecutorFor(ctx, b)
		for _, sql := range []string{"CREATE TEMP TABLE foreign_constraint(quantity integer, CONSTRAINT rule CHECK(quantity > 0)) ON COMMIT DROP", "COMMENT ON CONSTRAINT rule ON pg_temp.foreign_constraint IS 'foreign constraint'"} {
			if _, err := executor.Exec(ctx, sql); err != nil {
				return err
			}
		}
		if err := editor.AddConstraint(ctx, executor, schema, value); err != nil {
			return err
		}
		if err := editor.RemoveConstraint(ctx, executor, schema, value); err != nil {
			return err
		}
		var marker string
		if err := db.QueryRow(ctx, executor, `SELECT obj_description(c.oid,'pg_constraint') FROM pg_constraint c WHERE c.conrelid='pg_temp.foreign_constraint'::regclass AND c.conname='rule'`, nil, &marker); err != nil {
			return err
		}
		if marker != "foreign constraint" {
			t.Fatal("temporary constraint was claimed", marker)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := editor.AddConstraint(ctx, b, schema, value); err != nil {
		t.Fatal(err)
	}
	err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		executor := db.ExecutorFor(ctx, b)
		if _, err := executor.Exec(ctx, "CREATE TEMP TABLE tests_constraint(quantity integer) ON COMMIT DROP"); err != nil {
			return err
		}
		return editor.RemoveConstraint(ctx, executor, schema, value)
	})
	if err == nil {
		t.Fatal("shadowed target table passed ownership guard")
	}
	if err := editor.RemoveConstraint(ctx, b, schema, value); err != nil {
		t.Fatal("rejected shadowed operation changed real ownership", err)
	}
}
