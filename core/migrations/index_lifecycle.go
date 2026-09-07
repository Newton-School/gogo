package migrations

import (
	"errors"
	"reflect"
	"slices"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func cloneIndex(index models.Index) models.Index {
	index.Fields, index.Include = slices.Clone(index.Fields), slices.Clone(index.Include)
	if index.NullsDistinct != nil {
		value := *index.NullsDistinct
		index.NullsDistinct = &value
	}
	return index
}

func indexEquivalent(a, b models.Index) bool {
	if !slices.Equal(a.Fields, b.Fields) || !slices.Equal(a.Include, b.Include) {
		return false
	}
	a.Fields, b.Fields, a.Include, b.Include = nil, nil, nil, nil
	return reflect.DeepEqual(a, b)
}

func indexNames(indexes []models.Index) []string {
	names := make([]string, len(indexes))
	for i, index := range indexes {
		names[i] = index.Name
	}
	return names
}

func detectIndexes(before, after models.Schema) (remove, add []Operation, order *Operation, err error) {
	old, next := map[string]models.Index{}, map[string]models.Index{}
	for _, index := range before.Indexes {
		if _, duplicate := old[index.Name]; duplicate {
			return nil, nil, nil, errors.New("migrations: historical index names are ambiguous")
		}
		old[index.Name] = index
	}
	for _, index := range after.Indexes {
		next[index.Name] = index
	}
	result := []models.Index{}
	for _, index := range before.Indexes {
		other, exists := next[index.Name]
		if exists && indexEquivalent(index, other) {
			result = append(result, index)
			continue
		}
		if index.Concurrent || other.Concurrent {
			return nil, nil, nil, &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent index changes require an explicit online migration"}
		}
		remove = append(remove, RemoveIndex(before, index))
	}
	for _, index := range after.Indexes {
		other, exists := old[index.Name]
		if exists && indexEquivalent(index, other) {
			continue
		}
		if index.Concurrent || other.Concurrent {
			return nil, nil, nil, &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent index changes require an explicit online migration"}
		}
		add = append(add, AddIndex(after, index))
		result = append(result, index)
	}
	currentOrder, desiredOrder := indexNames(result), indexNames(after.Indexes)
	if !slices.Equal(currentOrder, desiredOrder) {
		op := Operation{Kind: "index_order", Schema: after.Clone(), OldIndexOrder: currentOrder, IndexOrder: desiredOrder}
		order = &op
	}
	return remove, add, order, nil
}

func applyIndexState(schema models.Schema, operation Operation) (models.Schema, error) {
	schema = schema.Clone()
	position := -1
	for i, index := range schema.Indexes {
		if index.Name == operation.Index.Name {
			position = i
			break
		}
	}
	switch operation.Kind {
	case "add_index":
		if position >= 0 {
			return schema, errors.New("migrations: index name already exists in historical state")
		}
		schema.Indexes = append(schema.Indexes, cloneIndex(operation.Index))
	case "remove_index":
		if position < 0 || !indexEquivalent(schema.Indexes[position], operation.Index) {
			return schema, errors.New("migrations: removed index differs from historical state")
		}
		schema.Indexes = append(schema.Indexes[:position], schema.Indexes[position+1:]...)
		if len(schema.Indexes) == 0 {
			schema.Indexes = nil
		}
	case "index_order":
		if !slices.Equal(indexNames(schema.Indexes), operation.OldIndexOrder) || len(operation.IndexOrder) != len(schema.Indexes) {
			return schema, errors.New("migrations: index order differs from historical state")
		}
		byName := map[string]models.Index{}
		for _, index := range schema.Indexes {
			byName[index.Name] = index
		}
		ordered := make([]models.Index, len(operation.IndexOrder))
		for i, name := range operation.IndexOrder {
			index, exists := byName[name]
			if !exists {
				return schema, errors.New("migrations: index order must name every index exactly once")
			}
			ordered[i] = index
			delete(byName, name)
		}
		schema.Indexes = ordered
	}
	return schema, schema.Validate()
}

func supportedModelStateChange(before, after models.Schema) error {
	before, after = before.Clone(), after.Clone()
	before.Fields, after.Fields = nil, nil
	before.Indexes, after.Indexes = nil, nil
	before.Constraints, after.Constraints = nil, nil
	if !reflect.DeepEqual(before, after) {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Model option changes require explicit state-aware model operations"}
	}
	return nil
}

func fieldStateAfter(schema models.Schema, old string, field models.Field) models.Schema {
	schema = schema.Clone()
	field = (models.Schema{Fields: []models.Field{field}}).Clone().Fields[0]
	for i := range schema.Fields {
		if schema.Fields[i].Name == old {
			schema.Fields[i] = field
			return schema
		}
	}
	schema.Fields = append(schema.Fields, field)
	return schema
}
