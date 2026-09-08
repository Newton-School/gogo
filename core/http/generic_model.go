package http

import (
	"context"
	"errors"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// ErrInvalidLookup is an explicit malformed-key result from a generic detail
// decoder. It is not a provider error and must never wrap an operational error.
var ErrInvalidLookup = errors.New("http: invalid model lookup")

// ModelReadOptions declares the data and authorization boundary for model-backed
// HTML views. Authentication is supplied by surrounding trusted middleware.
type ModelReadOptions struct {
	Store          *orm.Store
	Model          string
	Policy         auth.Policy
	AllowAnonymous bool
	// Scope must encode all listable/countable row visibility before selection
	// or navigation. It applies independently to any related schema needed by
	// a scope expression; there is no unscoped fallback.
	Scope func(context.Context, auth.Principal, models.Schema) (db.Predicate, error)
	// Fields is a required local stored-field output allowlist. PolicyFields
	// adds data needed by object policies, without disclosing it to templates.
	// Primary keys are loaded for identity, not automatically exposed.
	Fields       []string
	PolicyFields []string
	// AllowField can only narrow Fields. All policies must be read-only;
	// independent external ACL changes require application-owned coordination.
	AllowField func(context.Context, auth.Principal, models.Record, string) (bool, error)
}

type genericModel struct {
	store          *orm.Store
	schema         models.Schema
	policy         auth.Policy
	scope          func(context.Context, auth.Principal, models.Schema) (db.Predicate, error)
	allowAnonymous bool
	fields         []string
	selected       []string
	allowField     func(context.Context, auth.Principal, models.Record, string) (bool, error)
}

func newGenericModel(options ModelReadOptions) (*genericModel, error) {
	if options.Store == nil || genericNil(options.Store.Backend) || options.Store.Registry == nil || genericNil(options.Policy) || options.Scope == nil || len(options.Fields) == 0 || len(options.Fields) > 64 || len(options.PolicyFields) > 64 {
		return nil, ErrGenericConfiguration
	}
	store := *options.Store
	schema, ok := store.Registry.Get(options.Model)
	if !ok || schema.Key() != options.Model || len(schema.Fields) > 1024 || len(schema.PKFields()) == 0 || len(schema.PKFields()) > 64 {
		return nil, ErrGenericConfiguration
	}
	selected := []string{}
	seen := map[string]bool{}
	for _, names := range [][]string{options.Fields, options.PolicyFields} {
		local := map[string]bool{}
		for _, name := range names {
			field, ok := schema.Field(name)
			if !ok || !models.ValidIdentifier(name) || local[name] || !field.IsStored() || !genericModelFieldSupported(field, false) {
				return nil, ErrGenericConfiguration
			}
			local[name] = true
			if !seen[name] {
				selected = append(selected, name)
				seen[name] = true
			}
		}
	}
	for _, field := range schema.PKFields() {
		if !genericModelFieldSupported(field, true) {
			return nil, ErrGenericConfiguration
		}
		if !seen[field.Name] {
			selected = append(selected, field.Name)
			seen[field.Name] = true
		}
	}
	return &genericModel{
		store: &store, schema: schema, policy: auth.ConstrainPolicy(options.Policy), scope: options.Scope,
		allowAnonymous: options.AllowAnonymous, fields: append([]string(nil), options.Fields...), selected: selected, allowField: options.AllowField,
	}, nil
}

func (m *genericModel) authorize(ctx context.Context) error {
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	principal := auth.FromContext(ctx)
	if !principal.Authenticated && !m.allowAnonymous {
		return auth.ErrUnauthenticated
	}
	if principal.Authenticated && (!principal.Active || principal.ID == "") {
		return auth.ErrPermissionDenied
	}
	return m.policy.Authorize(ctx, principal, "view", auth.Resource{App: m.schema.AppLabel, Model: m.schema.Name})
}

func (m *genericModel) query(ctx context.Context) (orm.Query[*models.MapRecord], error) {
	var zero orm.Query[*models.MapRecord]
	if !genericContextOK(ctx) {
		return zero, ErrUnavailable
	}
	predicate, err := m.scope(ctx, auth.FromContext(ctx), m.schema.Clone())
	if err != nil {
		return zero, ErrUnavailable
	}
	factory := func() *models.MapRecord { record, _ := models.NewRecord(m.schema); return record }
	// Filter freezes builtin predicate data before later key/pagination hooks.
	// Scope cannot replace this query and thereby remove the root predicate.
	query := orm.For(m.store, factory).Only(m.selected...).Filter(predicate)
	if !genericContextOK(ctx) {
		return zero, ErrUnavailable
	}
	query = query.WithScope(func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		if !genericContextOK(ctx) {
			return db.Predicate{}, ErrUnavailable
		}
		if schema.Key() == m.schema.Key() {
			return db.Predicate{}, nil // Already frozen into the root query.
		}
		predicate, err := m.scope(ctx, auth.FromContext(ctx), schema.Clone())
		if err != nil {
			return db.Predicate{}, ErrUnavailable
		}
		// Return directly to the ORM's predicate-copy boundary. Its execution
		// checks cancellation before SQL; do not insert another callback while
		// the just-returned predicate still belongs to the scope provider.
		return predicate, nil
	})
	return query, nil
}
