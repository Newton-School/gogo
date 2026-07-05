package admin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/files"
	"github.com/cybersaksham/gogo/forms"
	gogohttp "github.com/cybersaksham/gogo/http"
	"github.com/cybersaksham/gogo/messages"
	"github.com/cybersaksham/gogo/models"
)

// AdminFormProcessor runs the Django-style add/change admin POST lifecycle.
type AdminFormProcessor struct {
	Site       *Site
	ModelAdmin ModelAdmin
	Mode       ChangeFormMode
}

// AdminFormProcessInput configures one add/change POST lifecycle run.
type AdminFormProcessInput struct {
	Request  *http.Request
	User     auth.User
	ObjectID string
	Existing map[string]any
	Values   map[string]any
}

// AdminFormValidationError carries bound formsets back to route rendering.
type AdminFormValidationError struct {
	Err            error
	InlineFormsets []InlineFormset
	Values         map[string]any
}

func (e AdminFormValidationError) Error() string {
	if e.Err == nil {
		return "admin form validation failed"
	}
	return e.Err.Error()
}

func (e AdminFormValidationError) Unwrap() error {
	return e.Err
}

type adminAtomicStore interface {
	Atomic(context.Context, func(context.Context) error) error
}

// ManyToManyStore persists metadata-backed many-to-many selections.
type ManyToManyStore interface {
	SetManyToMany(context.Context, models.Metadata, string, string, []string) error
}

// Process validates, saves, logs, messages, and returns the final admin response.
func (p AdminFormProcessor) Process(ctx context.Context, input AdminFormProcessInput) (gogohttp.Response, error) {
	site := adminSiteOrDefault(p.Site)
	if site.ModelStore == nil {
		return gogohttp.Response{}, fmt.Errorf("admin model store is required")
	}
	request := input.Request
	if request == nil {
		request, _ = http.NewRequest(http.MethodPost, adminModelURL(site, p.ModelAdmin), nil)
	}
	mode := p.Mode
	if mode == "" {
		mode = ChangeFormAdd
	}
	if mode == ChangeFormAdd && !p.ModelAdmin.HasAddPermission(request, input.User) {
		return gogohttp.Response{}, ErrAdminPermissionDenied
	}
	if mode == ChangeFormEdit && !p.ModelAdmin.HasChangePermission(request, input.User) {
		return gogohttp.Response{}, ErrAdminPermissionDenied
	}
	values := input.Values
	if values == nil {
		values = formValues(request)
	}
	existing := cloneRow(input.Existing)
	if mode == ChangeFormEdit && existing == nil {
		object, exists, err := site.ModelStore.Get(ctx, p.ModelAdmin.Model, input.ObjectID)
		if err != nil {
			return gogohttp.Response{}, err
		}
		if !exists {
			return gogohttp.Response{}, fmt.Errorf("admin object %q not found", input.ObjectID)
		}
		existing = object
	}
	cleaned, err := validateAdminModelForm(ctx, request, p.ModelAdmin, existing, values)
	if err != nil {
		return gogohttp.Response{}, err
	}
	if p.ModelAdmin.Hooks.SaveForm != nil {
		object, err := p.ModelAdmin.Hooks.SaveForm(request, cloneRow(cleaned))
		if err != nil {
			return gogohttp.Response{}, err
		}
		if mapped, ok := object.(map[string]any); ok {
			cleaned = cloneRow(mapped)
		}
	}
	cleaned, err = storeAdminUploadedFiles(ctx, site.FileStorage, p.ModelAdmin.Model, cleaned)
	if err != nil {
		return gogohttp.Response{}, err
	}
	inlineFormsets, err := BuildAdminInlineFormsets(ctx, site, p.ModelAdmin, AdminInlineFormsetInput{
		ParentID:  input.ObjectID,
		User:      input.User,
		Request:   request,
		Values:    values,
		Submitted: true,
	})
	if err != nil {
		return gogohttp.Response{}, err
	}
	inlineFormsets, err = ValidateAdminInlineFormsets(ctx, request, inlineFormsets)
	if err != nil {
		return gogohttp.Response{}, AdminFormValidationError{Err: err, InlineFormsets: inlineFormsets, Values: cloneRow(values)}
	}
	m2mValues := adminManyToManyValues(p.ModelAdmin.Model, cleaned)
	scalarValues := adminScalarFormValues(p.ModelAdmin.Model, cleaned)

	var saved map[string]any
	runSave := func(txCtx context.Context) error {
		var err error
		switch mode {
		case ChangeFormAdd:
			saved, err = site.ModelStore.Create(txCtx, p.ModelAdmin.Model, scalarValues)
		case ChangeFormEdit:
			saved, err = site.ModelStore.Update(txCtx, p.ModelAdmin.Model, input.ObjectID, scalarValues, true)
		default:
			return fmt.Errorf("unsupported admin form mode %s", mode)
		}
		if err != nil {
			return err
		}
		if p.ModelAdmin.Hooks.SaveModel != nil {
			if err := p.ModelAdmin.Hooks.SaveModel(request, cloneRow(saved)); err != nil {
				return err
			}
		}
		if len(m2mValues) > 0 {
			m2mStore, ok := site.ModelStore.(ManyToManyStore)
			if !ok {
				return fmt.Errorf("admin model store does not support many-to-many fields")
			}
			objectID := fmt.Sprint(objectPrimaryKey(p.ModelAdmin.Model, saved))
			for field, values := range m2mValues {
				if err := m2mStore.SetManyToMany(txCtx, p.ModelAdmin.Model, objectID, field, values); err != nil {
					return err
				}
			}
		}
		objectID := fmt.Sprint(objectPrimaryKey(p.ModelAdmin.Model, saved))
		if err := SaveAdminInlineFormsets(txCtx, site, request, p.ModelAdmin, objectID, inlineFormsets); err != nil {
			return err
		}
		if p.ModelAdmin.Hooks.SaveRelated != nil {
			if err := p.ModelAdmin.Hooks.SaveRelated(request, cloneRow(saved)); err != nil {
				return err
			}
		}
		return nil
	}
	if txStore, ok := site.ModelStore.(adminAtomicStore); ok {
		if err := txStore.Atomic(ctx, runSave); err != nil {
			return gogohttp.Response{}, err
		}
	} else if err := runSave(ctx); err != nil {
		return gogohttp.Response{}, err
	}

	action := ActionFlagChange
	if mode == ChangeFormAdd {
		action = ActionFlagAddition
	}
	message := adminFormSuccessMessage(p.ModelAdmin, saved, mode)
	if err := logAdminObject(site, input.User, p.ModelAdmin.Model, saved, action, message); err != nil {
		return gogohttp.Response{}, err
	}
	if p.ModelAdmin.Hooks.MessageUser != nil {
		p.ModelAdmin.Hooks.MessageUser(request, message)
	} else {
		messages.Add(request.Context(), messages.LevelSuccess, message)
	}
	if request.URL.Query().Get("_popup") == "1" {
		action := "change"
		if mode == ChangeFormAdd {
			action = "add"
		}
		objectID := popupObjectID(p.ModelAdmin.Model, saved, request)
		return renderAdminPopupResponse(action, objectID, rowDisplay(saved, objectID)), nil
	}
	if mode == ChangeFormAdd && p.ModelAdmin.Hooks.ResponseAdd != nil {
		if handler := p.ModelAdmin.Hooks.ResponseAdd(request, cloneRow(saved)); handler != nil {
			return gogohttp.FromHandler(handler)(ctx, gogohttp.NewRequest(request)), nil
		}
	}
	if mode == ChangeFormEdit && p.ModelAdmin.Hooks.ResponseChange != nil {
		if handler := p.ModelAdmin.Hooks.ResponseChange(request, cloneRow(saved)); handler != nil {
			return gogohttp.FromHandler(handler)(ctx, gogohttp.NewRequest(request)), nil
		}
	}
	return adminSaveRedirect(site, p.ModelAdmin, saved, request), nil
}

