package migrations

import (
	"container/heap"
	"fmt"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/models"
)

type operationHeap []int

func (q operationHeap) Len() int           { return len(q) }
func (q operationHeap) Less(i, j int) bool { return q[i] < q[j] }
func (q operationHeap) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *operationHeap) Push(value any)    { *q = append(*q, value.(int)) }
func (q *operationHeap) Pop() any {
	old := *q
	value := old[len(old)-1]
	*q = old[:len(old)-1]
	return value
}

type migrationReference struct {
	model string
	field models.Field
}

func constrainedRelation(field models.Field) bool {
	return (field.Kind == models.ForeignKey || field.Kind == models.OneToOne) && field.Relation != nil && !field.Relation.NoConstraint
}

func createsModelStorage(schema models.Schema) bool {
	return !schema.Unmanaged && !schema.Proxy && !schema.Abstract
}

func relationKeys(schema models.Schema, field models.Field) []string {
	if len(field.Relation.TargetFields) > 0 {
		return field.Relation.TargetFields
	}
	keys := []string{}
	for _, key := range schema.PKFields() {
		keys = append(keys, key.Name)
	}
	return keys
}

func sameKeyFields(a, b []string) bool {
	left, right := slices.Clone(a), slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func uniqueOperationFields(operation Operation) []string {
	switch operation.Kind {
	case "add_constraint", "remove_constraint":
		value := operation.Constraint
		if value != nil && strings.EqualFold(value.Kind, "unique") && value.Condition == "" && !value.Deferrable {
			return value.Fields
		}
	case "add_index", "remove_index":
		if operation.Index.Unique && operation.Index.Condition == "" {
			return operation.Index.Fields
		}
	}
	return nil
}

// Order only operations whose state effects have already been detected. Keep
// each model's exact historical snapshots in sequence and the complete rename
// normalization ahead of ordinary diffs. Cross-model database dependencies then
// decide which independently ready operation can run. No live database reads,
// implicit FK retargeting or cyclic-DDL splitting happen during detection.
func orderDetectedOperations(operations []Operation, before, after map[string]models.Schema) ([]Operation, error) {
	modelOps := map[string][]int{}
	removedModels, removedFields := map[string]int{}, map[string]map[string]int{}
	inbound := map[string][]migrationReference{}
	beforeKeys := make([]string, 0, len(before))
	for key := range before {
		beforeKeys = append(beforeKeys, key)
	}
	slices.Sort(beforeKeys)
	for _, key := range beforeKeys {
		schema := before[key]
		if schema.Abstract || schema.Proxy {
			continue
		}
		for _, field := range schema.Fields {
			if constrainedRelation(field) {
				inbound[field.Relation.Target] = append(inbound[field.Relation.Target], migrationReference{key, field})
			}
		}
	}
	edges := make([]map[int]bool, len(operations))
	indegree := make([]int, len(operations))
	link := func(from, to int) {
		if from == to || edges[from][to] {
			return
		}
		if edges[from] == nil {
			edges[from] = map[int]bool{}
		}
		edges[from][to] = true
		indegree[to]++
	}
	lastRename := -1
	for i, operation := range operations {
		key := operation.Schema.Key()
		previous := modelOps[key]
		if len(previous) > 0 {
			link(previous[len(previous)-1], i)
		}
		modelOps[key] = append(previous, i)
		switch operation.Kind {
		case "rename_field":
			if lastRename >= 0 {
				link(lastRename, i)
			}
			lastRename = i
		case "delete_model":
			removedModels[key] = i
		case "remove_field":
			if removedFields[key] == nil {
				removedFields[key] = map[string]int{}
			}
			removedFields[key][operation.Field.Name] = i
		}
	}
	if lastRename >= 0 {
		for i, operation := range operations {
			if operation.Kind != "rename_field" {
				link(lastRename, i)
			}
		}
	}
	for i, operation := range operations {
		var added []models.Field
		switch operation.Kind {
		case "create_model":
			if createsModelStorage(operation.Schema) {
				added = operation.Schema.Fields
			}
		case "add_field":
			added = []models.Field{operation.Field}
		}
		for _, field := range added {
			if !constrainedRelation(field) {
				continue
			}
			target := field.Relation.Target
			if _, removed := removedModels[target]; removed {
				return nil, fmt.Errorf("migrations: new foreign key %s.%s refers to removed model %s", operation.Schema.Key(), field.Name, target)
			}
			keys := relationKeys(after[target], field)
			if targetSchema, known := after[target]; known {
				if len(keys) == 0 {
					return nil, fmt.Errorf("migrations: new foreign key %s.%s requires target fields on %s", operation.Schema.Key(), field.Name, target)
				}
				for _, key := range keys {
					if _, exists := targetSchema.Field(key); !exists {
						return nil, fmt.Errorf("migrations: new foreign key %s.%s refers to missing field %s.%s", operation.Schema.Key(), field.Name, target, key)
					}
				}
			}
			for _, dependency := range modelOps[target] {
				other := operations[dependency]
				switch other.Kind {
				case "create_model":
					// PostgreSQL permits a self-reference within one CREATE.
					if createsModelStorage(other.Schema) {
						link(dependency, i)
					}
				case "add_field", "alter_field":
					if slices.Contains(keys, other.Field.Name) {
						link(dependency, i)
					}
				case "rename_field":
					if slices.Contains(keys, other.Name) {
						link(dependency, i)
					}
				case "add_constraint", "add_index":
					if unique := uniqueOperationFields(other); createsModelStorage(other.Schema) && len(unique) > 0 && sameKeyFields(unique, keys) {
						link(dependency, i)
					}
				}
			}
		}
		if operation.Kind != "delete_model" && operation.Kind != "remove_field" && operation.Kind != "remove_constraint" && operation.Kind != "remove_index" {
			continue
		}
		if operation.Kind != "remove_field" && !createsModelStorage(operation.Schema) {
			continue
		}
		target := operation.Schema.Key()
		for _, reference := range inbound[target] {
			keys := relationKeys(before[target], reference.field)
			dependent := operation.Kind == "delete_model"
			if operation.Kind == "remove_field" {
				dependent = slices.Contains(keys, operation.Field.Name)
			}
			if unique := uniqueOperationFields(operation); len(unique) > 0 {
				dependent = sameKeyFields(unique, keys)
			}
			if !dependent {
				continue
			}
			if removal, exists := removedModels[reference.model]; exists && createsModelStorage(operations[removal].Schema) {
				link(removal, i)
				continue
			}
			if removal, exists := removedFields[reference.model][reference.field.Name]; exists {
				link(removal, i)
				continue
			}
			return nil, fmt.Errorf("migrations: %s.%s retains a foreign-key binding affected by %s on %s; use explicit staged relation operations", reference.model, reference.field.Name, operation.Kind, target)
		}
	}
	ready := &operationHeap{}
	for i, count := range indegree {
		if count == 0 {
			heap.Push(ready, i)
		}
	}
	result := make([]Operation, 0, len(operations))
	for ready.Len() > 0 {
		next := heap.Pop(ready).(int)
		result = append(result, operations[next])
		for child := range edges[next] {
			indegree[child]--
			if indegree[child] == 0 {
				heap.Push(ready, child)
			}
		}
	}
	if len(result) != len(operations) {
		return nil, fmt.Errorf("migrations: operation dependency cycle requires explicit staged foreign-key migrations")
	}
	return result, nil
}
