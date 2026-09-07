package migrations

import (
	"errors"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/models"
)

// Logical renames preserve an explicit database column. Only an implicit
// column follows the new logical name. Derived references move with the field;
// trusted SQL fragments cannot be rewritten safely by string substitution.
func renameFieldState(schema models.Schema, old, new string) (models.Schema, error) {
	if _, exists := schema.Field(old); !exists {
		return schema, errors.New("migrations: renamed field does not exist")
	}
	if _, exists := schema.Field(new); exists || !models.ValidIdentifier(new) {
		return schema, errors.New("migrations: invalid or colliding renamed field")
	}
	for _, index := range schema.Indexes {
		if index.Condition != "" {
			return schema, errors.New("migrations: field rename with conditional indexes requires explicit state and SQL operations")
		}
	}
	for _, constraint := range schema.Constraints {
		if constraint.Condition != "" || constraint.Expression != "" {
			return schema, errors.New("migrations: field rename with SQL expressions requires explicit state and SQL operations")
		}
	}
	schema = schema.Clone()
	rename := func(names []string) {
		for i, name := range names {
			if name == old {
				names[i] = new
			}
		}
	}
	for i := range schema.Fields {
		f := &schema.Fields[i]
		if f.Name == old {
			f.Name = new
		}
		if f.UniqueForDate == old {
			f.UniqueForDate = new
		}
		if f.UniqueForMonth == old {
			f.UniqueForMonth = new
		}
		if f.UniqueForYear == old {
			f.UniqueForYear = new
		}
		renameRelationFieldReferences(f.Relation, schema.Key(), old, new)
	}
	rename(schema.PrimaryKey)
	for i := range schema.Indexes {
		rename(schema.Indexes[i].Fields)
		rename(schema.Indexes[i].Include)
	}
	for i := range schema.Constraints {
		rename(schema.Constraints[i].Fields)
	}
	for i, name := range schema.Ordering {
		if strings.TrimPrefix(name, "-") == old {
			schema.Ordering[i] = strings.TrimSuffix(name, old) + new
		}
	}
	if schema.ParentLink == old {
		schema.ParentLink = new
	}
	return schema, nil
}

func renameRelationFieldReferences(relation *models.Relation, model, old, new string) {
	if relation == nil {
		return
	}
	rename := func(fields []string) {
		for i, name := range fields {
			if name == old {
				fields[i] = new
			}
		}
	}
	if relation.Target == model {
		rename(relation.TargetFields)
	}
	if relation.Through == model {
		rename(relation.ThroughFields)
	}
}

// Rename operations precede ordinary field comparisons. Renames also update
// inbound relation metadata, including models that sort before the field owner;
// those derived state changes must not become independent relation alterations.
func detectFieldRenames(before, after map[string]models.Schema, renames map[string]string) ([]Operation, error) {
	keys := make([]string, 0, len(renames))
	for key := range renames {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	operations := []Operation{}
	for _, reference := range keys {
		separator := strings.LastIndexByte(reference, '.')
		if separator < 0 {
			return nil, errors.New("migrations: field rename requires a model-qualified source")
		}
		key, old, new := reference[:separator], reference[separator+1:], renames[reference]
		schema, exists := before[key]
		next, retained := after[key]
		if !exists || !retained {
			return nil, errors.New("migrations: field rename requires an existing retained model")
		}
		if _, exists := next.Field(new); !exists {
			return nil, errors.New("migrations: renamed field is missing from target state")
		}
		renamed, err := renameFieldState(schema, old, new)
		if err != nil {
			return nil, err
		}
		operations = append(operations, RenameField(schema, old, new))
		before[key] = renamed
		for otherKey, other := range before {
			if otherKey == key {
				continue
			}
			other = other.Clone()
			for i := range other.Fields {
				renameRelationFieldReferences(other.Fields[i].Relation, key, old, new)
			}
			before[otherKey] = other
		}
	}
	return operations, nil
}
