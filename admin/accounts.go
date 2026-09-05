package admin

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// AccountStoreConfig binds account operations, scoped Admin reads and audit
// writes to the same ORM store. Accounts.Authorize must evaluate the current
// trusted actor and the complete requested change; ordinary user-change or
// staff status must not implicitly grant account-flag authority.
type AccountStoreConfig struct {
	ORM      ORMConfig
	Accounts auth.AccountsConfig
}

// AccountStore adapts the default account models without exposing a generic
// credential/grant write path. User creation and password changes use explicit
// domain methods, never generic hash assignment. Group/permission editors and
// account deletion are not generic record writes.
// Applications using a custom user model provide their own domain-backed Store.
type AccountStore struct {
	base     *ORMStore
	accounts *auth.Accounts
}

func NewAccountStore(config AccountStoreConfig) (*AccountStore, error) {
	if config.ORM.Store == nil || config.Accounts.Store != nil && config.Accounts.Store != config.ORM.Store {
		return nil, errors.New("admin: account and audit operations require the same ORM store")
	}
	if config.Accounts.Authorize == nil {
		return nil, errors.New("admin: explicit account mutation authority required")
	}
	config.Accounts.Store = config.ORM.Store
	accounts, err := auth.NewAccounts(config.Accounts)
	if err != nil {
		return nil, err
	}
	factories := make(map[string]func() models.Model, len(config.ORM.Factories)+1)
	for key, factory := range config.ORM.Factories {
		factories[key] = factory
	}
	factories[(&auth.User{}).Schema().Key()] = func() models.Model { return &auth.User{} }
	config.ORM.Factories = factories
	base, err := NewORMStore(config.ORM)
	if err != nil {
		return nil, err
	}
	return &AccountStore{base: base, accounts: accounts}, nil
}

// Accounts is the same service used for Admin account transitions. It also
// implements the credential and session-principal backend contracts.
func (s *AccountStore) Accounts() *auth.Accounts { return s.accounts }

// UserAdmin returns stock options for default users, including separate
// creation and privileged password forms.
// Identifier and account metadata are readonly; hashes never enter display
// records. Global Site policy must still allow view/change, while the Accounts
// authority independently approves the exact submitted flag effect at save.
func (s *AccountStore) UserAdmin() ModelAdmin {
	return ModelAdmin{
		Schema:    (&auth.User{}).Schema(),
		userForms: true,
		Fieldsets: []Fieldset{
			{Name: "Identity", Fields: []string{"identifier"}},
			{Name: "Account status", Fields: []string{"active", "staff", "superuser"}, Description: "Changing account status requires explicit account-management authority."},
			{Name: "Activity", Fields: []string{"last_login", "created_at", "updated_at", "auth_version"}},
		},
		ReadonlyFields:    []string{"identifier", "last_login", "created_at", "updated_at", "auth_version"},
		SensitiveFields:   []string{"password_hash"},
		ListDisplay:       []string{"identifier", "active", "staff", "superuser"},
		ListDisplayLinks:  []string{"identifier"},
		SearchFields:      []string{"identifier"},
		ListFilter:        []string{"active", "staff", "superuser"},
		Ordering:          []string{"identifier"},
		ConstraintChecker: s.base.config.Store,
		Authorize: func(_ context.Context, _ auth.Principal, action string, _ Object) error {
			if action != "view" && action != "change" && action != "add" {
				return auth.ErrPermissionDenied
			}
			return nil
		},
	}
}

func (s *AccountStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	if accountModel(schema) && !slices.ContainsFunc(auth.Schemas(), func(known models.Schema) bool { return known.Key() == schema.Key() }) {
		// Reset digests, opaque API-token records and future credential models
		// are not generic Admin browsing surfaces, even with a custom factory.
		return nil, auth.ErrPermissionDenied
	}
	store, err := s.base.Scope(ctx, p, site, schema)
	if err != nil {
		return nil, err
	}
	return &accountScoped{ormScoped: store.(*ormScoped), accounts: s.accounts}, nil
}

type accountScoped struct {
	*ormScoped
	accounts *auth.Accounts
}

func accountModel(schema models.Schema) bool {
	// Reserve both credential namespaces, including opt-in password resets and
	// API tokens that are not part of the initial six account schemas.
	return schema.AppLabel == "gogo_auth" || schema.AppLabel == "gogo_authtokens"
}

func redactAccount(object Object) (Object, error) {
	if object.Record == nil || object.Record.Schema().Key() != (&auth.User{}).Schema().Key() {
		return object, nil
	}
	// Do not retain the original typed model behind the projection: exposing a
	// Record with a redacted getter but a credential-bearing Model() is unsafe.
	record, err := models.NewRecord(object.Record.Schema())
	if err != nil {
		return Object{}, err
	}
	for _, field := range record.Schema().Fields {
		var value any
		if field.Name != "password_hash" {
			value, err = object.Record.Get(field.Name)
			if err != nil {
				return Object{}, err
			}
		}
		if err := record.Set(field.Name, value); err != nil {
			return Object{}, err
		}
	}
	record.State().Persisted = object.Record.State().Persisted
	record.State().Database = object.Record.State().Database
	return objectFromRecord(record)
}

