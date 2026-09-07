package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/Newton-School/gogo/core/models"
)

// ModelPersistence keeps parent and relations within one real transaction. Its
// implementation must carry the transaction in the callback context.
type ModelPersistence interface {
	Atomic(context.Context, func(context.Context) error) error
	Save(context.Context, models.Record) error
	SaveRelations(context.Context, models.Record, map[string][]any) error
}
type ModelFormOptions struct {
	Fields          []string
	Exclude         []string
	Readonly        []string
	Overrides       map[string]Field
	ResolveRelation func(context.Context, models.Field, []string) ([]any, error)
	Checker         models.ConstraintChecker
	Persistence     ModelPersistence
}
type ModelForm struct {
	*Form
	record             models.Record
	options            ModelFormOptions
	ctx                context.Context
	relations          map[string][]any
	relationIdentities map[string][]string
	prepared           bool
	validationErr      error
}

func NewModelForm(ctx context.Context, record models.Record, config ModelFormOptions, options ...Option) (*ModelForm, error) {
	if record == nil || ctx == nil {
		return nil, errors.New("forms: model record and context are required")
	}
	if len(config.Fields) == 0 {
		return nil, errors.New("forms: ModelForm requires an explicit field allowlist")
	}
	schema := record.Schema()
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	fields := []Field{}
	initial := map[string]any{}
	for _, name := range config.Fields {
		if seen[name] {
			return nil, fmt.Errorf("forms: duplicate model form field %s", name)
		}
		seen[name] = true
		metadata, ok := schema.Field(name)
		if !ok {
			return nil, fmt.Errorf("forms: unknown model form field %s", name)
		}
		if slices.Contains(config.Exclude, name) || slices.Contains(config.Readonly, name) || !metadata.IsEditable() {
			continue
		}
		field, err := FieldFromModel(metadata)
		if override, ok := config.Overrides[name]; ok {
			field = override
			field.Name = name
			err = nil
		}
		if err != nil {
			return nil, err
		}
		if metadata.Relation != nil {
			meta := metadata
			field.Resolve = func(ctx context.Context, ids []string) ([]any, error) {
				if config.ResolveRelation == nil {
					return nil, Error{"invalid_choice", "Select a valid choice."}
				}
				return config.ResolveRelation(ctx, meta, ids)
			}
		}
		fields = append(fields, field)
		if metadata.IsStored() {
			value, err := record.Get(name)
			if err != nil {
				return nil, err
			}
			initial[name] = value
		}
	}
	m := &ModelForm{record: record, options: config, ctx: ctx, relations: map[string][]any{}, relationIdentities: map[string][]string{}}
	allOptions := []Option{WithInitial(initial)}
	allOptions = append(allOptions, options...)
	allOptions = append(allOptions, WithContext(ctx), WithClean(m.validateModel))
	form, err := New(fields, allOptions...)
	if err != nil {
		return nil, err
	}
	m.Form = form
	return m, nil
}

