// Package models provides the shared schema and validation vocabulary used by
// the ORM, forms, serializers, migrations and optional Admin module.
package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type Kind string

const (
	SmallInteger         Kind = "small_integer"
	Integer              Kind = "integer"
	BigInteger           Kind = "big_integer"
	PositiveSmallInteger Kind = "positive_small_integer"
	PositiveInteger      Kind = "positive_integer"
	PositiveBigInteger   Kind = "positive_big_integer"
	SmallAuto            Kind = "small_auto"
	Auto                 Kind = "auto"
	BigAuto              Kind = "big_auto"
	UUID                 Kind = "uuid"
	Decimal              Kind = "decimal"
	Float                Kind = "float"
	Boolean              Kind = "boolean"
	Char                 Kind = "char"
	Text                 Kind = "text"
	Slug                 Kind = "slug"
	Email                Kind = "email"
	URL                  Kind = "url"
	GenericIPAddress     Kind = "ip_address"
	FilePath             Kind = "file_path"
	Date                 Kind = "date"
	DateTime             Kind = "datetime"
	Time                 Kind = "time"
	Duration             Kind = "duration"
	Binary               Kind = "binary"
	JSON                 Kind = "json"
	File                 Kind = "file"
	Image                Kind = "image"
	ForeignKey           Kind = "foreign_key"
	OneToOne             Kind = "one_to_one"
	ManyToMany           Kind = "many_to_many"
	Generated            Kind = "generated"
	Array                Kind = "array"
	HStore               Kind = "hstore"
	Range                Kind = "range"
	SearchVector         Kind = "search_vector"
	Geometry             Kind = "geometry"
	Geography            Kind = "geography"
	Raster               Kind = "raster"
	Custom               Kind = "custom"
)

type DeletePolicy string

const (
	Cascade    DeletePolicy = "CASCADE"
	Protect    DeletePolicy = "PROTECT"
	Restrict   DeletePolicy = "RESTRICT"
	SetNull    DeletePolicy = "SET_NULL"
	SetDefault DeletePolicy = "SET_DEFAULT"
	DoNothing  DeletePolicy = "DO_NOTHING"
)

type Choice struct {
	Value any
	Label string
}
type Validator func(context.Context, any) error
type Codec interface {
	Encode(any) (any, error)
	Decode(any) (any, error)
}
type Relation struct {
	Target           string
	TargetFields     []string
	RelatedName      string
	RelatedQueryName string
	OnDelete         DeletePolicy
	Through          string
	ThroughFields    []string
	// Symmetrical defaults to true for self many-to-many relations. A false
	// pointer enables directed relationships and their reverse manager.
	Symmetrical  *bool
	NoConstraint bool
}
type Field struct {
	Name                                                                    string
	StructField                                                             string
	Column                                                                  string
	Kind                                                                    Kind
	Null, Blank, Editable, PrimaryKey, Unique, DBIndex, AutoNow, AutoNowAdd bool
	Label, HelpText, Comment, Collation, Tablespace                         string
	MaxLength, MinLength, MaxDigits, DecimalPlaces                          int
	Min, Max                                                                any
	Choices                                                                 []Choice
	Default                                                                 any
	DefaultFunc                                                             func() any `json:"-"`
	DefaultID                                                               string
	DBDefault                                                               string
	GeneratedExpression                                                     string
	Validators                                                              []Validator `json:"-"`
	Codec                                                                   Codec       `json:"-"`
	Relation                                                                *Relation
	Element                                                                 *Field
	RangeType                                                               Kind
	UniqueForDate, UniqueForMonth, UniqueForYear                            string
}

func (f Field) DBColumn() string {
	if f.Column != "" {
		return f.Column
	}
	return f.Name
}
func (f Field) GoField() string {
	if f.StructField != "" {
		return f.StructField
	}
	return f.Name
}
func (f Field) IsAuto() bool { return f.Kind == Auto || f.Kind == BigAuto || f.Kind == SmallAuto }
func (f Field) IsEditable() bool {
	return f.Editable && !f.IsAuto() && !f.AutoNow && !f.AutoNowAdd && f.Kind != Generated
}
func (f Field) IsStored() bool { return f.Kind != ManyToMany }
func (f Field) HasDefault() bool {
	return f.Default != nil || f.DefaultFunc != nil || f.DBDefault != ""
}

type Index struct {
	Name              string
	Fields            []string
	Unique            bool
	Method, Condition string
	Include           []string
	Concurrent        bool
	NullsDistinct     *bool
}
type Constraint struct {
	Name, Kind            string
	Fields                []string
	Expression, Condition string
	Deferrable            bool
	NullsDistinct         *bool
}
type Schema struct {
	AppLabel, Name, Table                   string
	Fields                                  []Field
	PrimaryKey                              []string
	Indexes                                 []Index
	Constraints                             []Constraint
	Ordering                                []string
	Abstract, Proxy, Unmanaged              bool
	Parent, ParentLink, Concrete            string
	AutoCreatedBy                           string
	AutoCreatedField                        string
	Label, LabelPlural, Comment, Tablespace string
	RequiredCapabilities                    []string
}

