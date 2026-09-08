package migrations

import (
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// Start/end metadata cannot describe a target created and deleted inside one
// migration. Refuse that unsupported transient graph before any DDL, rather
// than infer a target type or table from a same-key operation snapshot.
func validateAutomaticTransitionTargets(migration Migration, before, after []models.Schema) error {
	known := make(map[string]bool, len(before)+len(after))
	for _, schemas := range [][]models.Schema{before, after} {
		for _, schema := range schemas {
			known[schema.Key()] = true
		}
	}
	for _, operation := range migration.Operations {
		var fields []models.Field
		switch operation.Kind {
		case "create_model":
			if createsModelStorage(operation.Schema) {
				fields = operation.Schema.Fields
			}
		case "add_field":
			fields = []models.Field{operation.Field}
		}
		for _, field := range fields {
			if field.Kind == models.ManyToMany && field.Relation != nil && field.Relation.Through == "" && !known[field.Relation.Target] {
				return &db.Error{Code: db.UnsupportedFeature, Message: "Automatic relations to transient targets require separate migrations"}
			}
		}
	}
	return nil
}

func removesAutomaticRelations(before, after []models.Schema) bool {
	current := make(map[string]models.Schema, len(after))
	for _, schema := range after {
		current[schema.Key()] = schema
	}
	for _, source := range before {
		if !createsModelStorage(source) {
			continue
		}
		for _, field := range source.Fields {
			if field.Kind != models.ManyToMany || field.Relation == nil || field.Relation.Through != "" {
				continue
			}
			newSource, sourceExists := current[source.Key()]
			newField, fieldExists := newSource.Field(field.Name)
			_, targetExists := current[field.Relation.Target]
			if !sourceExists || !fieldExists || !targetExists || newField.Kind != models.ManyToMany || newField.Relation == nil || newField.Relation.Through != "" || newField.Relation.Target != field.Relation.Target {
				return true
			}
		}
	}
	return false
}

func needsAutomaticTransition(migration Migration, before, after []models.Schema) bool {
	if removesAutomaticRelations(before, after) {
		return true
	}
	// A remove/recreate can have identical boundary descriptors while still
	// destroying the original bridge. Do not let a legacy resolver infer its
	// deletion inventory from the newly created physical table.
	for _, source := range before {
		if !createsModelStorage(source) {
			continue
		}
		for _, field := range source.Fields {
			if field.Kind != models.ManyToMany || field.Relation == nil || field.Relation.Through != "" {
				continue
			}
			for _, operation := range migration.Operations {
				if operation.Kind == "delete_model" && createsModelStorage(operation.Schema) && (operation.Schema.Key() == source.Key() || operation.Schema.Key() == field.Relation.Target) {
					return true
				}
				if operation.Kind == "remove_field" && operation.Schema.Key() == source.Key() && operation.Field.Name == field.Name {
					return true
				}
			}
		}
	}
	return false
}