func FieldFromModel(metadata models.Field) (Field, error) {
	f := NewField(metadata.Name, Kind(metadata.Kind))
	f.Required = !metadata.Blank
	f.Label = metadata.Label
	f.HelpText = metadata.HelpText
	f.MaxLength = metadata.MaxLength
	f.MinLength = metadata.MinLength
	f.MaxDigits = metadata.MaxDigits
	f.DecimalPlaces = metadata.DecimalPlaces
	f.AllowUnicode = metadata.AllowUnicode
	switch metadata.Kind {
	case models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger:
		f.Kind = Integer
	case models.Char, models.Text:
		f.Kind = Char
		if metadata.Kind == models.Text {
			f.Widget = InputWidget{Type: "textarea"}
		}
	case models.GenericIPAddress:
		f.Kind = IP
	case models.Boolean:
		f.Kind = Boolean
		f.Required = false
		if metadata.Null {
			f.Kind = NullBoolean
		}
	case models.ForeignKey, models.OneToOne:
		f.Kind = ModelChoice
	case models.ManyToMany:
		f.Kind = ModelMultipleChoice
	case models.Array, models.HStore, models.Range:
		f.Kind = JSON
	case models.Binary, models.SearchVector, models.Geometry, models.Geography, models.Raster, models.Custom:
		return Field{}, fmt.Errorf("forms: field %s requires a form override", metadata.Name)
	}
	for _, choice := range metadata.Choices {
		f.Choices = append(f.Choices, Choice{fmt.Sprint(choice.Value), choice.Label})
	}
	if len(f.Choices) > 0 {
		f.Kind = TypedChoice
		choices := append([]models.Choice(nil), metadata.Choices...)
		f.Coerce = func(value string) (any, error) {
			for _, choice := range choices {
				if fmt.Sprint(choice.Value) == value {
					return cloneValue(choice.Value), nil
				}
			}
			return nil, Error{Code: "invalid_choice", Message: "Select a valid choice."}
		}
	}
	if metadata.Min != nil {
		if n, err := strconv.ParseFloat(fmt.Sprint(metadata.Min), 64); err == nil {
			f.MinValue = &n
		}
	}
	if metadata.Max != nil {
		if n, err := strconv.ParseFloat(fmt.Sprint(metadata.Max), 64); err == nil {
			f.MaxValue = &n
		}
	}
	return f, nil
}
func (m *ModelForm) validateModel(form *Form) error {
	excluded := []string{}
	for _, field := range m.record.Schema().Fields {
		value, ok := form.cleaned[field.Name]
		if ok && field.Relation != nil {
			values := []any{value}
			if field.Kind == models.ManyToMany {
				values, _ = value.([]any)
			}
			keys := []string{}
			for _, item := range values {
				key, err := relationValueKey(item)
				if err != nil {
					return Error{"invalid_choice", "Select a valid choice."}
				}
				keys = append(keys, key)
			}
			m.relationIdentities[field.Name] = keys
		}
		if !ok || !field.IsStored() {
			excluded = append(excluded, field.Name)
			if ok && field.Kind == models.ManyToMany {
				if values, ok := value.([]any); ok {
					m.relations[field.Name] = values
				}
			}
			continue
		}
		if err := m.record.Set(field.Name, value); err != nil {
			form.AddError(field.Name, Error{"invalid", "Enter a valid value."})
			excluded = append(excluded, field.Name)
		}
	}
	err := models.FullClean(m.ctx, m.record, models.CleanOptions{Exclude: excluded}, m.options.Checker)
	if err != nil {
		var validation *models.ValidationError
		if models.IsValidationOnly(err) && errors.As(err, &validation) {
			for name, values := range validation.Fields {
				for _, value := range values {
					form.AddError(name, Error{value.Code, value.Message})
				}
			}
		} else {
			m.validationErr = modelValidationFailure{cause: err}
			return Error{"validation_unavailable", "Validation is temporarily unavailable."}
		}
	}
	m.prepared = true
	return nil
}
func (m *ModelForm) Instance() models.Record { return m.record }

// Err returns an operational failure from model/constraint validation, distinct
// from invalid user input in Errors. Call it after IsValid (or directly; cleaning
// is cached) before mapping invalid input to an HTTP form response. Its message
// is safe and stable; errors.Is preserves the underlying provider/cancellation
// identity for trusted handling. Do not render an unwrapped provider error.
func (m *ModelForm) Err() error {
	m.Form.fullClean()
	return m.validationErr
}

type modelValidationFailure struct{ cause error }

func (modelValidationFailure) Error() string {
	return "forms: model validation is temporarily unavailable"
}
func (e modelValidationFailure) Unwrap() error { return e.cause }

// CheckRelations repeats the scoped resolver and rejects changed identities.
// Use the final write transaction's context when a custom save pipeline owns it.
func (m *ModelForm) CheckRelations(ctx context.Context) error {
	if err := m.Err(); err != nil {
		return err
	}
	if ctx == nil || !m.IsValid() || !m.prepared {
		return errors.New("forms: valid model form and transaction context required")
	}
	return m.recheckRelations(ctx)
}

