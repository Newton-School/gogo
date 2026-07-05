package admin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cybersaksham/gogo/auth"
	gogohttp "github.com/cybersaksham/gogo/http"
	"github.com/cybersaksham/gogo/messages"
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

type adminAtomicStore interface {
	Atomic(context.Context, func(context.Context) error) error
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

	var saved map[string]any
	runSave := func(txCtx context.Context) error {
		var err error
		switch mode {
		case ChangeFormAdd:
			saved, err = site.ModelStore.Create(txCtx, p.ModelAdmin.Model, cleaned)
		case ChangeFormEdit:
			saved, err = site.ModelStore.Update(txCtx, p.ModelAdmin.Model, input.ObjectID, cleaned, true)
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
