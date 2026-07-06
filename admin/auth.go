package admin

import (
	"fmt"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

// RegisterAuthModels registers the built-in auth models with Django-style admin
// defaults for generated projects.
func RegisterAuthModels(registry *Registry) error {
	if registry == nil {
		return fmt.Errorf("admin registry is required")
	}
	metadata := authMetadataByLabel()
	for _, registration := range auth.AdminRegistrations() {
		meta, ok := metadata[registration.Model]
		if !ok {
			continue
		}
		if err := registry.RegisterMetadata(meta, ModelAdmin{
			ListDisplay:      registration.ListDisplay,
			ListFilter:       registration.ListFilter,
			SearchFields:     registration.SearchFields,
			Fieldsets:        authFieldsets(registration.Fieldsets),
			ReadonlyFields:   registration.ReadOnlyFields,
			FilterHorizontal: registration.FilterHorizontal,
			Actions:          registration.Actions,
			Ordering:         registration.Ordering,
		}); err != nil {
			return err
		}
	}
	return nil
}

func authMetadataByLabel() map[string]models.Metadata {
	metadata := make(map[string]models.Metadata)
	for _, meta := range auth.ModelMetadata() {
		metadata[meta.Label()] = meta
	}
	return metadata
}

func authFieldsets(values []auth.AdminFieldset) []Fieldset {
	fieldsets := make([]Fieldset, len(values))
	for i, value := range values {
		fieldsets[i] = Fieldset{Name: value.Name, Fields: append([]string(nil), value.Fields...)}
	}
	return fieldsets
}