func adminFormSuccessMessage(modelAdmin ModelAdmin, object map[string]any, mode ChangeFormMode) string {
	verb := "Changed"
	if mode == ChangeFormAdd {
		verb = "Added"
	}
	objectID := fmt.Sprint(objectPrimaryKey(modelAdmin.Model, object))
	return fmt.Sprintf("%s %s %q.", verb, modelVerboseName(modelAdmin), rowDisplay(object, objectID))
}

func adminScalarFormValues(meta models.Metadata, values map[string]any) map[string]any {
	scalar := cloneRow(values)
	for _, field := range meta.Fields {
		if adminIsManyToManyField(field) {
			delete(scalar, field.Name)
		}
	}
	return scalar
}

func adminManyToManyValues(meta models.Metadata, values map[string]any) map[string][]string {
	result := map[string][]string{}
	for _, field := range meta.Fields {
		if !adminIsManyToManyField(field) {
			continue
		}
		if value, ok := values[field.Name]; ok {
			result[field.Name] = adminStringSlice(value)
		}
	}
	return result
}

func adminIsManyToManyField(field models.FieldMeta) bool {
	return metadataKind(field) == "many_to_many"
}

func adminIsFileField(field models.FieldMeta) bool {
	kind := metadataKind(field)
	return kind == "file" || kind == "image" || kind == "filepath"
}

func adminStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			values = append(values, fmt.Sprint(item))
		}
		return values
	case nil:
		return nil
	default:
		return []string{fmt.Sprint(typed)}
	}
}

func storeAdminUploadedFiles(ctx context.Context, storage files.Storage, meta models.Metadata, values map[string]any) (map[string]any, error) {
	stored := cloneRow(values)
	for _, field := range meta.Fields {
		if !adminIsFileField(field) {
			continue
		}
		value, ok := stored[field.Name]
		if !ok {
			continue
		}
		switch upload := value.(type) {
		case forms.UploadedFile:
			name, err := saveAdminUploadedFile(ctx, storage, field, upload)
			if err != nil {
				return nil, err
			}
			stored[field.Name] = name
		case *forms.UploadedFile:
			if upload == nil {
				continue
			}
			name, err := saveAdminUploadedFile(ctx, storage, field, *upload)
			if err != nil {
				return nil, err
			}
			stored[field.Name] = name
		}
	}
	return stored, nil
}

func saveAdminUploadedFile(ctx context.Context, storage files.Storage, field models.FieldMeta, upload forms.UploadedFile) (string, error) {
	if storage == nil {
		return "", fmt.Errorf("admin file storage is required for field %s", field.Name)
	}
	name := adminUploadStorageName(field, upload.Name)
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("admin upload for field %s has no file name", field.Name)
	}
	return storage.Save(ctx, name, bytes.NewReader(upload.Content))
}

func adminUploadStorageName(field models.FieldMeta, name string) string {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	uploadTo := strings.Trim(strings.ReplaceAll(field.UploadTo, "\\", "/"), "/")
	if uploadTo == "" {
		return base
	}
	return path.Join(uploadTo, base)
}
