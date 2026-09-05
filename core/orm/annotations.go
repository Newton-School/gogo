package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

// Annotate selects named, typed per-row expressions. Their decoded values live
// in each result's ModelState().Annotations and never become stored model
// fields. Values can select those names; filters and ordering can refer to
// them. References to related records require explicit scoped SelectRelated
// paths. Output declares decoding, not a conversion: use Cast to convert a
// nonliteral expression. Literal values are bound with their declared SQL type.
//
// Existing aliases cannot be redefined. Aggregate/window annotations require
// a separate grouped execution path and are rejected by this row-expression
// API. Query construction snapshots metadata/data and invokes no provider hook.
func (q Query[T]) Annotate(expressions map[string]ResultExpression) Query[T] {
	q = q.clone()
	if q.err != nil {
		return q
	}
	if len(expressions) == 0 || len(q.selectAST.Aliases)+len(expressions) > 128 {
		q.err = errors.New("orm: annotations require between one and 128 named outputs")
		return q
	}
	names := make([]string, 0, len(expressions))
	for name := range expressions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !models.ValidIdentifier(name) || strings.Contains(name, "__") || q.annotationCollision(name) {
			q.err = errors.New("orm: invalid, duplicate or colliding annotation name")
			return q
		}
		spec := expressions[name]
		expression := cloneExpression(spec.Expression)
		output := cloneQueryValue(spec.Output).(models.Field)
		if err := sqlcompiler.ValidateOutputField(output); err != nil {
			q.err = err
			return q
		}
		if err := sqlcompiler.ValidateRowExpression(expression); err != nil {
			q.err = err
			return q
		}
		projection := db.Projection{Alias: name, Expression: expression, Output: &output}
		q.selectAST.Aliases = append(q.selectAST.Aliases, projection)
		q.selectAST.Projections = append(q.selectAST.Projections, projection)
	}
	return q
}

func (q Query[T]) annotationCollision(name string) bool {
	if _, exists := q.schema.Field(name); exists {
		return true
	}
	for _, projection := range q.selectAST.Aliases {
		if projection.Alias == name {
			return true
		}
	}
	for _, path := range q.relatedPaths {
		if strings.SplitN(path, "__", 2)[0] == name {
			return true
		}
	}
	if q.store.Registry != nil {
		for _, schema := range q.store.Registry.All() {
			for _, field := range schema.Fields {
				if field.Relation == nil || field.Relation.Target != q.schema.Key() || field.Relation.RelatedName == "+" {
					continue
				}
				reverse := field.Relation.RelatedName
				if reverse == "" {
					reverse = strings.ToLower(schema.Name) + "_set"
				}
				if reverse == name {
					return true
				}
			}
		}
	}
	return false
}

func cloneProjections(projections []db.Projection) []db.Projection {
	result := append([]db.Projection(nil), projections...)
	for i := range result {
		result[i].Expression = cloneExpression(result[i].Expression)
		if result[i].Output != nil {
			output := cloneQueryValue(*result[i].Output).(models.Field)
			result[i].Output = &output
		}
	}
	return result
}

// prepareAnnotations runs only on the terminal operation's private query copy.
// Literal codecs run once per alias, not once for each filter/order expansion.
func (q Query[T]) prepareAnnotations(ctx context.Context) (Query[T], error) {
	if err := ctx.Err(); err != nil {
		return q, err
	}
	q = q.clone()
	resolved := make(map[string]db.Projection, len(q.selectAST.Aliases))
	for i, projection := range q.selectAST.Aliases {
		_, err := q.store.Backend.Dialect().FieldType(*projection.Output)
		if canceled := annotationCancellation(ctx, err); canceled != nil {
			return q, canceled
		}
		if err != nil {
			return q, err
		}
		if projection.Expression.Kind == "value" {
			value := projection.Expression.Value
			if value != nil && reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil() {
				value = nil
			}
			value, err := encodeField(*projection.Output, value)
			if canceled := annotationCancellation(ctx, err); canceled != nil {
				return q, canceled
			}
			if err != nil {
				return q, fmt.Errorf("orm: annotation %s cannot encode its literal", projection.Alias)
			}
			projection.Expression = Cast(Value(value), *projection.Output)
		}
		q.selectAST.Aliases[i] = projection
		resolved[projection.Alias] = projection
	}
	for i, projection := range q.selectAST.Projections {
		if alias, ok := resolved[projection.Alias]; ok {
			q.selectAST.Projections[i] = alias
		}
	}
	return q, nil
}

func (q Query[T]) checkModelProjection() error {
	for _, name := range q.selectAST.Fields {
		if q.prepared && strings.Contains(name, "__") {
			if _, err := sqlcompiler.SelectOutputField(q.schema, q.selectAST, name); err == nil {
				continue
			}
		}
		field, ok := q.schema.Field(name)
		if !ok || !field.IsStored() {
			return errors.New("orm: Only selects stored model fields; use Values for annotation or path projections")
		}
	}
	return nil
}

func (s *Store) decodeAnnotation(ctx context.Context, projection db.Projection, raw any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if projection.Output == nil {
		return nil, errors.New("orm: annotation requires declared output metadata")
	}
	value, err := s.decodeField(*projection.Output, raw)
	if canceled := annotationCancellation(ctx, err); canceled != nil {
		return nil, canceled
	}
	if err == nil {
		value, err = projection.Output.Clean(ctx, value)
	}
	if canceled := annotationCancellation(ctx, err); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, fmt.Errorf("orm: annotation output %s could not be decoded", projection.Alias)
	}
	return value, nil
}

// Keep cancellation recognizable without exposing a provider's wrapped text.
func annotationCancellation(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
