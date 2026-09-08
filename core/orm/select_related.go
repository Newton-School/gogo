package orm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
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
// Reverse SQL paths use RelatedQueryName (then RelatedName, then the lower-case
// model name). RelatedOne keeps the instance's separate accessor/cache name.
func (q Query[T]) SelectRelated(paths ...string) Query[T] {
	q = q.clone()
	q.relatedPaths = append([]string(nil), paths...)
	return q
}

func (q Query[T]) prepareRelated(ctx context.Context) (Query[T], error) {
	if e := q.checkRoutingShape(); e != nil {
		return q, e
	}
	if q.prepared {
		return q, nil
	}
	if q.err != nil {
		return q, q.err
	}
	q = q.clone()
	if q.modelGrouping || len(q.selectAST.GroupBy) > 0 {
		where, having, err := sqlcompiler.SplitAggregateFilters(q.schema, q.selectAST.Where, q.selectAST.Aliases)
		if err != nil {
			return q, err
		}
		q.selectAST.Where = where
		q.selectAST.Having = And(q.selectAST.Having, having)
	}
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
		scopeSchema := q.schema
		if q.store.routed() {
			scopeSchema = scopeSchema.Clone()
		}
		where, err := q.scope(ctx, scopeSchema)
		where = clonePredicate(where)
		if err != nil {
			return q, err
		}
		q.selectAST.Where = And(q.selectAST.Where, where)
	}
	if e := q.checkRoutingShape(); e != nil {
		return q, e
	}
	if len(q.relatedPaths) == 0 {
		return q.prepareAggregateRelations(ctx)
	}
	if q.store.Registry == nil {
		return q, errors.New("orm: SelectRelated requires a complete model registry")
	}
	q.selectAST.Alias = "gogo_root"
	resolved := map[string]models.Schema{"": q.schema}
	outer := map[string]bool{}
	for _, path := range q.relatedPaths {
		names := strings.Split(path, "__")
		if path == "" || len(names) > 8 {
			return q, errors.New("orm: eager paths require one to eight relation segments")
		}
		parent := ""
		for _, name := range names {
			if !models.ValidIdentifier(name) {
				return q, errors.New("orm: invalid eager relation name")
			}
			currentPath := name
			if parent != "" {
				currentPath = parent + "__" + name
			}
			if _, ok := resolved[currentPath]; ok {
				parent = currentPath
				continue
			}
			parentSchema := resolved[parent]
			prototype, err := models.NewRecord(parentSchema)
			if err != nil {
				return q, err
			}
			binding, err := (RelationManager{Store: q.store, Source: prototype, Name: name}).resolveQuery()
			if err != nil {
				return q, err
			}
			if binding.through != nil || binding.reverse && binding.field.Kind != models.OneToOne {
				return q, errors.New("orm: SelectRelated supports to-one paths only")
			}
			cacheName := name
			if binding.reverse {
				cacheName = reverseAccessorName(binding.target, binding.field)
				if strings.HasSuffix(cacheName, "+") {
					return q, errors.New("orm: SelectRelated requires a visible relation accessor")
				}
				accessor, err := (RelationManager{Store: q.store, Source: prototype, Name: cacheName}).resolve()
				if err != nil || !accessor.reverse || accessor.target.Key() != binding.target.Key() || accessor.field.Name != binding.field.Name {
					return q, errors.New("orm: SelectRelated requires an unambiguous instance accessor")
				}
			}
			// Repeating a schema is valid for a finite, explicit self/reverse
			// path. Each path owns a distinct join alias; the segment bound
			// prevents unbounded traversal without rejecting self relations.
			join := db.Join{Path: currentPath, Alias: fmt.Sprintf("gogo_join_%d", len(q.joined)+1), ParentPath: parent, Schema: binding.target}
			// Required unscoped forward links can use INNER JOIN and participate
			// in ordinary FOR UPDATE. Nullable ancestors and scoped targets must
			// stay outer joined, so an absent/hidden target never removes a root.
			join.Inner = !binding.reverse && !binding.field.Null && q.scope == nil && !outer[parent]
			outer[currentPath] = !join.Inner
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
				join.Where = clonePredicate(join.Where)
				if err != nil {
					return q, err
				}
			}
			node := joinedRelation{path: currentPath, parent: parent, name: cacheName, schema: binding.target, offset: len(q.selectAST.Fields)}
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
	return q.prepareAggregateRelations(ctx)
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
			value, err := q.store.decodeField(field, values[node.offset+index])
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