func (s Schema) Key() string { return s.AppLabel + "." + s.Name }
func (s Schema) DBTable() string {
	if s.Table != "" {
		return s.Table
	}
	return strings.ToLower(s.AppLabel + "_" + s.Name)
}
func (s Schema) Field(name string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}
func (s Schema) PKFields() []Field {
	var fields []Field
	if len(s.PrimaryKey) > 0 {
		for _, name := range s.PrimaryKey {
			if f, ok := s.Field(name); ok {
				fields = append(fields, f)
			}
		}
		return fields
	}
	for _, f := range s.Fields {
		if f.PrimaryKey {
			fields = append(fields, f)
		}
	}
	return fields
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidIdentifier(value string) bool { return identifier.MatchString(value) }
func (s Schema) Validate() error {
	if !ValidIdentifier(s.AppLabel) || !ValidIdentifier(s.Name) || !ValidIdentifier(s.DBTable()) {
		return errors.New("models: invalid app, model or table identifier")
	}
	seen, columns := map[string]bool{}, map[string]bool{}
	for _, f := range s.Fields {
		if !ValidIdentifier(f.Name) || !ValidIdentifier(f.DBColumn()) || seen[f.Name] || columns[f.DBColumn()] {
			return fmt.Errorf("models: duplicate or invalid field %q", f.Name)
		}
		seen[f.Name], columns[f.DBColumn()] = true, true
		if !knownKind(f.Kind) {
			return fmt.Errorf("models: unknown field kind %q", f.Kind)
		}
		if f.MaxLength < 0 || f.MinLength < 0 || (f.MaxLength > 0 && f.MinLength > f.MaxLength) {
			return fmt.Errorf("models: invalid length bounds for %s", f.Name)
		}
		if f.Kind == Decimal && (f.MaxDigits < 1 || f.DecimalPlaces < 0 || f.DecimalPlaces > f.MaxDigits) {
			return fmt.Errorf("models: invalid decimal precision for %s", f.Name)
		}
		if f.DefaultFunc != nil && f.DefaultID == "" {
			return fmt.Errorf("models: callable default for %s needs stable DefaultID", f.Name)
		}
		if (f.Kind == ForeignKey || f.Kind == OneToOne || f.Kind == ManyToMany) && f.Relation == nil {
			return fmt.Errorf("models: relation %s needs target metadata", f.Name)
		}
		if f.Relation != nil && f.Relation.OnDelete == SetNull && !f.Null {
			return fmt.Errorf("models: SET_NULL requires nullable %s", f.Name)
		}
		if f.Kind == Array && f.Element == nil {
			return fmt.Errorf("models: array %s needs element field", f.Name)
		}
	}
	if !s.Abstract && len(s.PKFields()) == 0 {
		return errors.New("models: concrete schema needs a primary key")
	}
	for _, key := range s.PrimaryKey {
		if !seen[key] {
			return fmt.Errorf("models: unknown primary key field %s", key)
		}
	}
	for _, index := range s.Indexes {
		if !ValidIdentifier(index.Name) {
			return fmt.Errorf("models: invalid index name %q", index.Name)
		}
		for _, name := range append(append([]string{}, index.Fields...), index.Include...) {
			if !seen[name] {
				return fmt.Errorf("models: index references unknown field %s", name)
			}
		}
	}
	constraintNames := map[string]bool{}
	for _, constraint := range s.Constraints {
		if !ValidIdentifier(constraint.Name) || constraintNames[constraint.Name] {
			return errors.New("models: invalid or duplicate constraint name")
		}
		constraintNames[constraint.Name] = true
		for _, name := range constraint.Fields {
			if !seen[name] {
				return errors.New("models: constraint references unknown field")
			}
		}
		if constraint.Condition != "" && constraint.Deferrable {
			return errors.New("models: conditional constraints cannot be deferred")
		}
		if strings.ContainsAny(constraint.Condition+constraint.Expression, ";\x00") {
			return errors.New("models: invalid constraint expression")
		}
		switch strings.ToLower(constraint.Kind) {
		case "unique":
			if len(constraint.Fields) == 0 {
				return errors.New("models: unique constraint requires fields")
			}
		case "check":
			if constraint.Expression == "" {
				return errors.New("models: check constraint requires expression")
			}
		default:
			return errors.New("models: unsupported constraint kind")
		}
	}
	return nil
}
func knownKind(k Kind) bool {
	switch k {
	case SmallInteger, Integer, BigInteger, PositiveSmallInteger, PositiveInteger, PositiveBigInteger, SmallAuto, Auto, BigAuto, UUID, Decimal, Float, Boolean, Char, Text, Slug, Email, URL, GenericIPAddress, FilePath, Date, DateTime, Time, Duration, Binary, JSON, File, Image, ForeignKey, OneToOne, ManyToMany, Generated, Array, HStore, Range, SearchVector, Geometry, Geography, Raster, Custom:
		return true
	}
	return false
}
func (s Schema) Fingerprint() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Clone isolates mutable descriptor slices before exposing a registry snapshot.
func (s Schema) Clone() Schema {
	s.Fields = append([]Field(nil), s.Fields...)
	for i := range s.Fields {
		f := &s.Fields[i]
		f.Choices = append([]Choice(nil), f.Choices...)
		f.Validators = append([]Validator(nil), f.Validators...)
		if f.Relation != nil {
			r := *f.Relation
			r.TargetFields = append([]string(nil), r.TargetFields...)
			r.ThroughFields = append([]string(nil), r.ThroughFields...)
			if r.Symmetrical != nil {
				value := *r.Symmetrical
				r.Symmetrical = &value
			}
			f.Relation = &r
		}
		if f.Element != nil {
			e := Schema{Fields: []Field{*f.Element}}.Clone().Fields[0]
			f.Element = &e
		}
	}
	s.PrimaryKey = append([]string(nil), s.PrimaryKey...)
	s.Ordering = append([]string(nil), s.Ordering...)
	s.RequiredCapabilities = append([]string(nil), s.RequiredCapabilities...)
	s.Indexes = append([]Index(nil), s.Indexes...)
	for i := range s.Indexes {
		s.Indexes[i].Fields = append([]string(nil), s.Indexes[i].Fields...)
		s.Indexes[i].Include = append([]string(nil), s.Indexes[i].Include...)
		if s.Indexes[i].NullsDistinct != nil {
			v := *s.Indexes[i].NullsDistinct
			s.Indexes[i].NullsDistinct = &v
		}
	}
	s.Constraints = append([]Constraint(nil), s.Constraints...)
	for i := range s.Constraints {
		s.Constraints[i].Fields = append([]string(nil), s.Constraints[i].Fields...)
		if s.Constraints[i].NullsDistinct != nil {
			v := *s.Constraints[i].NullsDistinct
			s.Constraints[i].NullsDistinct = &v
		}
	}
	return s
}

type Registry struct {
	mu        sync.RWMutex
	schemas   map[string]Schema
	automatic map[string]bool
	frozen    bool
}

func (r *Registry) Register(schema Schema) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return errors.New("models: registry is frozen")
	}
	if err := schema.Validate(); err != nil {
		return err
	}
	if schema.AutoCreatedBy != "" || schema.AutoCreatedField != "" {
		return errors.New("models: automatic intermediary metadata is reserved for the registry")
	}
	if r.schemas == nil {
		r.schemas = map[string]Schema{}
	}
	if _, ok := r.schemas[schema.Key()]; ok {
		return fmt.Errorf("models: duplicate schema %s", schema.Key())
	}
	r.schemas[schema.Key()] = schema.Clone()
	return nil
}
func (r *Registry) Get(key string) (Schema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.schemas[key]
	return s.Clone(), ok
}

