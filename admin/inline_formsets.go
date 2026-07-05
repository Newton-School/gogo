package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/forms"
	"github.com/cybersaksham/gogo/models"
)

// AdminInlineFormsetInput configures route-level inline formset building.
type AdminInlineFormsetInput struct {
	ParentID  string
	User      auth.User
	Request   *http.Request
	Values    map[string]any
	Submitted bool
}

// BuildAdminInlineFormsets resolves registered inline metadata and builds formsets
// from either posted management-form values or stored child rows.
func BuildAdminInlineFormsets(ctx context.Context, site *Site, parentAdmin ModelAdmin, input AdminInlineFormsetInput) ([]InlineFormset, error) {
	site = adminSiteOrDefault(site)
	if site.ModelRegistry == nil {
		return nil, nil
	}
	request := input.Request
	if request == nil {
		request, _ = http.NewRequest(http.MethodGet, "/", nil)
	}
	inlines := adminInlinesForRequest(parentAdmin, request)
	formsets := make([]InlineFormset, 0, len(inlines))
	for _, inline := range inlines {
		inlineAdmin, ok := site.ModelRegistry.GetAdmin(inline.Model)
		if !ok {
			return nil, fmt.Errorf("%w: inline model %s", ErrNotRegistered, inline.Model)
		}
		if inline.HasPermission != nil && !inline.HasPermission(request, input.User) {
			continue
		}
		canAdd := inlineAdmin.HasAddPermission(request, input.User)
		canChange := inlineAdmin.HasChangePermission(request, input.User)
		canDelete := inline.CanDelete && inlineAdmin.HasDeletePermission(request, input.User)
		canView := inlineAdmin.HasViewPermission(request, input.User)
		if !canAdd && !canChange && !canDelete && !canView {
			continue
		}
		fkName, err := resolveInlineFKName(parentAdmin.Model, inlineAdmin.Model, inline)
		if err != nil {
			return nil, err
		}
		prefix := inlinePrefix(inline, inlineAdmin.Model)
		fields := inlineEditableFields(inlineAdmin.Model, fkName)
		kind := inline.Kind
		if kind == "" {
			kind = InlineStacked
		}
		formset := InlineFormset{
			Model:          inline.Model,
			Meta:           inlineAdmin.Model,
			Prefix:         prefix,
			Kind:           kind,
			ExtraForms:     inline.Extra,
			MinNum:         inline.MinNum,
			MaxNum:         inline.MaxNum,
			CanDelete:      canDelete,
			CanAdd:         canAdd,
			CanChange:      canChange,
			CanView:        canView,
			ShowChangeLink: inline.ShowChangeLink,
			FKName:         fkName,
			ParentID:       input.ParentID,
			Fields:         fields,
			Submitted:      input.Submitted,
		}
		if input.Submitted {
			formset.Forms = parseInlinePostedForms(formset, input.Values)
			formset.InitialForms = inlinePostedInt(input.Values, prefix+"-INITIAL_FORMS", 0)
		} else {
			rows, err := loadInlineRows(ctx, site, inlineAdmin.Model, fkName, input.ParentID)
			if err != nil {
				return nil, err
			}
			formset.InitialForms = len(rows)
			for index, row := range rows {
				formset.Forms = append(formset.Forms, InlineForm{Index: index, Values: cloneRow(row)})
			}
		}
		formsets = append(formsets, formset)
	}
	return formsets, nil
}

func adminInlinesForRequest(modelAdmin ModelAdmin, request *http.Request) []Inline {
	if modelAdmin.Hooks.GetInlineInstances != nil {
		return append([]Inline(nil), modelAdmin.Hooks.GetInlineInstances(request)...)
	}
	if modelAdmin.Hooks.GetInlines != nil {
		return append([]Inline(nil), modelAdmin.Hooks.GetInlines(request)...)
	}
	return append([]Inline(nil), modelAdmin.Inlines...)
}

func resolveInlineFKName(parentMeta, inlineMeta models.Metadata, inline Inline) (string, error) {
	if strings.TrimSpace(inline.FKName) != "" {
		return inline.FKName, nil
	}
	parentLabel := parentMeta.Label()
	for _, field := range inlineMeta.Fields {
		if strings.EqualFold(field.RelationTarget, parentLabel) {
			return field.Name, nil
		}
	}
	return "", fmt.Errorf("%w: inline %s has no relation to %s", ErrInvalidInlineFormset, inlineMeta.Label(), parentLabel)
}

