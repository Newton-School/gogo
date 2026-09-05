package orm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// QueryScope applies trusted application scope independently to every schema.
// Joined target predicates belong in ON, preserving nullable/hidden relations.
type QueryScope func(context.Context, models.Schema) (db.Predicate, error)
type joinedRelation struct {
	path, parent, name string
	schema             models.Schema
	offset             int
	fields             []string
}

func (q Query[T]) WithScope(scope QueryScope) Query[T] { q = q.clone(); q.scope = scope; return q }

// SelectRelated joins explicit to-one paths in the root SQL statement. Calling
// it with no paths clears selected joins; collections require PrefetchRelated.
func (q Query[T]) SelectRelated(paths ...string) Query[T] {
	q = q.clone()
	q.relatedPaths = append([]string(nil), paths...)
	return q
}

func (q Query[T]) prepareRelated(ctx context.Context) (Query[T], error) {
	if q.prepared {
		return q, nil
	}
	if q.err != nil {
		return q, q.err
	}
	q = q.clone()
	if q.selectAST.ForUpdate {
		if err := q.store.Backend.Capabilities().Require("row_locks"); err != nil {
			return q, err
		}
		if len(q.selectAST.LockOf) > 0 {
			if err := q.store.Backend.Capabilities().Require("row_lock_of"); err != nil {
				return q, err
			}
		}
		if q.selectAST.NoKey {
			if err := q.store.Backend.Capabilities().Require("row_lock_no_key"); err != nil {
				return q, err
			}
		}
	}
	if q.scope != nil {
		where, err := q.scope(ctx, q.schema)
		if err != nil {
			return q, err
		}
		q.selectAST.Where = And(q.selectAST.Where, where)
	}
	if len(q.relatedPaths) == 0 {
		q.prepared = true
		return q, nil
	}
	if q.store.Registry == nil {
		return q, errors.New("orm: SelectRelated requires a complete model registry")
	}
	q.selectAST.Alias = "gogo_root"
	resolved := map[string]models.Schema{"": q.schema}
	for _, path := range q.relatedPaths {
		names := strings.Split(path, "__")
		if path == "" || len(names) > 8 {
			return q, errors.New("orm: eager paths require one to eight relation segments")
		}
		parent := ""
		ancestors := map[string]bool{q.schema.Key(): true}
		for _, name := range names {
			if !models.ValidIdentifier(name) {
				return q, errors.New("orm: invalid eager relation name")
			}
			currentPath := name
			if parent != "" {
				currentPath = parent + "__" + name
			}
			if existing, ok := resolved[currentPath]; ok {
				ancestors[existing.Key()] = true
				parent = currentPath
				continue
			}
			parentSchema := resolved[parent]
			prototype, err := models.NewRecord(parentSchema)
			if err != nil {
				return q, err
			}
			binding, err := (RelationManager{Store: q.store, Source: prototype, Name: name}).resolve()
			if err != nil {
				return q, err
			}
			if binding.through != nil || binding.reverse && binding.field.Kind != models.OneToOne {
				return q, errors.New("orm: SelectRelated supports to-one paths only")
			}
			if ancestors[binding.target.Key()] {
				return q, errors.New("orm: cyclic eager path rejected")
			}
			ancestors[binding.target.Key()] = true
			join := db.Join{Path: currentPath, Alias: fmt.Sprintf("gogo_join_%d", len(q.joined)+1), ParentPath: parent, Schema: binding.target}
			if binding.reverse {
				key, err := relationTargetField(parentSchema, binding.field)
				if err != nil {
					return q, err
				}
				join.ParentField = key.Name
				join.TargetField = binding.field.Name
			} else {
				key, err := relationTargetField(binding.target, binding.field)
				if err != nil {
					return q, err
				}
				join.ParentField = binding.field.Name
				join.TargetField = key.Name
			}
			if parent == "" {
				selected := false
				for _, field := range q.selectAST.Fields {
					if field == join.ParentField {
						selected = true
						break
					}
				}
				if !selected {
					return q, errors.New("orm: selected relation connector field cannot be deferred")
				}
			}
			if q.scope != nil {
				join.Where, err = q.scope(ctx, binding.target)
				if err != nil {
					return q, err
				}
			}
			node := joinedRelation{path: currentPath, parent: parent, name: name, schema: binding.target, offset: len(q.selectAST.Fields)}
			for _, field := range binding.target.Fields {
				if field.IsStored() {
					node.fields = append(node.fields, field.Name)
					q.selectAST.Fields = append(q.selectAST.Fields, currentPath+"__"+field.Name)
				}
			}
			q.joined = append(q.joined, node)
			q.selectAST.Joins = append(q.selectAST.Joins, join)
			resolved[currentPath] = binding.target
			parent = currentPath
		}
	}
	q.prepared = true
	return q, nil
}

func (q Query[T]) attachJoined(root models.Record, values []any) error {
	attached := map[string]models.Record{"": root}
	for _, node := range q.joined {
		parent := attached[node.parent]
		if parent == nil {
			continue
		}
		if parent.State().Related == nil {
			parent.State().Related = map[string]any{}
		}
		present := false
		for index, name := range node.fields {
			for _, pk := range node.schema.PKFields() {
				if name == pk.Name && values[node.offset+index] != nil {
					present = true
				}
			}
		}
		if !present {
			parent.State().Related[node.name] = nil
			continue
		}
		record, err := models.NewRecord(node.schema)
		if err != nil {
			return err
		}
		for index, name := range node.fields {
			field, _ := node.schema.Field(name)
			value, err := decodeField(field, values[node.offset+index])
			if err != nil {
				return err
			}
			if err := record.Set(name, value); err != nil {
				return err
			}
		}
		record.State().Persisted = true
		record.State().Database = q.store.Backend.Alias()
		parent.State().Related[node.name] = models.Record(record)
		attached[node.path] = record
	}
	return nil
}

// RelatedOne reads an instance-local SelectRelated snapshot. The bool distinguishes
// an explicitly empty relation from a path that was never eagerly loaded.
func RelatedOne(record models.Record, name string) (models.Record, bool) {
	if record == nil || record.State() == nil {
		return nil, false
	}
	value, ok := record.State().Related[name]
	if !ok {
		return nil, false
	}
	if value == nil {
		return nil, true
	}
	related, ok := value.(models.Record)
	return related, ok
}
