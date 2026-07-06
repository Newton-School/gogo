package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/models"
)

// CheckSite validates registered ModelAdmin configuration.
func CheckSite(site *Site) []checks.Result {
	site = adminSiteOrDefault(site)
	if site.ModelRegistry == nil {
		return nil
	}
	var results []checks.Result
	for _, label := range site.ModelRegistry.RegisteredModels() {
		modelAdmin, ok := site.ModelRegistry.GetAdmin(label)
		if !ok {
			continue
		}
		meta := modelAdmin.Model
		if err := modelAdmin.Validate(meta); err != nil {
			results = append(results, adminCheckResult("admin.E001", err.Error(), "Fix the invalid ModelAdmin option combination.", meta.Label()))
		}
		results = append(results, checkAdminReferencedFields(meta, modelAdmin)...)
		results = append(results, checkAdminUnsupportedWidgets(meta, modelAdmin)...)
		results = append(results, checkAdminStoreCapabilities(site, meta)...)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ID == results[j].ID {
			return results[i].Object < results[j].Object
		}
		return results[i].ID < results[j].ID
	})
	return results
}

func checkAdminUnsupportedWidgets(meta models.Metadata, modelAdmin ModelAdmin) []checks.Result {
	fields := adminFieldMetaMap(meta)
	var results []checks.Result
	for _, fieldName := range modelAdmin.RawIDFields {
		field, ok := fields[fieldName]
		if ok && strings.TrimSpace(field.RelationTarget) == "" {
			results = append(results, adminCheckResult("admin.E003", fmt.Sprintf("raw_id_fields references non-relation field %s", fieldName), "Use raw_id_fields only with relationship metadata.", meta.Label()+".raw_id_fields"))
		}
	}
	for _, fieldName := range modelAdmin.AutocompleteFields {
		field, ok := fields[fieldName]
		if ok && strings.TrimSpace(field.RelationTarget) == "" {
			results = append(results, adminCheckResult("admin.E003", fmt.Sprintf("autocomplete_fields references non-relation field %s", fieldName), "Use autocomplete_fields only with relationship metadata.", meta.Label()+".autocomplete_fields"))
		}
	}
	for _, fieldName := range append(append([]string(nil), modelAdmin.FilterHorizontal...), modelAdmin.FilterVertical...) {
		field, ok := fields[fieldName]
		if !ok || field.RelationType == "" {
			continue
		}
		if !strings.EqualFold(field.RelationType, "many_to_many") {
			results = append(results, adminCheckResult("admin.E003", fmt.Sprintf("filter_horizontal/filter_vertical references non-many-to-many field %s", fieldName), "Use filtered select widgets only with many-to-many relationship metadata.", meta.Label()+".filter_horizontal"))
		}
	}
	for fieldName := range modelAdmin.RadioFields {
		field, ok := fields[fieldName]
		if ok && len(field.Choices) == 0 && strings.TrimSpace(field.RelationTarget) == "" {
			results = append(results, adminCheckResult("admin.E003", fmt.Sprintf("radio_fields references field %s without choices or relation metadata", fieldName), "Use radio_fields only with choice or relationship fields.", meta.Label()+".radio_fields"))
		}
	}
	return results
}

func checkAdminStoreCapabilities(site *Site, meta models.Metadata) []checks.Result {
	var results []checks.Result
	if site.ModelStore != nil && hasAdminManyToManyFields(meta) {
		if _, ok := site.ModelStore.(ManyToManyStore); !ok {
			results = append(results, adminCheckResult("admin.E004", fmt.Sprintf("%s has many-to-many fields but the admin model store cannot persist them", meta.Label()), "Implement admin.ManyToManyStore on the configured admin model store.", meta.Label()+".model_store"))
		}
	}
	if site.ModelStore != nil && hasAdminFileFields(meta) && site.FileStorage == nil {
		results = append(results, adminCheckResult("admin.E004", fmt.Sprintf("%s has file fields but the admin site has no file storage", meta.Label()), "Configure admin.SiteOptions.FileStorage with a files.Storage implementation.", meta.Label()+".file_storage"))
	}
	return results
}

func hasAdminManyToManyFields(meta models.Metadata) bool {
	for _, field := range meta.Fields {
		if adminIsManyToManyField(field) {
			return true
		}
	}
	return false
}

