package serialization

import (
	"github.com/Newton-School/gogo/core/models"
	"reflect"
	"slices"
	"sort"
)

func nilValue(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return r.IsNil()
	}
	return false
}
func name(s string) bool { return len(s) > 0 && len(s) <= 128 && models.ValidIdentifier(s) }

// New performs no SQL, migrations, policy calls or value conversions. It copies
// only the scalar schema metadata used by these queries; defaults, validators,
// choices and model lifecycle callbacks are deliberately not part of raw loads.
func New(c Config) (out *Fixtures, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = ErrConfiguration
		}
	}()
	if nilValue(c.Backend) || len(c.Profiles) == 0 || len(c.Profiles) > 64 {
		return nil, ErrConfiguration
	}
	l := c.Limits
	if l.MaxBytes == 0 {
		l.MaxBytes = MaxBytes
	}
	if l.MaxRecords == 0 {
		l.MaxRecords = MaxRecords
	}
	if l.MaxRecordBytes == 0 {
		l.MaxRecordBytes = MaxRecordBytes
	}
	if l.MaxBytes < 1 || l.MaxBytes > MaxBytes || l.MaxRecords < 1 || l.MaxRecords > MaxRecords || l.MaxRecordBytes < 1 || l.MaxRecordBytes > MaxRecordBytes || l.MaxRecordBytes > l.MaxBytes {
		return nil, ErrConfiguration
	}
	s := &fixtureState{backend: c.Backend, registry: &models.Registry{}, profiles: map[string]profile{}, limits: l}
	for _, decl := range c.Profiles {
		schema := decl.Schema
		if !name(schema.AppLabel) || !name(schema.Name) || !name(schema.DBTable()) || len(schema.Fields) == 0 || len(schema.Fields) > 128 || len(schema.PrimaryKey) > 8 || len(decl.Fields) == 0 || len(decl.Fields) > 128 || len(decl.PolicyFields) > 128 || decl.Scope == nil || decl.Authorize == nil || schema.Abstract || schema.Proxy || schema.Unmanaged || schema.Parent != "" || schema.Concrete != "" || schema.AutoCreatedBy != "" {
			return nil, ErrConfiguration
		}
		p := profile{schema: models.Schema{AppLabel: schema.AppLabel, Name: schema.Name, Table: schema.DBTable()}, fields: slices.Clone(decl.Fields), policy: slices.Clone(decl.PolicyFields), scope: decl.Scope, authorize: decl.Authorize, load: decl.Import}
		selected, output := map[string]bool{}, map[string]bool{}
		for _, n := range p.fields {
			if !name(n) || output[n] {
				return nil, ErrConfiguration
			}
			output[n] = true
			selected[n] = true
		}
		private := map[string]bool{}
		for _, n := range p.policy {
			if !name(n) || private[n] {
				return nil, ErrConfiguration
			}
			private[n] = true
			selected[n] = true
		}
		for _, n := range schema.PrimaryKey {
			if _, ok := schema.Field(n); !ok {
				return nil, ErrConfiguration
			}
		}
		keys := schema.PKFields()
		if len(keys) == 0 || len(keys) > 8 {
			return nil, ErrConfiguration
		}
		for _, key := range keys {
			if !output[key.Name] || key.Null || !keyKind(key.Kind) || slices.Contains(p.pk, key.Name) {
				return nil, ErrConfiguration
			}
			p.pk = append(p.pk, key.Name)
		}
		p.schema.PrimaryKey = slices.Clone(p.pk)
		seen := map[string]bool{}
		for _, field := range schema.Fields {
			if !name(field.Name) || seen[field.Name] {
				return nil, ErrConfiguration
			}
			seen[field.Name] = true
			if decl.Import && (!output[field.Name] || field.IsAuto() || field.DBDefault != "") {
				return nil, ErrConfiguration
			}
			if !selected[field.Name] {
				continue
			}
			if !name(field.DBColumn()) || !scalarKind(field.Kind) || !field.IsStored() || field.Codec != nil || field.Relation != nil || field.Element != nil || field.GeneratedExpression != "" || field.MaxDigits > 1000 || field.DecimalPlaces > 1000 {
				return nil, ErrConfiguration
			}
			p.schema.Fields = append(p.schema.Fields, models.Field{Name: field.Name, Column: field.DBColumn(), Kind: field.Kind, PrimaryKey: slices.Contains(p.pk, field.Name), Null: field.Null, Blank: field.Blank, MaxLength: field.MaxLength, MinLength: field.MinLength, MaxDigits: field.MaxDigits, DecimalPlaces: field.DecimalPlaces})
			delete(selected, field.Name)
		}
		if len(selected) != 0 || p.schema.Validate() != nil {
			return nil, ErrConfiguration
		}
		sort.Strings(p.fields)
		sort.Strings(p.policy)
		if _, ok := s.profiles[p.schema.Key()]; ok {
			return nil, ErrConfiguration
		}
		s.profiles[p.schema.Key()] = p
		if s.registry.Register(p.schema) != nil {
			return nil, ErrConfiguration
		}
	}
	if s.registry.Freeze() != nil {
		return nil, ErrConfiguration
	}
	// Every mutable declaration has been copied before the first provider call.
	s.alias = c.Backend.Alias()
	s.dialect = c.Backend.Dialect()
	if !name(s.alias) || nilValue(s.dialect) || c.Backend.Capabilities().Require("transactions") != nil {
		return nil, ErrConfiguration
	}
	return &Fixtures{state: s}, nil
}

func scalarKind(k models.Kind) bool {
	switch k {
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.UUID, models.GenericIPAddress, models.FilePath, models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto, models.Boolean, models.Decimal, models.Float, models.Date, models.DateTime, models.Time, models.Duration, models.Binary, models.JSON:
		return true
	}
	return false
}
func keyKind(k models.Kind) bool {
	switch k {
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.UUID, models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto:
		return true
	}
	return false
}
func (s fixtureState) selectModels(format Format, names []string, load bool) ([]profile, error) {
	if (format != JSON && format != JSONL) || len(names) == 0 || len(names) > 64 {
		return nil, ErrInvalid
	}
	selected := slices.Clone(names)
	sort.Strings(selected)
	out := make([]profile, 0, len(selected))
	for i, n := range selected {
		if len(n) > 257 || i > 0 && n == selected[i-1] {
			return nil, ErrInvalid
		}
		p, ok := s.profiles[n]
		if !ok || load && !p.load {
			return nil, ErrInvalid
		}
		out = append(out, p)
	}
	return out, nil
}
