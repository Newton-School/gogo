package admin

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/app"
	"github.com/Newton-School/gogo/models"
)

var (
	ErrAlreadyRegistered = errors.New("model is already registered")
	ErrNotRegistered     = errors.New("model is not registered")
	ErrUnmanagedModel    = errors.New("unmanaged model cannot be registered")
)

// NewRegistry creates an empty model admin registry.
func NewRegistry() *Registry {
	return &Registry{byModel: make(map[string]ModelAdmin)}
}

// Register resolves and registers a model.
func (r *Registry) Register(model models.Model, admin ModelAdmin) error {
	return r.RegisterMetadata(models.ResolveMetadata(model), admin)
}

// RegisterMetadata registers already resolved model metadata.
func (r *Registry) RegisterMetadata(meta models.Metadata, admin ModelAdmin) error {
	if r.byModel == nil {
		r.byModel = make(map[string]ModelAdmin)
	}
	if !meta.IsManaged() && !admin.AllowUnmanaged {
		return fmt.Errorf("%w: %s", ErrUnmanagedModel, meta.Label())
	}
	label := meta.Label()
	if label == "" {
		return fmt.Errorf("%w: model label is required", ErrNotRegistered)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byModel[label]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, label)
	}
	admin.Model = meta.Clone()
	r.byModel[label] = admin.clone()
	r.order = append(r.order, label)
	return nil
}

// Unregister removes a model admin registration.
func (r *Registry) Unregister(label string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byModel[label]; !exists {
		return fmt.Errorf("%w: %s", ErrNotRegistered, label)
	}
	delete(r.byModel, label)
	for i, existing := range r.order {
		if existing == label {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return nil
}

// IsRegistered reports whether a model label has a registered admin.
func (r *Registry) IsRegistered(label string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byModel[label]
	return ok
}

// GetAdmin returns admin configuration by model label.
func (r *Registry) GetAdmin(label string) (ModelAdmin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	admin, ok := r.byModel[label]
	if !ok {
		return ModelAdmin{}, false
	}
	return admin.clone(), true
}

// RegisteredModels returns model labels in registration order.
func (r *Registry) RegisteredModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Autodiscover registers admin resources exposed by installed apps.
func (r *Registry) Autodiscover(apps *app.Registry) error {
	if apps == nil {
		return nil
	}
	for _, resource := range apps.Admin() {
		meta := models.Metadata{
			AppLabel:  resource.AppLabel,
			ModelName: resource.ModelName,
			TableName: resource.AppLabel + "_" + strings.ToLower(resource.ModelName),
		}
		if err := r.RegisterMetadata(meta, ModelAdmin{Handler: resource.Handler}); err != nil {
			return err
		}
	}
	return nil
}