func inlinePrefix(inline Inline, meta models.Metadata) string {
	if inline.FKName != "" {
		return strings.ToLower(meta.ModelName) + "_set"
	}
	if meta.ModelName == "" {
		return strings.NewReplacer(".", "_", "-", "_").Replace(strings.ToLower(inline.Model))
	}
	return strings.ToLower(meta.ModelName) + "_set"
}

func inlineEditableFields(meta models.Metadata, fkName string) []string {
	fields := make([]string, 0, len(meta.Fields))
	for _, field := range meta.Fields {
		if field.Name == "" || field.PrimaryKey || field.Name == fkName {
			continue
		}
		if field.Editable != nil && !*field.Editable {
			continue
		}
		fields = append(fields, field.Name)
	}
	return fields
}

func loadInlineRows(ctx context.Context, site *Site, meta models.Metadata, fkName, parentID string) ([]map[string]any, error) {
	if parentID == "" || site.ModelStore == nil {
		return nil, nil
	}
	filterKey := fkName + "__exact"
	if queryStore, ok := site.ModelStore.(models.ObjectQueryStore); ok {
		result, err := queryStore.Query(ctx, meta, models.ObjectQuery{
			Filters: map[string][]string{filterKey: {parentID}},
		})
		if err != nil {
			return nil, err
		}
		return cloneRows(result.Rows), nil
	}
	rows, err := site.ModelStore.List(ctx, meta)
	if err != nil {
		return nil, err
	}
	filtered := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if fmt.Sprint(row[fkName]) == parentID {
			filtered = append(filtered, cloneRow(row))
		}
	}
	return filtered, nil
}

func parseInlinePostedForms(formset InlineFormset, values map[string]any) []InlineForm {
	total := inlinePostedInt(values, formset.Prefix+"-TOTAL_FORMS", 0)
	initial := inlinePostedInt(values, formset.Prefix+"-INITIAL_FORMS", 0)
	forms := make([]InlineForm, 0, total)
	pkName := primaryKeyName(formset.Meta)
	for index := 0; index < total; index++ {
		row := map[string]any{}
		if pkName != "" {
			if value, ok := values[inlineFieldKey(formset.Prefix, index, pkName)]; ok {
				row[pkName] = firstFormValue(value)
			}
		}
		for _, field := range formset.Fields {
			if value, ok := values[inlineFieldKey(formset.Prefix, index, field)]; ok {
				row[field] = value
			}
		}
		deleteValue := firstFormValue(values[inlineFieldKey(formset.Prefix, index, "DELETE")])
		deleting := isTruthy(deleteValue)
		if index >= initial && !deleting && inlineRowEmpty(row, pkName) {
			continue
		}
		forms = append(forms, InlineForm{Index: index, Values: row, Delete: deleting})
	}
	return forms
}

func ValidateAdminInlineFormsets(ctx context.Context, request *http.Request, formsets []InlineFormset) ([]InlineFormset, error) {
	validated := make([]InlineFormset, len(formsets))
	var hasErrors bool
	for index, formset := range formsets {
		validated[index] = formset
		if err := ValidateInlineFormset(formset); err != nil {
			validated[index].Errors = append(validated[index].Errors, err.Error())
			hasErrors = true
			continue
		}
		for formIndex, form := range formset.Forms {
			if form.Delete {
				continue
			}
			if !formset.CanChange && hasInlinePrimaryKey(formset.Meta, form.Values) {
				validated[index].Forms[formIndex].Errors = map[string]string{"__all__": "You do not have permission to change this inline object."}
				hasErrors = true
				continue
			}
			if !formset.CanAdd && !hasInlinePrimaryKey(formset.Meta, form.Values) {
				validated[index].Forms[formIndex].Errors = map[string]string{"__all__": "You do not have permission to add this inline object."}
				hasErrors = true
				continue
			}
			cleaned, errors := validateInlineFormRow(ctx, request, formset, form)
			if len(errors) > 0 {
				validated[index].Forms[formIndex].Errors = errors
				hasErrors = true
				continue
			}
			validated[index].Forms[formIndex].Values = cleaned
		}
	}
	if hasErrors {
		return validated, ErrInvalidInlineFormset
	}
	return validated, nil
}

