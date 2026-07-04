package models

import "fmt"

const parentLinkDeleteBehavior = "cascade"

// FieldMeta describes model field metadata without importing the fields package.
type FieldMeta struct {
	Name            string
	Column          string
	Kind            string
	ColumnTypes     map[string]string
	SourceModel     string
	PrimaryKey      bool
	Null            bool
	Unique          bool
	DBIndex         bool
	DBDefault       any
	DBCollation     string
	ParentLink      bool
	RelationTarget  string
	TargetFieldName string
	DeleteBehavior  string
}

// ModelRef stores a compact model reference for inheritance metadata.
type ModelRef struct {
	AppLabel  string
	ModelName string
	TableName string
	Abstract  bool
	Proxy     bool
}

// Label returns app_label.ModelName.
func (r ModelRef) Label() string {
	if r.AppLabel == "" || r.ModelName == "" {
		return ""
	}
	return modelKey(r.AppLabel, r.ModelName)
}

// ParentLink describes a generated multi-table inheritance parent link.
type ParentLink struct {
	Parent ModelRef
	Field  FieldMeta
}

// AuthUserExtension stores metadata for extending the framework-owned auth user.
type AuthUserExtension struct {
	UserModel                    ModelRef
	ProfileRelation              string
	ExtensionFields              []FieldMeta
	PreservesFrameworkUserTable  bool
	AllowFrameworkUserTableWrite bool
}

// InheritanceInfo stores all inheritance and composition metadata for a model.
type InheritanceInfo struct {
	AbstractBases     []ModelRef
	MultiTableParents []ParentLink
	ProxyFor          *ModelRef
	AuthUserExtension *AuthUserExtension
}

// Clone returns a deep copy of inheritance metadata.
func (i InheritanceInfo) Clone() InheritanceInfo {
	copied := InheritanceInfo{
		AbstractBases:     append([]ModelRef(nil), i.AbstractBases...),
		MultiTableParents: append([]ParentLink(nil), i.MultiTableParents...),
	}
	if i.ProxyFor != nil {
		value := *i.ProxyFor
		copied.ProxyFor = &value
	}
	if i.AuthUserExtension != nil {
		value := *i.AuthUserExtension
		value.ExtensionFields = cloneFieldMetaSlice(i.AuthUserExtension.ExtensionFields)
		copied.AuthUserExtension = &value
	}
	return copied
}

type inheritanceConfig struct {
	abstractBases     []Metadata
	multiTableParents []Metadata
	proxyBase         *Metadata
	authExtension     *AuthUserExtension
}

// InheritanceOption configures inheritance resolution.
type InheritanceOption func(*inheritanceConfig)

// WithAbstractBase inherits fields from an abstract model.
func WithAbstractBase(parent Metadata) InheritanceOption {
	return func(config *inheritanceConfig) {
		config.abstractBases = append(config.abstractBases, parent)
	}
}

// WithMultiTableParent adds a concrete parent model for multi-table inheritance.
func WithMultiTableParent(parent Metadata) InheritanceOption {
	return func(config *inheritanceConfig) {
		config.multiTableParents = append(config.multiTableParents, parent)
	}
}

// WithProxyBase configures the concrete model a proxy model wraps.
func WithProxyBase(parent Metadata) InheritanceOption {
	return func(config *inheritanceConfig) {
		clone := parent.Clone()
		config.proxyBase = &clone
	}
}

// WithAuthUserExtension configures framework auth user extension metadata.
func WithAuthUserExtension(extension AuthUserExtension) InheritanceOption {
	return func(config *inheritanceConfig) {
		clone := extension
		clone.ExtensionFields = cloneFieldMetaSlice(extension.ExtensionFields)
		config.authExtension = &clone
	}
}

