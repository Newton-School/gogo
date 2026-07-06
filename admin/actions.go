package admin

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

// ActionHandler executes one admin action.
type ActionHandler func(ActionContext) (ActionResult, error)

// Action describes a global or model-specific admin action.
type Action struct {
	Name                 string
	Label                string
	Permissions          []string
	RequiresConfirmation bool
	Handler              ActionHandler
}

// ActionContext stores selected rows and execution dependencies.
type ActionContext struct {
	Request       *http.Request
	User          auth.User
	Model         models.Metadata
	SelectedIDs   []string
	Selected      []map[string]any
	SelectedQuery models.ObjectQuery
	SelectAcross  bool
	Store         ActionStore
	Confirmed     bool
}

// ActionResult stores action outcome metadata.
type ActionResult struct {
	Message              string
	ConfirmationRequired bool
	Response             http.Handler
}

// ActionStore persists action mutations.
type ActionStore interface {
	DeleteObjects([]map[string]any) error
}

// ActionRegistry stores global actions.
type ActionRegistry struct {
	mu      sync.RWMutex
	actions []Action
}

// NewActionRegistry creates an empty global action registry.
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{}
}

// Register adds one global action.
func (r *ActionRegistry) Register(action Action) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
}

// Actions returns registered global actions.
func (r *ActionRegistry) Actions() []Action {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Action(nil), r.actions...)
}

// AvailableActions returns global actions followed by model actions.
func AvailableActions(global *ActionRegistry, admin ModelAdmin) []Action {
	var actions []Action
	if global != nil {
		actions = append(actions, global.Actions()...)
	}
	actions = append(actions, admin.ActionDefinitions...)
	return actions
}

// DeleteSelectedAction returns the built-in delete selected action.
func DeleteSelectedAction() Action {
	return Action{
		Name:                 "delete_selected",
		Label:                "Delete selected objects",
		Permissions:          []string{"delete"},
		RequiresConfirmation: true,
		Handler: func(ctx ActionContext) (ActionResult, error) {
			if ctx.Store != nil {
				if err := ctx.Store.DeleteObjects(ctx.Selected); err != nil {
					return ActionResult{}, err
				}
			}
			return ActionResult{Message: fmt.Sprintf("Deleted %d objects", len(ctx.Selected))}, nil
		},
	}
}

// ExecuteAction checks permissions, handles confirmation, and executes an action.
func ExecuteAction(action Action, ctx ActionContext) (ActionResult, error) {
	if !actionAllowed(action, ctx.User) {
		return ActionResult{}, ErrAdminPermissionDenied
	}
	if action.RequiresConfirmation && !ctx.Confirmed {
		return ActionResult{ConfirmationRequired: true}, nil
	}
	if action.Handler == nil {
		return ActionResult{}, nil
	}
	return action.Handler(ctx)
}

func actionAllowed(action Action, user auth.User) bool {
	if user.IsActive && user.IsSuperuser {
		return true
	}
	for _, permission := range action.Permissions {
		if permission == "delete" {
			continue
		}
		if !auth.HasPerm(user, permission) {
			return false
		}
	}
	return true
}

// MemoryActionStore records action deletions in memory.
type MemoryActionStore struct {
	Deleted []map[string]any
}

// NewMemoryActionStore creates an empty action store.
func NewMemoryActionStore() *MemoryActionStore {
	return &MemoryActionStore{}
}

// DeleteObjects records deleted rows.
func (s *MemoryActionStore) DeleteObjects(rows []map[string]any) error {
	for _, row := range rows {
		s.Deleted = append(s.Deleted, cloneRow(row))
	}
	return nil
}