func hasAdminFileFields(meta models.Metadata) bool {
	for _, field := range meta.Fields {
		if adminIsFileField(field) {
			return true
		}
	}
	return false
}

// RegisterChecks registers this site's admin checks into a checks registry.
func RegisterChecks(registry *checks.Registry, site *Site) {
	if registry == nil {
		return
	}
	for _, result := range CheckSite(site) {
		result := result
		registry.Register(checks.Check{
			ID:       result.ID,
			Tags:     result.Tags,
			Severity: result.Severity,
			Message:  result.Message,
			Hint:     result.Hint,
			Object:   result.Object,
			Run: func(context.Context) checks.Result {
				return result
			},
		})
	}
}

type adminFieldReference struct {
	Option string
	Field  string
}

func checkAdminReferencedFields(meta models.Metadata, modelAdmin ModelAdmin) []checks.Result {
	fields := adminMetadataFieldSet(meta)
	var results []checks.Result
	seen := map[string]struct{}{}
	for _, reference := range adminReferencedFields(modelAdmin) {
		field := adminLookupRoot(reference.Field)
		if field == "" || field == "__str__" {
			continue
		}
		if _, computed := modelAdmin.ComputedColumns[field]; computed {
			continue
		}
		if _, ok := fields[field]; ok {
			continue
		}
		key := reference.Option + "." + field
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		results = append(results, adminCheckResult(
			"admin.E002",
			fmt.Sprintf("%s references unknown field %s", reference.Option, field),
			"Use a field from the model metadata or register a computed admin column.",
			meta.Label()+"."+reference.Option,
		))
	}
	return results
}

func adminReferencedFields(modelAdmin ModelAdmin) []adminFieldReference {
	var refs []adminFieldReference
	addRefs := func(option string, values []string) {
		for _, value := range values {
			refs = append(refs, adminFieldReference{Option: option, Field: value})
		}
	}
	addRefs("fields", modelAdmin.Fields)
	addRefs("exclude", modelAdmin.Exclude)
	addRefs("readonly_fields", modelAdmin.ReadonlyFields)
	addRefs("list_display", modelAdmin.ListDisplay)
	addRefs("list_display_links", modelAdmin.ListDisplayLinks)
	addRefs("list_editable", modelAdmin.ListEditable)
	addRefs("list_filter", modelAdmin.ListFilter)
	addRefs("raw_id_fields", modelAdmin.RawIDFields)
	addRefs("autocomplete_fields", modelAdmin.AutocompleteFields)
	addRefs("filter_horizontal", modelAdmin.FilterHorizontal)
	addRefs("filter_vertical", modelAdmin.FilterVertical)
	addRefs("sortable_by", modelAdmin.SortableBy)
	addRefs("search_fields", modelAdmin.SearchFields)
	addRefs("ordering", modelAdmin.Ordering)
	if modelAdmin.DateHierarchy != "" {
		refs = append(refs, adminFieldReference{Option: "date_hierarchy", Field: modelAdmin.DateHierarchy})
	}
	for _, fieldset := range modelAdmin.Fieldsets {
		addRefs("fieldsets", fieldset.Fields)
	}
	for field, display := range modelAdmin.RadioFields {
		refs = append(refs, adminFieldReference{Option: "radio_fields", Field: field})
		if strings.TrimSpace(display) == "" {
			refs = append(refs, adminFieldReference{Option: "radio_fields", Field: field})
		}
	}
	for target, sources := range modelAdmin.PrepopulatedFields {
		refs = append(refs, adminFieldReference{Option: "prepopulated_fields", Field: target})
		addRefs("prepopulated_fields", sources)
	}
	return refs
}

func adminMetadataFieldSet(meta models.Metadata) map[string]struct{} {
	fields := make(map[string]struct{}, len(meta.Fields))
	for _, field := range meta.Fields {
		if field.Name != "" {
			fields[field.Name] = struct{}{}
		}
	}
	return fields
}

func adminLookupRoot(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "-")
	if value == "" {
		return ""
	}
	switch value[0] {
	case '=', '^', '@':
		value = value[1:]
	}
	if index := strings.Index(value, "__"); index >= 0 {
		value = value[:index]
	}
	return value
}

func adminCheckResult(id, message, hint, object string) checks.Result {
	return checks.Result{
		ID:       id,
		Tags:     []string{"admin"},
		Severity: checks.SeverityError,
		Message:  message,
		Hint:     hint,
		Object:   object,
	}
}
