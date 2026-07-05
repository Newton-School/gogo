package auth

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cybersaksham/gogo/models"
)

var (
	// ErrDuplicateContentType is returned when a natural key is registered twice.
	ErrDuplicateContentType = errors.New("duplicate content type")
	// ErrContentTypeNotFound is returned when a content type lookup fails.
	ErrContentTypeNotFound = errors.New("content type not found")
)

// ContentType identifies one installed model for permission and admin lookups.
type ContentType struct {
	ID       int64
	AppLabel string
	Model    string
}

// NaturalKey returns Django-style app_label.model identity.
func (c ContentType) NaturalKey() string {
	return naturalKey(c.AppLabel, c.Model)
}

// ModelMeta returns metadata for the framework content type table.
func (ContentType) ModelMeta() models.Metadata {
	return authMetadata(models.Metadata{
		AppLabel:          appLabel,
		ModelName:         "ContentType",
		TableName:         "gogo_content_type",
		DBTable:           "gogo_content_type",
		VerboseName:       "content type",
		VerboseNamePlural: "content types",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "app_label", Column: "app_label", VerboseName: "App label"},
			{Name: "model", Column: "model", VerboseName: "Model"},
		},
		Constraints: []models.Constraint{
			{Name: "gogo_content_type_app_model_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("app_label"), models.Asc("model")}},
		},
	})
}

// ContentTypeRegistry stores content types by natural key and primary key.
type ContentTypeRegistry struct {
	mu      sync.RWMutex
	nextID  int64
	byKey   map[string]ContentType
	byID    map[int64]ContentType
	ordered []ContentType
}

// NewContentTypeRegistry creates an empty content type registry.
func NewContentTypeRegistry() *ContentTypeRegistry {
	return &ContentTypeRegistry{
		nextID: 1,
		byKey:  make(map[string]ContentType),
		byID:   make(map[int64]ContentType),
	}
}

// NewContentTypeRegistryFromModels creates content types from registered models.
func NewContentTypeRegistryFromModels(modelRegistry *models.Registry) (*ContentTypeRegistry, error) {
	registry := NewContentTypeRegistry()
	if modelRegistry == nil {
		return registry, nil
	}
	for _, meta := range modelRegistry.ContentTypeMetadata() {
		if _, err := registry.RegisterModel(meta); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// RegisterModel registers a content type for a model metadata entry.
func (r *ContentTypeRegistry) RegisterModel(meta models.Metadata) (ContentType, error) {
	if meta.AppLabel == "" || meta.ModelName == "" {
		return ContentType{}, fmt.Errorf("%w: app label and model name are required", ErrContentTypeNotFound)
	}
	return r.Register(ContentType{AppLabel: meta.AppLabel, Model: meta.ModelName})
}

// Register inserts one content type and assigns an ID when needed.
func (r *ContentTypeRegistry) Register(contentType ContentType) (ContentType, error) {
	normalized := normalizeContentType(contentType)
	key := normalized.NaturalKey()

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byKey[key]; exists {
		return ContentType{}, fmt.Errorf("%w: %s", ErrDuplicateContentType, key)
	}
	if normalized.ID == 0 {
		normalized.ID = r.nextID
	}
	if normalized.ID >= r.nextID {
		r.nextID = normalized.ID + 1
	}
	r.byKey[key] = normalized
	r.byID[normalized.ID] = normalized
	r.ordered = append(r.ordered, normalized)
	return normalized, nil
}

// LookupByModel returns a content type for a model name in any case.
func (r *ContentTypeRegistry) LookupByModel(appLabel, modelName string) (ContentType, bool) {
	return r.LookupNaturalKey(appLabel, modelName)
}

// LookupNaturalKey returns a content type by natural key.
func (r *ContentTypeRegistry) LookupNaturalKey(appLabel, model string) (ContentType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	contentType, ok := r.byKey[naturalKey(appLabel, model)]
	return contentType, ok
}

// LookupID returns a content type by ID.
func (r *ContentTypeRegistry) LookupID(id int64) (ContentType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	contentType, ok := r.byID[id]
	return contentType, ok
}

// All returns registered content types in registration order.
func (r *ContentTypeRegistry) All() []ContentType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ContentType(nil), r.ordered...)
}

// StaleContentTypes returns persisted rows that no longer match registered models.
func (r *ContentTypeRegistry) StaleContentTypes(existing []ContentType) []ContentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stale := make([]ContentType, 0)
	for _, row := range existing {
		if _, ok := r.byKey[normalizeContentType(row).NaturalKey()]; !ok {
			stale = append(stale, row)
		}
	}
	return stale
}

func normalizeContentType(contentType ContentType) ContentType {
	contentType.AppLabel = strings.ToLower(strings.TrimSpace(contentType.AppLabel))
	contentType.Model = strings.ToLower(strings.TrimSpace(contentType.Model))
	return contentType
}

func naturalKey(appLabel, model string) string {
	return strings.ToLower(strings.TrimSpace(appLabel)) + "." + strings.ToLower(strings.TrimSpace(model))
}