func (s *accountScoped) Get(ctx context.Context, key string, lock bool) (Object, error) {
	object, err := s.ormScoped.Get(ctx, key, lock)
	if err != nil {
		return Object{}, err
	}
	return redactAccount(object)
}

func (s *accountScoped) List(ctx context.Context, query ListQuery) (Page, error) {
	page, err := s.ormScoped.List(ctx, query)
	if err != nil {
		return Page{}, err
	}
	for index, object := range page.Objects {
		page.Objects[index], err = redactAccount(object)
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

func (s *accountScoped) ReadRelations(ctx context.Context, object Object, fields []string) (map[string][]Object, error) {
	rows, err := s.ormScoped.ReadRelations(ctx, object, fields)
	if err != nil {
		return nil, err
	}
	for _, objects := range rows {
		for index, object := range objects {
			objects[index], err = redactAccount(object)
			if err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

func (s *accountScoped) New(ctx context.Context) (Object, error) {
	if accountModel(s.schema) {
		return Object{}, auth.ErrPermissionDenied
	}
	return s.ormScoped.New(ctx)
}

func (s *accountScoped) Save(ctx context.Context, object Object) (Object, error) {
	if !accountModel(s.schema) {
		return s.ormScoped.Save(ctx, object)
	}
	if s.schema.Key() != (&auth.User{}).Schema().Key() || object.Record == nil || object.Record.Schema().Key() != s.schema.Key() || !object.Record.State().Persisted || !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) {
		return Object{}, auth.ErrPermissionDenied
	}
	current, err := s.Get(ctx, object.ID, true)
	if err != nil {
		return Object{}, err
	}
	if current.Version != object.Version {
		return Object{}, ErrConflict
	}
	for _, field := range current.Record.Schema().Fields {
		if slices.Contains([]string{"active", "staff", "superuser"}, field.Name) {
			continue
		}
		before, err := current.Record.Get(field.Name)
		if err != nil {
			return Object{}, err
		}
		after, err := object.Record.Get(field.Name)
		if err != nil || !reflect.DeepEqual(before, after) {
			return Object{}, auth.ErrPermissionDenied
		}
	}
	if err := s.owner.config.ValidateWrite(ctx, s.principal, object.Record); err != nil {
		return Object{}, err
	}
	flags := [3]bool{}
	for index, name := range []string{"active", "staff", "superuser"} {
		value, err := object.Record.Get(name)
		if err != nil {
			return Object{}, err
		}
		flag, ok := value.(bool)
		if !ok {
			return Object{}, auth.ErrPermissionDenied
		}
		flags[index] = flag
	}
	id, err := current.Record.Get("id")
	if err != nil {
		return Object{}, err
	}
	// The principal that established this scoped store also establishes the
	// account authority. Do not combine one actor's row scope with a different
	// caller context's privileges; retain any private token ceiling verbatim.
	if err := s.accounts.SetAccountFlags(auth.WithPrincipal(ctx, s.principal), id.(string), flags[0], flags[1], flags[2]); err != nil {
		return Object{}, err
	}
	written, err := s.Get(ctx, object.ID, true)
	if errors.Is(err, ErrNotFound) {
		return Object{}, auth.ErrPermissionDenied
	}
	if err != nil {
		return Object{}, err
	}
	if err := s.owner.config.ValidateWrite(ctx, s.principal, written.Record); err != nil {
		return Object{}, err
	}
	return written, nil
}

func (s *accountScoped) Delete(ctx context.Context, object Object) error {
	if accountModel(s.schema) {
		return auth.ErrPermissionDenied
	}
	return s.DeleteAuthorized(ctx, object, func(context.Context, Deletion) error { return nil })
}

func protectAccountDeletion(graph Deletion) error {
	for _, object := range graph.Objects {
		if object.Record == nil || accountModel(object.Record.Schema()) {
			return auth.ErrPermissionDenied
		}
	}
	for _, update := range graph.Updates {
		if update.Object.Record == nil || accountModel(update.Object.Record.Schema()) {
			return auth.ErrPermissionDenied
		}
	}
	for _, removal := range graph.JoinRemovals {
		if removal.Endpoint.Record == nil || accountModel(removal.Endpoint.Record.Schema()) {
			return auth.ErrPermissionDenied
		}
	}
	return nil
}

func (s *accountScoped) CollectDeletion(ctx context.Context, object Object) (Deletion, error) {
	if accountModel(s.schema) {
		return Deletion{}, auth.ErrPermissionDenied
	}
	graph, err := s.ormScoped.CollectDeletion(ctx, object)
	if err != nil {
		return Deletion{}, err
	}
	if err := protectAccountDeletion(graph); err != nil {
		return Deletion{}, err
	}
	return graph, nil
}

func (s *accountScoped) DeleteAuthorized(ctx context.Context, object Object, authorize func(context.Context, Deletion) error) error {
	if accountModel(s.schema) || authorize == nil {
		return auth.ErrPermissionDenied
	}
	return s.ormScoped.DeleteAuthorized(ctx, object, func(ctx context.Context, graph Deletion) error {
		if err := protectAccountDeletion(graph); err != nil {
			return err
		}
		return authorize(ctx, graph)
	})
}
