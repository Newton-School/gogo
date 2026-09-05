package forms

import (
	"context"
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
	record    models.Record
	options   ModelFormOptions
	ctx       context.Context
	relations map[string][]any
	prepared  bool
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
	m := &ModelForm{record: record, options: config, ctx: ctx, relations: map[string][]any{}}
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
		if errors.As(err, &validation) {
			for name, values := range validation.Fields {
				for _, value := range values {
					form.AddError(name, Error{value.Code, value.Message})
				}
			}
		} else {
			return Error{"validation_unavailable", "Validation is temporarily unavailable."}
		}
	}
	m.prepared = true
	return nil
}
func (m *ModelForm) Instance() models.Record { return m.record }
func (m *ModelForm) Save(commit bool) (models.Record, error) {
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
		ids := stringValues(m.raw(field))
		if len(ids) == 0 || len(ids) == 1 && ids[0] == "" {
			continue
		}
		if m.options.ResolveRelation == nil {
			return Error{"invalid_choice", "Select a valid choice."}
		}
		if _, err := m.options.ResolveRelation(ctx, metadata, ids); err != nil {
			return err
		}
	}
	return nil
}
