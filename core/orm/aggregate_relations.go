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

// prepareAggregateRelations adds non-hydrating FK joins only. Multiple visible
// collections have ordinary SQL multiplication semantics; callers explicitly
// request DISTINCT aggregates when that is the desired counting operation.
func (q Query[T]) prepareAggregateRelations(ctx context.Context) (Query[T], error) {
	if err := ctx.Err(); err != nil {
		return q, err
	}
	names, err := sqlcompiler.AggregateFieldReferences(q.schema, q.selectAST.Projections, q.selectAST.Aliases)
	if err != nil {
		return q, err
	}
	resolved := map[string]models.Schema{"": q.schema}
	for _, join := range q.selectAST.Joins {
		resolved[join.Path] = join.Schema
	}
	for _, name := range names {
		parent, source := "", q.schema
		parts := strings.Split(name, "__")
		for index, component := range parts[:len(parts)-1] {
			if err := ctx.Err(); err != nil {
				return q, err
			}
			// Preserve exact stored names and JSON transforms without inventing
			// relation traversal or requiring a registry for scalar JSON fields.
			if _, stored := source.Field(strings.Join(parts[index:], "__")); stored {
				break
			}
			if field, stored := source.Field(component); stored && field.Kind == models.JSON {
				break
			}
			if !models.ValidIdentifier(component) || index >= 8 {
				return q, errors.New("orm: aggregate relation paths require at most eight valid segments")
			}
			path := component
			if parent != "" {
				path = parent + "__" + component
			}
			if schema, exists := resolved[path]; exists {
				parent, source = path, schema
				continue
			}
			if q.store.Registry == nil {
				return q, errors.New("orm: aggregate relation joins require a complete model registry")
			}
			if len(q.selectAST.Joins) >= 64 {
				return q, errors.New("orm: aggregate queries require at most 64 resolved relation joins")
			}
			prototype, err := models.NewRecord(source)
			if err != nil {
				return q, err
			}
			binding, err := (RelationManager{Store: q.store, Source: prototype, Name: component}).resolve()
			if err != nil {
				return q, err
			}
			if binding.through != nil {
				return q, &db.Error{Code: db.UnsupportedFeature, Message: "Many-to-many aggregate joins require a scoped intermediary/target join group"}
			}
			join := db.Join{Path: path, ParentPath: parent, Alias: fmt.Sprintf("gogo_join_%d", len(q.selectAST.Joins)+1), Schema: binding.target}
			if binding.reverse {
				key, err := relationTargetField(source, binding.field)
				if err != nil {
					return q, err
				}
				join.ParentField, join.TargetField = key.Name, binding.field.Name
			} else {
				key, err := relationTargetField(binding.target, binding.field)
				if err != nil {
					return q, err
				}
				join.ParentField, join.TargetField = binding.field.Name, key.Name
			}
			if q.scope != nil {
				join.Where, err = q.scope(ctx, binding.target)
				if canceled := ctx.Err(); canceled != nil {
					return q, canceled
				}
				if err != nil {
					return q, err
				}
			}
			q.selectAST.Alias = "gogo_root"
			q.selectAST.Joins = append(q.selectAST.Joins, join)
			resolved[path] = binding.target
			parent, source = path, binding.target
		}
	}
	q.prepared = true
	return q, nil
}
