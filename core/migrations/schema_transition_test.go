package migrations

import (
	"context"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func transitionMigrations() []Migration {
	target := models.Schema{AppLabel: "shop", Name: "Tag", Fields: []models.Field{models.BigAutoField("id")}}
	source := testSchema()
	field := models.ManyToManyField("tags", models.Relation{Target: target.Key()})
	initial := Migration{App: "shop", Name: "0001", Operations: []Operation{CreateModel(source), CreateModel(target)}}
	added := Migration{App: "shop", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []Operation{AddField(source, field)}}
	source.Fields = append(source.Fields, field)
	removed := Migration{App: "shop", Name: "0003", Dependencies: []string{added.Key()}, Operations: []Operation{DeleteModel(source)}}
	return []Migration{initial, added, removed}
}

type transitionCapture struct {
	noIndexIOEditor
	before, after []models.Schema
}

func (p *transitionCapture) WithSchemaTransition(before, after []models.Schema) (db.SchemaEditor, error) {
	p.before, p.after = before, after
	return noIndexIOEditor{}, nil
}

func TestSchemaTransitionExactDirectionAndDetachedDescriptors(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			probe := &transitionCapture{}
			e := Executor{Editor: probe, Migrations: transitionMigrations()}
			beforeSum, _ := e.Migrations[1].Checksum()
			if _, err := e.withHistoricalSchemas("shop.0003", reverse); err != nil {
				t.Fatal(err)
			}
			before, _ := e.State("shop.0002")
			after, _ := e.State("shop.0003")
			if reverse {
				before, after = after, before
			}
			if !reflect.DeepEqual(probe.before, before) || !reflect.DeepEqual(probe.after, after) {
				t.Fatal("transition snapshots did not match exact directional states")
			}
			for _, schemas := range [][]models.Schema{probe.before, probe.after} {
				for _, schema := range schemas {
					for _, field := range schema.Fields {
						if field.Relation != nil {
							field.Relation.Target = "mutated.Target"
						}
					}
				}
			}
			afterSum, _ := e.Migrations[1].Checksum()
			if beforeSum != afterSum {
				t.Fatal("provider mutation changed historical operation checksum")
			}
		})
	}
}

func TestAutomaticRelationRemovalRequiresTransitionBeforeIO(t *testing.T) {
	e := Executor{Backend: noIndexIOBackend{}, Editor: noIndexIOEditor{}, Migrations: transitionMigrations()}
	if statements, err := e.SQL(context.Background(), "shop.0003", false); !db.IsCode(err, db.UnsupportedFeature) || len(statements) != 0 {
		t.Fatal(statements, err)
	}
	if err := e.Apply(context.Background(), "shop.0003"); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("missing transition provider reached storage", err)
	}
	if _, err := e.withHistoricalSchemas("shop.0001", false); err != nil {
		t.Fatal("unaffected provider falsely required transition support", err)
	}
	if _, err := e.withHistoricalSchemas("shop.0002", true); err != nil {
		t.Fatal("legacy reverse add-field resolver rejected", err)
	}
	for _, mode := range []string{"explicit", "unmanaged", "proxy", "abstract"} {
		t.Run(mode, func(t *testing.T) {
			source := e.Migrations[2].Operations[0].Schema.Clone()
			switch mode {
			case "explicit":
				source.Fields[2].Relation.Through = "shop.Link"
			case "unmanaged":
				source.Unmanaged = true
			case "proxy":
				source.Proxy = true
			case "abstract":
				source.Abstract = true
			}
			if removesAutomaticRelations([]models.Schema{source}, nil) {
				t.Fatal("nonautomatic/nonconcrete relation required transition capability")
			}
		})
	}
}

func TestAutomaticRelationTransientTargetRefusedBeforeIO(t *testing.T) {
	migrations := transitionMigrations()
	source := migrations[2].Operations[0].Schema
	target := migrations[0].Operations[1].Schema
	migration := Migration{App: "shop", Name: "0001", NonAtomic: true, Operations: []Operation{CreateModel(source), CreateModel(target), DeleteModel(source), DeleteModel(target)}}
	e := Executor{Backend: noIndexIOBackend{}, Editor: &transitionCapture{}, Migrations: []Migration{migration}}
	for _, reverse := range []bool{false, true} {
		if statements, err := e.SQL(context.Background(), migration.Key(), reverse); !db.IsCode(err, db.UnsupportedFeature) || len(statements) != 0 {
			t.Fatal("transient target produced executable SQL", statements, err)
		}
	}
	if err := e.Apply(context.Background(), migration.Key()); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("transient target reached storage", err)
	}
}

func TestAutomaticRelationIdenticalBoundaryReplacementRequiresTransition(t *testing.T) {
	migrations := transitionMigrations()
	source := migrations[2].Operations[0].Schema
	migrations[2].Operations = []Operation{DeleteModel(source), CreateModel(source)}
	e := Executor{Backend: noIndexIOBackend{}, Editor: noIndexIOEditor{}, Migrations: migrations}
	if err := e.Apply(context.Background(), "shop.0003"); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("same-boundary physical replacement reached storage without transition capability", err)
	}
}