func validateInlineFormRow(ctx context.Context, _ *http.Request, formset InlineFormset, form InlineForm) (map[string]any, map[string]string) {
	data := cloneRow(form.Values)
	include := append([]string(nil), formset.Fields...)
	row := &adminRowModel{meta: formset.Meta, values: cloneRow(data)}
	modelForm := forms.NewModelForm(forms.ModelFormOptions{
		Model:   row,
		Meta:    formset.Meta,
		Include: include,
		Data:    data,
		Initial: data,
		Context: ctx,
	})
	if modelForm.IsValid() {
		cleaned := cloneRow(form.Values)
		for _, name := range modelForm.FieldNames() {
			if value, ok := modelForm.CleanedData[name]; ok {
				cleaned[name] = value
			}
		}
		return cleaned, nil
	}
	errors := map[string]string{}
	for field, fieldErrors := range modelForm.Errors() {
		if len(fieldErrors) > 0 {
			errors[field] = strings.Join(fieldErrors.Messages(), "; ")
		}
	}
	if nonField := modelForm.NonFieldErrors(); len(nonField) > 0 {
		errors["__all__"] = strings.Join(nonField.Messages(), "; ")
	}
	return nil, errors
}

func SaveAdminInlineFormsets(ctx context.Context, site *Site, request *http.Request, modelAdmin ModelAdmin, parentID string, formsets []InlineFormset) error {
	if len(formsets) == 0 {
		return nil
	}
	if site.ModelStore == nil {
		return fmt.Errorf("admin model store is required for inline formsets")
	}
	for _, formset := range formsets {
		formset.ParentID = parentID
		if modelAdmin.Hooks.SaveFormset != nil {
			if err := modelAdmin.Hooks.SaveFormset(request, formset); err != nil {
				return err
			}
			continue
		}
		for _, form := range formset.Forms {
			values := cloneRow(form.Values)
			if form.Delete {
				if !formset.CanDelete {
					return fmt.Errorf("%w: %s cannot delete", ErrInvalidInlineFormset, formset.Model)
				}
				pk := inlinePrimaryKeyValue(formset.Meta, values)
				if pk == "" {
					continue
				}
				if err := site.ModelStore.Delete(ctx, formset.Meta, pk); err != nil {
					return err
				}
				continue
			}
			values[formset.FKName] = parentID
			pk := inlinePrimaryKeyValue(formset.Meta, values)
			if pk != "" {
				if !formset.CanChange {
					return fmt.Errorf("%w: %s cannot change", ErrInvalidInlineFormset, formset.Model)
				}
				if _, err := site.ModelStore.Update(ctx, formset.Meta, pk, values, true); err != nil {
					return err
				}
				continue
			}
			if !formset.CanAdd {
				return fmt.Errorf("%w: %s cannot add", ErrInvalidInlineFormset, formset.Model)
			}
			if _, err := site.ModelStore.Create(ctx, formset.Meta, values); err != nil {
				return err
			}
		}
	}
	return nil
}

func inlineFieldKey(prefix string, index int, field string) string {
	return prefix + "-" + strconv.Itoa(index) + "-" + field
}

func inlinePostedInt(values map[string]any, key string, fallback int) int {
	raw := firstFormValue(values[key])
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func firstFormValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []string:
		if len(typed) > 0 {
			return typed[0]
		}
	case []any:
		if len(typed) > 0 {
			return fmt.Sprint(typed[0])
		}
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
	return ""
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func inlineRowEmpty(row map[string]any, pkName string) bool {
	for key, value := range row {
		if key == pkName {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(value)) != "" {
			return false
		}
	}
	return true
}

func primaryKeyName(meta models.Metadata) string {
	for _, field := range meta.Fields {
		if field.PrimaryKey {
			return field.Name
		}
	}
	return "id"
}

func hasInlinePrimaryKey(meta models.Metadata, values map[string]any) bool {
	return inlinePrimaryKeyValue(meta, values) != ""
}

func inlinePrimaryKeyValue(meta models.Metadata, values map[string]any) string {
	key := primaryKeyName(meta)
	if key == "" {
		return ""
	}
	value := values[key]
	if strings.TrimSpace(fmt.Sprint(value)) == "" || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