// ResolveInheritance resolves inheritance metadata and inherited field order.
func ResolveInheritance(child Metadata, options ...InheritanceOption) (Metadata, error) {
	resolved := child.Clone()
	resolved.Fields = normalizeFieldSources(resolved.Fields, resolved.Label())

	config := inheritanceConfig{}
	for _, option := range options {
		option(&config)
	}

	for _, base := range config.abstractBases {
		if !base.Abstract {
			return Metadata{}, fmt.Errorf("%w: abstract base %s is not abstract", ErrInvalidMetadata, base.Label())
		}
		resolved.Fields = mergeInheritedFields(resolved.Fields, normalizeFieldSources(base.Fields, base.Label()))
		resolved.Inheritance.AbstractBases = append(resolved.Inheritance.AbstractBases, modelRef(base))
	}

	for _, parent := range config.multiTableParents {
		if parent.Abstract || parent.Proxy {
			return Metadata{}, fmt.Errorf("%w: multi-table parent %s must be concrete", ErrInvalidMetadata, parent.Label())
		}
		link := ParentLink{
			Parent: modelRef(parent),
			Field: FieldMeta{
				Name:           snakeCase(parent.ModelName) + "_ptr",
				Column:         snakeCase(parent.ModelName) + "_ptr_id",
				SourceModel:    resolved.Label(),
				PrimaryKey:     true,
				ParentLink:     true,
				RelationTarget: parent.Label(),
				DeleteBehavior: parentLinkDeleteBehavior,
			},
		}
		resolved.Fields = append(resolved.Fields, link.Field)
		resolved.Inheritance.MultiTableParents = append(resolved.Inheritance.MultiTableParents, link)
	}

	if config.proxyBase != nil {
		if !resolved.Proxy {
			return Metadata{}, fmt.Errorf("%w: proxy model %s must set Proxy", ErrInvalidMetadata, resolved.Label())
		}
		parent := config.proxyBase.Clone()
		if parent.TableName == "" && parent.DBTable != "" {
			parent.TableName = parent.DBTable
		}
		if parent.DBTable == "" && parent.TableName != "" {
			parent.DBTable = parent.TableName
		}
		resolved.TableName = parent.TableName
		resolved.DBTable = parent.DBTable
		if len(resolved.Fields) == 0 {
			resolved.Fields = normalizeFieldSources(parent.Fields, parent.Label())
		}
		ref := modelRef(parent)
		resolved.Inheritance.ProxyFor = &ref
	}

	if config.authExtension != nil {
		extension := *config.authExtension
		if extension.UserModel.Label() == "" || extension.UserModel.TableName == "" || extension.ProfileRelation == "" {
			return Metadata{}, fmt.Errorf("%w: auth user extension requires user model table and profile relation", ErrInvalidMetadata)
		}
		extension.PreservesFrameworkUserTable = true
		extension.ExtensionFields = normalizeFieldSources(extension.ExtensionFields, resolved.Label())
		resolved.Fields = mergeInheritedFields(resolved.Fields, extension.ExtensionFields)
		resolved.Inheritance.AuthUserExtension = &extension
	}

	return resolved, ValidateMetadata(resolved)
}

// ParentSaveOrder returns concrete parent labels before the child label.
func ParentSaveOrder(meta Metadata) []string {
	order := make([]string, 0, len(meta.Inheritance.MultiTableParents)+1)
	for _, parent := range meta.Inheritance.MultiTableParents {
		order = append(order, parent.Parent.Label())
	}
	order = append(order, meta.Label())
	return order
}

// ParentDeleteOrder returns the child label before parent labels unless parent rows are kept.
func ParentDeleteOrder(meta Metadata, keepParents bool) []string {
	order := []string{meta.Label()}
	if keepParents {
		return order
	}
	for i := len(meta.Inheritance.MultiTableParents) - 1; i >= 0; i-- {
		order = append(order, meta.Inheritance.MultiTableParents[i].Parent.Label())
	}
	return order
}

func modelRef(meta Metadata) ModelRef {
	return ModelRef{
		AppLabel:  meta.AppLabel,
		ModelName: meta.ModelName,
		TableName: meta.TableName,
		Abstract:  meta.Abstract,
		Proxy:     meta.Proxy,
	}
}

func normalizeFieldSources(fields []FieldMeta, source string) []FieldMeta {
	copied := cloneFieldMetaSlice(fields)
	for i := range copied {
		if copied[i].SourceModel == "" {
			copied[i].SourceModel = source
		}
	}
	return copied
}

func mergeInheritedFields(child []FieldMeta, parent []FieldMeta) []FieldMeta {
	merged := cloneFieldMetaSlice(parent)
	positions := make(map[string]int, len(merged))
	for i, field := range merged {
		positions[field.Name] = i
	}
	for _, field := range child {
		if position, ok := positions[field.Name]; ok {
			merged[position] = field
			continue
		}
		positions[field.Name] = len(merged)
		merged = append(merged, field)
	}
	return merged
}

func cloneFieldMetaSlice(fields []FieldMeta) []FieldMeta {
	copied := make([]FieldMeta, len(fields))
	for i, field := range fields {
		copied[i] = field
		copied[i].ColumnTypes = cloneStringMap(field.ColumnTypes)
	}
	return copied
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