// All returns a deterministic, independent snapshot for relation graph walkers.
func (r *Registry) All() []Schema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.schemas))
	for key := range r.schemas {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Schema, 0, len(keys))
	for _, key := range keys {
		result = append(result, r.schemas[key].Clone())
	}
	return result
}
func (r *Registry) Freeze() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return nil
	}
	generated := []Schema{}
	for _, source := range r.schemas {
		for _, field := range source.Fields {
			if field.Kind != ManyToMany || field.Relation == nil || field.Relation.Through != "" {
				continue
			}
			target, ok := r.schemas[field.Relation.Target]
			if !ok {
				return fmt.Errorf("models: unknown relation target %s", field.Relation.Target)
			}
			through, err := ImplicitThrough(source, field, target)
			if err != nil {
				return err
			}
			if _, ok := r.schemas[through.Key()]; ok {
				return errors.New("models: implicit through schema collides with a declared model")
			}
			for _, previous := range generated {
				if previous.Key() == through.Key() || previous.DBTable() == through.DBTable() {
					return errors.New("models: automatic intermediary collision")
				}
			}
			generated = append(generated, through)
		}
	}
	for _, s := range r.schemas {
		for _, f := range s.Fields {
			if f.Relation != nil {
				target, ok := r.schemas[f.Relation.Target]
				if !ok {
					return fmt.Errorf("models: unknown relation target %s", f.Relation.Target)
				}
				for _, key := range f.Relation.TargetFields {
					if _, ok := target.Field(key); !ok {
						return fmt.Errorf("models: unknown relation target field %s", key)
					}
				}
			}
		}
	}
	r.automatic = map[string]bool{}
	for _, through := range generated {
		r.schemas[through.Key()] = through
		r.automatic[through.Key()] = true
	}
	r.frozen = true
	return nil
}

// IsAutomatic reports framework provenance, not a caller-controlled schema flag.
// Only intermediaries synthesized during successful Freeze return true.
func (r *Registry) IsAutomatic(key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.automatic[key]
}
