package files

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// Service is an immutable existing-owner binding. Opaque backend/storage/policy
// implementations are trusted ports; their internal mutable state is not copied.
type Service struct{ state *serviceState }
type serviceState struct {
	backend             db.Backend
	alias, storageAlias string
	storage             Storage
	bindings            map[string]ownerBinding
	byModelField        map[string]string
	registry            *models.Registry
}
type ownerBinding struct {
	name, field string
	schema      models.Schema
	pk, policy  []string
	scope       orm.QueryScope
	authorize   func(context.Context, OwnerAction, OwnerSnapshot) error
}

func bindingName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

// NewService freezes declarations without querying or migrating the database.
// Both root scope and current owner authorization are mandatory per binding.
func NewService(config ServiceConfig) (service *Service, err error) {
	defer func() {
		if recover() != nil {
			service = nil
			err = ErrConfiguration
		}
	}()
	if missingValue(config.Backend) || missingValue(config.Storage) || config.Registry == nil || !bindingName(config.StorageAlias) || len(config.Bindings) == 0 || len(config.Bindings) > 128 {
		return nil, ErrConfiguration
	}
	config.Bindings = slices.Clone(config.Bindings)
	for i := range config.Bindings {
		if len(config.Bindings[i].PolicyFields) > 32 {
			return nil, ErrConfiguration
		}
		config.Bindings[i].PolicyFields = slices.Clone(config.Bindings[i].PolicyFields)
	}
	// Local's public handle can be replaced by application code. Capture its
	// immutable private state before any backend method; opaque providers retain
	// their documented cooperative internal-state ownership contract.
	if local, ok := config.Storage.(*Local); ok {
		copy := *local
		config.Storage = &copy
	}
	state := &serviceState{backend: config.Backend, storageAlias: config.StorageAlias, storage: config.Storage, bindings: map[string]ownerBinding{}, byModelField: map[string]string{}, registry: &models.Registry{}}
	for _, declaration := range config.Bindings {
		if !bindingName(declaration.Name) || declaration.Scope == nil || declaration.Authorize == nil || len(declaration.PolicyFields) > 32 {
			return nil, ErrConfiguration
		}
		if _, exists := state.bindings[declaration.Name]; exists {
			return nil, ErrConfiguration
		}
		schema, found := config.Registry.Get(declaration.Model)
		if !found || schema.Parent != "" || schema.Proxy || schema.Abstract || schema.Unmanaged || schema.AutoCreatedBy != "" || schema.Concrete != "" {
			return nil, ErrConfiguration
		}
		file, ok := schema.Field(declaration.Field)
		if !ok || file.Kind != models.File && file.Kind != models.Image || file.Codec != nil || file.Relation != nil || file.PrimaryKey || file.GeneratedExpression != "" || !file.Editable || file.MaxLength > 0 && file.MaxLength < 32 {
			return nil, ErrConfiguration
		}
		key := schema.Key() + "\x00" + file.Name
		if _, exists := state.byModelField[key]; exists {
			return nil, ErrConfiguration
		}
		binding := ownerBinding{name: declaration.Name, field: file.Name, scope: declaration.Scope, authorize: declaration.Authorize}
		keys := schema.PKFields()
		if len(keys) == 0 || len(keys) > 8 {
			return nil, ErrConfiguration
		}
		selected := map[string]bool{file.Name: true}
		for _, pk := range keys {
			if !ownerScalarKind(pk.Kind) || pk.Kind == models.Boolean || pk.Kind == models.File || pk.Kind == models.Image || pk.Null {
				return nil, ErrConfiguration
			}
			binding.pk = append(binding.pk, pk.Name)
			selected[pk.Name] = true
		}
		for _, name := range declaration.PolicyFields {
			if slices.Contains(binding.policy, name) {
				return nil, ErrConfiguration
			}
			binding.policy = append(binding.policy, name)
			selected[name] = true
		}
		// Minimal query descriptors retain exact table/column/type identity, not
		// defaults, validators, methods, choices, relationships or mutable metadata.
		binding.schema = models.Schema{AppLabel: schema.AppLabel, Name: schema.Name, Table: schema.DBTable(), PrimaryKey: slices.Clone(binding.pk)}
		for _, field := range schema.Fields {
			if !selected[field.Name] {
				continue
			}
			if !field.IsStored() || !ownerScalarKind(field.Kind) || field.Codec != nil || field.Relation != nil || field.GeneratedExpression != "" {
				return nil, ErrConfiguration
			}
			binding.schema.Fields = append(binding.schema.Fields, models.Field{Name: field.Name, Kind: field.Kind, Column: field.Column, PrimaryKey: field.PrimaryKey, Null: field.Null, Blank: field.Blank, MaxLength: field.MaxLength})
			delete(selected, field.Name)
		}
		if len(selected) != 0 || binding.schema.Validate() != nil {
			return nil, ErrConfiguration
		}
		state.bindings[binding.name] = binding
		state.byModelField[key] = binding.name
	}
	// Private query registry needs only concrete metadata; no installation state
	// is changed. Owner queries carry their own exact minimal schema descriptors.
	if err := state.registry.Register((&File{}).Schema()); err != nil {
		return nil, ErrConfiguration
	}
	if err := state.registry.Freeze(); err != nil {
		return nil, ErrConfiguration
	}
	state.alias = config.Backend.Alias()
	if !bindingName(state.alias) {
		return nil, ErrConfiguration
	}
	if config.Backend.Capabilities().Require("transactions", "row_locks") != nil {
		return nil, ErrConfiguration
	}
	return &Service{state: state}, nil
}

