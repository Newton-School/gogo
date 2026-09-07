package migrations

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestFieldIndexAndLogicalRenameHistoricalState(t *testing.T) {
	for _, column := range []string{"", "stored_name"} {
		t.Run(column, func(t *testing.T) {
			before := models.Schema{AppLabel: "tests", Name: "Article", Fields: []models.Field{models.BigAutoField("id"), models.SlugField("slug", models.WithColumn(column))}, Indexes: []models.Index{{Name: "explicit_slug", Fields: []string{"slug"}}}, Ordering: []string{"-slug"}}
			after := before.Clone()
			after.Fields[1].Name = "heading"
			after.Fields[1].DBIndex = false
			after.Indexes[0].Fields[0] = "heading"
			after.Ordering = []string{"-heading"}
			operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{Renames: map[string]string{before.Key() + ".slug": "heading"}})
			if err != nil || len(operations) != 2 || operations[0].Kind != "rename_field" || operations[1].Kind != "alter_field" || operations[1].OldField.Column != column || operations[1].OldField.Name != "heading" || operations[1].Field.DBIndex {
				t.Fatal("rename/toggle detection", operations, err)
			}
			initial := Migration{App: "tests", Name: "0001", Operations: []Operation{CreateModel(before)}}
			migration := Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
			encoded, err := json.Marshal(migration)
			if err != nil {
				t.Fatal(err)
			}
			executor := Executor{Migrations: []Migration{initial, MustDecode(string(encoded))}}
			state, err := executor.State("")
			if err != nil || len(state) != 1 || !reflect.DeepEqual(state[0], after) {
				t.Fatal("rename/index historical metadata mismatch", state, err)
			}
			if next, err := Detect(state, []models.Schema{after}, DetectOptions{}); err != nil || len(next) != 0 {
				t.Fatal("index metadata kept generating changes", next, err)
			}
			if before.Fields[1].Name != "slug" || before.Indexes[0].Fields[0] != "slug" {
				t.Fatal("historical state mutated input")
			}
		})
	}
}

func TestFieldRenameKeepsColumnChangeAndRejectsAmbiguity(t *testing.T) {
	before := models.Schema{AppLabel: "tests", Name: "Article", Fields: []models.Field{models.BigAutoField("id"), models.SlugField("slug", models.WithColumn("stored_old"))}}
	after := before.Clone()
	after.Fields[1].Name = "heading"
	after.Fields[1].Column = "stored_new"
	operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{Renames: map[string]string{before.Key() + ".slug": "heading"}})
	if err != nil || len(operations) != 2 || operations[1].OldField.DBColumn() != "stored_old" || operations[1].Field.DBColumn() != "stored_new" {
		t.Fatal("physical column change was discarded", operations, err)
	}
	before.Fields = append(before.Fields, models.TextField("other"))
	if _, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{Renames: map[string]string{before.Key() + ".slug": "heading", before.Key() + ".other": "heading"}}); err == nil {
		t.Fatal("ambiguous rename picked a random source")
	}
}
