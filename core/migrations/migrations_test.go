package migrations

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func testSchema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Product", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title")}}
}

func TestPlanDeterminismCyclesAndDependencies(t *testing.T) {
	a := Migration{App: "shop", Name: "0001_initial", Operations: []Operation{CreateModel(testSchema())}}
	b := Migration{App: "shop", Name: "0002_next", Dependencies: []string{a.Key()}}
	e := Executor{Migrations: []Migration{b, a}}
	plan, err := e.Plan("")
	if err != nil || len(plan) != 2 || plan[0].Key() != a.Key() {
		t.Fatal(plan, err)
	}
	if _, err := e.Plan("shop.missing"); err == nil {
		t.Fatal("missing target accepted")
	}
	a.Dependencies = []string{b.Key()}
	e.Migrations = []Migration{a, b}
	if _, err := e.Plan(""); err == nil {
		t.Fatal("cycle accepted")
	}
	e.Migrations = []Migration{b}
	if _, err := e.Plan(""); err == nil {
		t.Fatal("missing dependency accepted")
	}
	e.Migrations = []Migration{b, b}
	if _, err := e.Plan(""); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestHistoricalStateAndExplicitDestructiveIntent(t *testing.T) {
	schema := testSchema()
	next := schema.Clone()
	next.Fields = append(next.Fields, models.TextField("note", models.Nullable))
	ops, err := Detect([]models.Schema{schema}, []models.Schema{next}, DetectOptions{})
	if err != nil || len(ops) != 1 || ops[0].Kind != "add_field" {
		t.Fatal(ops, err)
	}
	e := Executor{Migrations: []Migration{{App: "shop", Name: "0001_initial", Operations: []Operation{CreateModel(schema)}}, {App: "shop", Name: "0002_note", Dependencies: []string{"shop.0001_initial"}, Operations: ops}}}
	state, err := e.State("")
	if err != nil || len(state) != 1 || !reflect.DeepEqual(state[0], next) {
		t.Fatal(state, err)
	}
	if len(schema.Fields) != 2 {
		t.Fatal("historical input mutated")
	}
	if _, err := Detect([]models.Schema{next}, []models.Schema{schema}, DetectOptions{}); err == nil {
		t.Fatal("removal not gated")
	}
	ops, err = Detect([]models.Schema{next}, []models.Schema{schema}, DetectOptions{AllowRemoveFields: map[string]bool{"shop.Product.note": true}})
	if err != nil || len(ops) != 1 || ops[0].Kind != "remove_field" {
		t.Fatal(ops, err)
	}
	next.Fields[2].Null = false
	if _, err := Detect([]models.Schema{schema}, []models.Schema{next}, DetectOptions{}); err == nil {
		t.Fatal("unsafe nonnull addition accepted")
	}
}

func TestGeneratePreservesUserFilesAndRejectsCallbacks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "migrations")
	m := Migration{App: "shop", Name: "0001_initial", Operations: []Operation{CreateModel(testSchema())}}
	path, err := Generate(directory, m)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "Migration_0001_initial") {
		t.Fatal(string(data), err)
	}
	if _, err := Generate(directory, m); err == nil {
		t.Fatal("existing source overwritten")
	}
	registry := filepath.Join(directory, "registry.go")
	if err := os.WriteFile(registry, []byte("package migrations\n// user owned"), 0644); err != nil {
		t.Fatal(err)
	}
	m.Name = "0002_next"
	if _, err := Generate(directory, m); err == nil {
		t.Fatal("user registry overwritten")
	}
	if _, err := os.Stat(filepath.Join(directory, "0002_next.go")); !os.IsNotExist(err) {
		t.Fatal("partial file created before registry guard")
	}
	m.Operations = []Operation{RunData("copy:v1", func(context.Context, db.Executor) error { return nil }, nil)}
	if _, err := Generate(t.TempDir(), m); err == nil {
		t.Fatal("serialized callback silently dropped")
	}
}
