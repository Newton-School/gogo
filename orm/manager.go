package orm

import "github.com/Newton-School/gogo/models"

// ManagerMethod transforms a queryset.
type ManagerMethod func(QuerySet) QuerySet

// Manager is the Django-style entrypoint for model querysets.
type Manager struct {
	Name     string
	Meta     models.Metadata
	Compiler Compiler
	methods  map[string]ManagerMethod
}

// ManagerSet stores default and base managers for a model.
type ManagerSet struct {
	Default Manager
	Base    Manager
}

// NewManager creates a default manager for a model.
func NewManager(meta models.Metadata, compiler Compiler) Manager {
	name := meta.DefaultManagerName
	if name == "" {
		name = "objects"
	}
	return Manager{
		Name:     name,
		Meta:     meta.Clone(),
		Compiler: compiler,
		methods:  make(map[string]ManagerMethod),
	}
}

// ManagersForModel creates default and base managers from metadata.
func ManagersForModel(meta models.Metadata, compiler Compiler) ManagerSet {
	defaultManager := NewManager(meta, compiler)
	baseName := meta.BaseManagerName
	if baseName == "" {
		baseName = defaultManager.Name
	}
	baseManager := defaultManager
	baseManager.Name = baseName
	return ManagerSet{Default: defaultManager, Base: baseManager}
}

// QuerySet returns a fresh queryset for the manager model.
func (m Manager) QuerySet() QuerySet {
	return NewQuerySet(m.Meta, m.Compiler)
}

// All returns all objects for the manager model.
func (m Manager) All() QuerySet {
	return m.QuerySet().All()
}

// WithMethod registers a custom manager method.
func (m Manager) WithMethod(name string, method ManagerMethod) Manager {
	copied := m.clone()
	copied.methods[name] = method
	return copied
}

// Call invokes a custom manager method.
func (m Manager) Call(name string) (QuerySet, bool) {
	method, ok := m.methods[name]
	if !ok {
		return QuerySet{}, false
	}
	return method(m.QuerySet()), true
}

// Inherit returns this manager bound to child metadata.
func (m Manager) Inherit(child models.Metadata) Manager {
	copied := m.clone()
	copied.Meta = child.Clone()
	return copied
}

func (m Manager) clone() Manager {
	m.Meta = m.Meta.Clone()
	methods := make(map[string]ManagerMethod, len(m.methods))
	for key, value := range m.methods {
		methods[key] = value
	}
	m.methods = methods
	return m
}

// TypedManager is a generic manager bound to a model type.
type TypedManager[T models.Model] struct {
	Manager
}

// NewTypedManager creates a typed model manager.
func NewTypedManager[T models.Model](meta models.Metadata, compiler Compiler) TypedManager[T] {
	return TypedManager[T]{Manager: NewManager(meta, compiler)}
}

// ModelName returns the managed model name.
func (m TypedManager[T]) ModelName() string {
	return m.Meta.ModelName
}