func ownerScalarKind(kind models.Kind) bool {
	switch kind {
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.UUID, models.Boolean, models.File, models.Image,
		models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto:
		return true
	}
	return false
}

// scalarOwnerValue never calls Stringer, JSON, a codec, or a user conversion.
// Result values use string/bool/int64 builtins and cannot carry mutable state.
func scalarOwnerValue(field models.Field, value any, primary bool) (any, error) {
	if value == nil {
		if field.Null && !primary {
			return nil, nil
		}
		return nil, ErrInvalidOwner
	}
	v := reflect.ValueOf(value)
	if bytes, ok := value.([]byte); ok {
		if len(bytes) > 4096 {
			return nil, ErrLimit
		}
		value = string(bytes)
		v = reflect.ValueOf(value)
	}
	switch field.Kind {
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.UUID, models.File, models.Image:
		if v.Kind() != reflect.String {
			return nil, ErrInvalidOwner
		}
		text := v.String()
		maximum := 4096
		if primary {
			maximum = 512
		}
		if len(text) > maximum || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return nil, ErrInvalidOwner
		}
		if primary && text == "" {
			return nil, ErrInvalidOwner
		}
		if field.Kind == models.UUID {
			return metadataID(text)
		}
		if field.Kind == models.File || field.Kind == models.Image {
			if text != "" && !validKey(text) {
				return nil, ErrInvalidOwner
			}
		}
		return text, nil
	case models.Boolean:
		if v.Kind() != reflect.Bool {
			return nil, ErrInvalidOwner
		}
		return v.Bool(), nil
	default:
		if !ownerScalarKind(field.Kind) {
			return nil, ErrInvalidOwner
		}
		var number int64
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			number = v.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if v.Uint() > math.MaxInt64 {
				return nil, ErrInvalidOwner
			}
			number = int64(v.Uint())
		case reflect.String:
			text := v.String()
			if len(text) > 20 {
				return nil, ErrInvalidOwner
			}
			var err error
			number, err = strconv.ParseInt(text, 10, 64)
			if err != nil || strconv.FormatInt(number, 10) != text {
				return nil, ErrInvalidOwner
			}
		default:
			return nil, ErrInvalidOwner
		}
		minimum, maximum := int64(math.MinInt64), int64(math.MaxInt64)
		switch field.Kind {
		case models.SmallInteger, models.SmallAuto:
			minimum, maximum = math.MinInt16, math.MaxInt16
		case models.Integer, models.Auto:
			minimum, maximum = math.MinInt32, math.MaxInt32
		case models.PositiveSmallInteger:
			minimum, maximum = 0, math.MaxInt16
		case models.PositiveInteger:
			minimum, maximum = 0, math.MaxInt32
		case models.PositiveBigInteger:
			minimum = 0
		}
		if number < minimum || number > maximum {
			return nil, ErrInvalidOwner
		}
		return number, nil
	}
}

type ownerPart struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type ownerReference struct {
	Version int         `json:"v"`
	Model   string      `json:"model"`
	Field   string      `json:"field"`
	Key     []ownerPart `json:"key"`
}
type ownerSelection struct {
	binding   ownerBinding
	key       map[string]any
	reference string
}

func (s serviceState) selectOwner(input OwnerInput) (ownerSelection, error) {
	binding, ok := s.bindings[input.Binding]
	if !ok || len(input.Key) != len(binding.pk) {
		return ownerSelection{}, ErrInvalidOwner
	}
	key := make(map[string]any, len(binding.pk))
	reference := ownerReference{Version: 1, Model: binding.schema.Key(), Field: binding.field}
	for _, name := range binding.pk {
		raw, exists := input.Key[name]
		if !exists {
			return ownerSelection{}, ErrInvalidOwner
		}
		field, _ := binding.schema.Field(name)
		value, err := scalarOwnerValue(field, raw, true)
		if err != nil {
			return ownerSelection{}, err
		}
		text, ok := value.(string)
		if !ok {
			text = strconv.FormatInt(value.(int64), 10)
		}
		key[name] = value
		reference.Key = append(reference.Key, ownerPart{Name: name, Value: text})
	}
	encoded, err := json.Marshal(reference)
	if err != nil || len(encoded) > MaxOwnerReferenceBytes {
		return ownerSelection{}, ErrInvalidOwner
	}
	return ownerSelection{binding: binding, key: key, reference: string(encoded)}, nil
}

func (s serviceState) parseOwner(value string) (ownerSelection, error) {
	if len(value) == 0 || len(value) > MaxOwnerReferenceBytes {
		return ownerSelection{}, ErrInvalidOwner
	}
	var reference ownerReference
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reference) != nil || reference.Version != 1 || len(reference.Key) > 8 {
		return ownerSelection{}, ErrInvalidOwner
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ownerSelection{}, ErrInvalidOwner
	}
	name, ok := s.byModelField[reference.Model+"\x00"+reference.Field]
	if !ok {
		return ownerSelection{}, ErrInvalidOwner
	}
	input := OwnerInput{Binding: name, Key: map[string]any{}}
	for _, part := range reference.Key {
		if _, exists := input.Key[part.Name]; exists {
			return ownerSelection{}, ErrInvalidOwner
		}
		input.Key[part.Name] = part.Value
	}
	selected, err := s.selectOwner(input)
	if err != nil || !bytes.Equal([]byte(selected.reference), []byte(value)) {
		return ownerSelection{}, ErrInvalidOwner
	}
	return selected, nil
}

func copyOwnerValues(values map[string]any) map[string]any {
	copy := make(map[string]any, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
