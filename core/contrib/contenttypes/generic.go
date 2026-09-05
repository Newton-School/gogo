package contenttypes

import (
	"context"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// Reference uses named key components, including composite keys. It is data,
// never authority to query an arbitrary registered model.
type Reference struct {
	ContentTypeID int64          `json:"content_type_id"`
	Key           map[string]any `json:"key"`
}

type Target struct {
	Factory func() models.Model
	// Scope is required and runs before target lookup. It must include the
	// current requester and tenant boundary; return orm.ErrNotFound to hide a
	// denied reference. An empty predicate intentionally allows all target rows.
	Scope func(context.Context, models.Schema) (db.Predicate, error)
}

type Resolver struct {
	store   *orm.Store
	targets map[string]Target
}

func NewResolver(store *orm.Store, targets []Target) (*Resolver, error) {
	if store == nil || store.Backend == nil || len(targets) > 1000 {
		return nil, ErrConfiguration
	}
	r := &Resolver{store: store, targets: map[string]Target{}}
	for _, target := range targets {
		if target.Factory == nil || target.Scope == nil {
			return nil, ErrConfiguration
		}
		record, err := models.Bind(target.Factory())
		if err != nil {
			return nil, ErrConfiguration
		}
		schema := record.Schema()
		key := identity(schema)
		if _, duplicate := r.targets[key]; duplicate || schema.Abstract {
			return nil, ErrConfiguration
		}
		fingerprint, err := schema.Fingerprint()
		if err != nil {
			return nil, ErrConfiguration
		}
		// Query construction and decoding each call the factory. Enforce the
		// same frozen schema on every invocation, not only the first lookup.
		factory := target.Factory
		target.Factory = func() models.Model {
			model := factory()
			current, err := models.Bind(model)
			if err != nil {
				return nil
			}
			actual, err := current.Schema().Fingerprint()
			if err != nil || actual != fingerprint {
				return nil
			}
			return model
		}
		r.targets[key] = target
	}
	return r, nil
}

func (r *Resolver) Resolve(ctx context.Context, reference Reference) (models.Model, error) {
	if r == nil || reference.ContentTypeID < 1 || len(reference.Key) < 1 || len(reference.Key) > 32 {
		return nil, orm.ErrNotFound
	}
	contentType, err := orm.For(r.store, func() *ContentType { return &ContentType{} }).Filter(orm.Q("id", reference.ContentTypeID), orm.Q("active", true)).Get(ctx)
	if err != nil {
		return nil, err
	}
	target, allowed := r.targets[contentType.AppLabel+"."+contentType.ModelName]
	if !allowed {
		return nil, orm.ErrNotFound
	}
	record, err := models.Bind(target.Factory())
	if err != nil {
		return nil, ErrConfiguration
	}
	schema := record.Schema()
	if identity(schema) != contentType.AppLabel+"."+contentType.ModelName {
		return nil, ErrConfiguration
	}
	keys := schema.PKFields()
	if len(keys) != len(reference.Key) {
		return nil, orm.ErrNotFound
	}
	var predicates []db.Predicate
	for _, field := range keys {
		value, present := reference.Key[field.Name]
		if !present || value == nil {
			return nil, orm.ErrNotFound
		}
		value, err = field.Clean(ctx, value)
		if err != nil {
			return nil, orm.ErrNotFound
		}
		predicates = append(predicates, orm.Q(field.Name, value))
	}
	scope, err := target.Scope(ctx, schema)
	if err != nil {
		return nil, err
	}
	return orm.For(r.store, target.Factory).Filter(scope, orm.And(predicates...)).Get(ctx)
}