// CleanedRelations returns the validated many-to-many values. Persistence must
// still call CheckRelations and re-scope these identities within its transaction.
func (m *ModelForm) CleanedRelations() (map[string][]any, error) {
	if err := m.Err(); err != nil {
		return nil, err
	}
	if !m.IsValid() || !m.prepared {
		return nil, errors.New("forms: cannot read invalid model relations")
	}
	result := map[string][]any{}
	for name, values := range m.relations {
		result[name] = append([]any(nil), values...)
	}
	return result, nil
}
func (m *ModelForm) Save(commit bool) (models.Record, error) {
	if err := m.Err(); err != nil {
		return nil, err
	}
	if !m.IsValid() || !m.prepared {
		return nil, errors.New("forms: cannot save invalid model form")
	}
	if !commit {
		return m.record, nil
	}
	if m.options.Persistence == nil {
		return nil, errors.New("forms: model persistence is required")
	}
	err := m.options.Persistence.Atomic(m.ctx, func(ctx context.Context) error {
		if err := m.recheckRelations(ctx); err != nil {
			return err
		}
		if err := m.options.Persistence.Save(ctx, m.record); err != nil {
			return err
		}
		return m.options.Persistence.SaveRelations(ctx, m.record, m.relations)
	})
	if err != nil {
		return nil, err
	}
	return m.record, nil
}
func (m *ModelForm) SaveM2M() error {
	if err := m.Err(); err != nil {
		return err
	}
	if !m.IsValid() || !m.record.State().Persisted {
		return errors.New("forms: save the valid parent before SaveM2M")
	}
	if m.options.Persistence == nil {
		return errors.New("forms: model persistence is required")
	}
	return m.options.Persistence.Atomic(m.ctx, func(ctx context.Context) error {
		if err := m.recheckRelations(ctx); err != nil {
			return err
		}
		return m.options.Persistence.SaveRelations(ctx, m.record, m.relations)
	})
}
func (m *ModelForm) recheckRelations(ctx context.Context) error {
	for _, field := range m.fields {
		metadata, _ := m.record.Schema().Field(field.Name)
		if metadata.Relation == nil {
			continue
		}
		expected := m.relationIdentities[field.Name]
		if metadata.IsStored() {
			value, err := m.record.Get(field.Name)
			if err != nil || !sameRelationKeys(expected, []any{value}) {
				return Error{"invalid_choice", "The selected relationship changed. Select it again."}
			}
		} else if !sameRelationKeys(expected, m.relations[field.Name]) {
			return Error{"invalid_choice", "The selected relationship changed. Select it again."}
		}
		ids := stringValues(m.raw(field))
		if len(ids) == 0 || len(ids) == 1 && ids[0] == "" {
			continue
		}
		if m.options.ResolveRelation == nil {
			return Error{"invalid_choice", "Select a valid choice."}
		}
		resolved, err := m.options.ResolveRelation(ctx, metadata, uniqueStrings(ids))
		if err != nil {
			return err
		}
		// A resolver may use aliases or other mutable lookup values. Rechecking
		// success alone is insufficient: never save a previously cleaned object
		// when the same submitted ID now resolves to a different identity.
		if !sameRelationKeys(expected, resolved) {
			return Error{"invalid_choice", "The selected relationship changed. Select it again."}
		}
	}
	return nil
}

func sameRelationKeys(left []string, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	counts := map[string]int{}
	for _, key := range left {
		counts[key]++
	}
	for _, value := range right {
		key, err := relationValueKey(value)
		if err != nil || counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func relationValueKey(value any) (string, error) {
	record, ok := value.(models.Record)
	if !ok {
		if model, isModel := value.(models.Model); isModel {
			var err error
			record, err = models.Bind(model)
			if err != nil {
				return "", err
			}
			ok = true
		}
	}
	if ok {
		values := []any{record.Schema().Key()}
		for _, field := range record.Schema().PKFields() {
			part, err := record.Get(field.Name)
			if err != nil {
				return "", err
			}
			values = append(values, part)
		}
		value = values
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
