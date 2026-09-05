package contenttypes

import (
	"context"
	"errors"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

var ErrConfiguration = errors.New("contenttypes: invalid configuration")

// Sync installs current identities without deleting, renumbering or silently
// deactivating stale types. Versions are explicit per model key (default 1),
// not hashes of mutable Go implementations. A version cannot decrease.
func Sync(ctx context.Context, store *orm.Store, registry *models.Registry, versions map[string]int64) ([]*ContentType, error) {
	if store == nil || store.Backend == nil || registry == nil {
		return nil, ErrConfiguration
	}
	schemas := registry.All()
	known, identities := map[string]bool{}, map[string]bool{}
	for _, schema := range schemas {
		if schema.Abstract || schema.AutoCreatedBy != "" {
			continue
		}
		key := identity(schema)
		if identities[key] || len(schema.AppLabel) > 128 || len(schema.Name) > 128 {
			return nil, ErrConfiguration
		}
		identities[key], known[schema.Key()] = true, true
	}
	for key, version := range versions {
		if !known[key] || version < 1 {
			return nil, ErrConfiguration
		}
	}
	var result []*ContentType
	err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		for _, schema := range schemas {
			if !known[schema.Key()] {
				continue
			}
			version := versions[schema.Key()]
			if version == 0 {
				version = 1
			}
			key := orm.UniqueKey{Constraint: IdentityConstraint, Values: map[string]any{"app_label": schema.AppLabel, "model_name": strings.ToLower(schema.Name)}}
			item, _, err := orm.For(store, func() *ContentType { return &ContentType{} }).GetOrCreate(ctx, key, orm.Defaults{"schema_version": version, "active": true})
			if err != nil {
				return err
			}
			// Re-lock even a race winner so concurrent synchronization cannot
			// overwrite a newer schema version with an older one.
			item, err = orm.For(store, func() *ContentType { return &ContentType{} }).Filter(orm.Q("id", item.ID)).SelectForUpdate(false, false).Get(ctx)
			if err != nil {
				return err
			}
			if version < item.SchemaVersion {
				return errors.New("contenttypes: schema version cannot decrease")
			}
			if version != item.SchemaVersion || !item.Active {
				item.SchemaVersion, item.Active = version, true
				if err := store.Save(ctx, item, orm.SaveOptions{UpdateFields: []string{"schema_version", "active"}}); err != nil {
					return err
				}
			}
			result = append(result, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ForModel(ctx context.Context, store *orm.Store, schema models.Schema) (*ContentType, error) {
	return ByNaturalKey(ctx, store, schema.AppLabel, strings.ToLower(schema.Name))
}

func ByNaturalKey(ctx context.Context, store *orm.Store, appLabel, modelName string) (*ContentType, error) {
	if !models.ValidIdentifier(appLabel) || !models.ValidIdentifier(modelName) || strings.ToLower(modelName) != modelName {
		return nil, orm.ErrNotFound
	}
	return orm.For(store, func() *ContentType { return &ContentType{} }).Filter(orm.Q("app_label", appLabel), orm.Q("model_name", modelName), orm.Q("active", true)).Get(ctx)
}

// Stale reports identities no longer installed. It never deletes or deactivates
// them: permissions and generic references may still point to their stable IDs.
// This is a privileged maintenance API, not a public model-discovery endpoint.
func Stale(ctx context.Context, store *orm.Store, registry *models.Registry) ([]*ContentType, error) {
	if registry == nil {
		return nil, ErrConfiguration
	}
	installed := map[string]bool{}
	for _, schema := range registry.All() {
		if !schema.Abstract && schema.AutoCreatedBy == "" {
			installed[identity(schema)] = true
		}
	}
	all, err := orm.For(store, func() *ContentType { return &ContentType{} }).Limit(10001).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) > 10000 {
		return nil, errors.New("contenttypes: maintenance scan limit exceeded")
	}
	var stale []*ContentType
	for _, item := range all {
		if !installed[item.AppLabel+"."+item.ModelName] {
			stale = append(stale, item)
		}
	}
	return stale, nil
}
