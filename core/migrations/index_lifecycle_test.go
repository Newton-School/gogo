package migrations

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestNamedIndexesDetectExactStateAndDeclarationOrder(t *testing.T) {
	a := models.Index{Name: "index_a", Fields: []string{"title"}}
	c := models.Index{Name: "index_c", Fields: []string{"id"}}
	b := models.Index{Name: "index_b", Fields: []string{"title"}, Include: []string{"id"}}
	for _, mode := range []string{"add_before", "remove", "remove_all", "replace", "reorder", "field_dependency"} {
		t.Run(mode, func(t *testing.T) {
			before := testSchema()
			before.Indexes = []models.Index{a, c}
			after := before.Clone()
			switch mode {
			case "add_before":
				after.Indexes = []models.Index{b, a, c}
			case "remove":
				after.Indexes = []models.Index{c}
			case "remove_all":
				after.Indexes = nil
			case "replace":
				after.Indexes[0].Unique = true
				after.Indexes[0].Condition = "title <> ''"
			case "reorder":
				after.Indexes = []models.Index{c, a}
			case "field_dependency":
				after.Fields = append(after.Fields, models.TextField("note", models.Nullable))
				after.Indexes[0].Fields = []string{"note"}
			}
			operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{})
			if err != nil || len(operations) == 0 {
				t.Fatal("index drift was ignored", operations, err)
			}
			if mode == "field_dependency" && (operations[0].Kind != "remove_index" || operations[1].Kind != "add_field" || operations[2].Kind != "add_index") {
				t.Fatal("index/field dependency ordering", operations)
			}
			if mode == "reorder" && (len(operations) != 1 || operations[0].Kind != "index_order") {
				t.Fatal("index declaration order caused storage work", operations)
			}
			change := Migration{App: "shop", Name: "0002", Dependencies: []string{"shop.0001"}, Operations: operations}
			encoded, err := json.Marshal(change)
			if err != nil {
				t.Fatal(err)
			}
			e := Executor{Migrations: []Migration{{App: "shop", Name: "0001", Operations: []Operation{CreateModel(before)}}, MustDecode(string(encoded))}}
			state, err := e.State("")
			if err != nil || len(state) != 1 || !reflect.DeepEqual(state[0], after) {
				t.Fatal("index history differs from target descriptor", state, err)
			}
			if next, err := Detect(state, []models.Schema{after}, DetectOptions{}); err != nil || len(next) != 0 {
				t.Fatal("index history kept reporting drift", next, err)
			}
			if len(state[0].Indexes) > 0 {
				state[0].Indexes[0].Fields[0] = "mutated"
				if before.Indexes[0].Fields[0] != "title" || after.Indexes[0].Fields[0] == "mutated" {
					t.Fatal("index snapshots alias caller metadata")
				}
			}
		})
	}
}

func TestNamedIndexUnsupportedMetadataNeverReportsNoDrift(t *testing.T) {
	for _, mode := range []string{"options", "concurrent_add", "concurrent_remove", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			before, after := testSchema(), testSchema()
			index := models.Index{Name: "named", Fields: []string{"title"}, Concurrent: true}
			switch mode {
			case "options":
				after.Ordering = []string{"title"}
			case "concurrent_add":
				after.Indexes = []models.Index{index}
			case "concurrent_remove":
				before.Indexes = []models.Index{index}
			case "duplicate":
				after.Indexes = []models.Index{index, index}
			}
			if operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{}); err == nil || len(operations) != 0 {
				t.Fatal("unsupported metadata silently ignored", operations, err)
			}
		})
	}
}

func TestNamedIndexSnapshotsAndOptionalRemovalContract(t *testing.T) {
	index := models.Index{Name: "named", Fields: []string{"title"}, Include: []string{"id"}}
	operation := AddIndex(testSchema(), index)
	index.Fields[0], index.Include[0] = "changed", "changed"
	if operation.Index.Fields[0] != "title" || operation.Index.Include[0] != "id" {
		t.Fatal("constructor retained mutable index")
	}
	encoded, err := json.Marshal(operation)
	if err != nil || strings.Contains(string(encoded), "IndexOrder") {
		t.Fatal("new optional metadata changed legacy checksum shape", string(encoded), err)
	}
	legacy := models.Index{Name: "legacy", Fields: []string{"title"}, Include: []string{}}
	if copied := AddIndex(testSchema(), legacy); copied.Index.Include == nil {
		t.Fatal("index cloning changed a legacy empty-slice checksum shape")
	}
	e := Executor{}
	if err := e.run(context.Background(), nil, operation, true); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("provider without safe removal contract accepted reversal", err)
	}
}

type noIndexIOBackend struct{ db.Backend }
type noIndexIOEditor struct{ db.SchemaEditor }
type safeIndexIOEditor struct{ noIndexIOEditor }

func (safeIndexIOEditor) RemoveModelIndex(context.Context, db.Executor, models.Schema, models.Index) error {
	panic("index preflight unexpectedly reached storage")
}

func TestNamedIndexPreflightRejectsUnsupportedModesBeforeAnyIO(t *testing.T) {
	for _, mode := range []string{"provider", "non_atomic_removal", "atomic_concurrent_add", "atomic_concurrent_model", "reverse_concurrent", "reverse_add_provider"} {
		t.Run(mode, func(t *testing.T) {
			index := models.Index{Name: "named", Fields: []string{"title"}}
			schema := testSchema()
			schema.Indexes = []models.Index{index}
			migration := Migration{App: "shop", Name: "0001", Operations: []Operation{RemoveIndex(schema, index)}}
			e := Executor{Backend: noIndexIOBackend{}, Editor: safeIndexIOEditor{}}
			reverse := false
			switch mode {
			case "provider":
				e.Editor = noIndexIOEditor{}
			case "non_atomic_removal":
				migration.NonAtomic = true
			case "atomic_concurrent_add":
				index.Concurrent = true
				migration.Operations = []Operation{AddIndex(schema, index)}
			case "atomic_concurrent_model":
				schema.Indexes[0].Concurrent = true
				migration.Operations = []Operation{CreateModel(schema)}
			case "reverse_concurrent":
				index.Concurrent = true
				migration.NonAtomic = true
				migration.Operations = []Operation{AddIndex(schema, index)}
				reverse = true
			case "reverse_add_provider":
				e.Editor = noIndexIOEditor{}
				migration.Operations = []Operation{AddIndex(schema, index)}
				reverse = true
			}
			e.Migrations = []Migration{migration}
			if statements, err := e.SQL(context.Background(), migration.Key(), reverse); !db.IsCode(err, db.UnsupportedFeature) || len(statements) != 0 {
				t.Fatal("unsupported index mode produced SQL", statements, err)
			}
			if !reverse {
				if err := e.Apply(context.Background(), migration.Key()); !db.IsCode(err, db.UnsupportedFeature) {
					t.Fatal("unsupported index mode reached apply IO", err)
				}
			}
		})
	}
}
