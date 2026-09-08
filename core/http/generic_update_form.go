package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

func (v *genericUpdate) run(call *readViewCall, data url.Values) (Response, error) {
	ctx := call.base.Context()
	query, err := v.form.model.query(ctx)
	if err != nil {
		return Response{}, err
	}
	raw, err := v.key(call.request())
	if err != nil {
		if !genericContextOK(ctx) || err != ErrInvalidLookup {
			return Response{}, ErrUnavailable
		}
		return Response{}, ErrNotFound
	}
	keys := v.form.model.schema.PKFields()
	// Decode all returned components before another callback can mutate the
	// decoder's map. Intrinsic lookup values never invoke application codecs.
	identity := make(map[string]any, len(keys))
	valid := len(raw) == len(keys)
	if valid {
		for _, field := range keys {
			value, ok := raw[field.Name]
			if !ok {
				valid = false
				break
			}
			value, err = decodeGenericLookupValue(field, value)
			if err != nil {
				valid = false
				break
			}
			identity[field.Name] = value
			query = query.Filter(orm.Q(field.Name, value))
		}
	}
	if !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	if !valid {
		return Response{}, ErrNotFound
	}
	query = query.OrderBy().Limit(2)
	if call.base.Method == http.MethodPost {
		return v.save(call, query, identity, data)
	}
	row, err := v.load(ctx, query)
	if err != nil {
		return Response{}, err
	}
	if err := v.objectGrant(ctx, row); err != nil {
		return Response{}, err
	}
	_, record, err := v.hydrate(ctx, row)
	if err != nil {
		return Response{}, err
	}
	form, err := v.modelForm(ctx, record, row, nil, false)
	if err != nil {
		return Response{}, err
	}
	return v.renderForm(call, form, row, false)
}

func (v *genericUpdate) load(ctx context.Context, query orm.Query[*models.MapRecord]) (genericModelRow, error) {
	rows, err := v.form.model.readRows(ctx, query, 2)
	if err != nil {
		return genericModelRow{}, ErrUnavailable
	}
	if len(rows) == 0 {
		return genericModelRow{}, ErrNotFound
	}
	if len(rows) != 1 {
		return genericModelRow{}, ErrUnavailable
	}
	return rows[0], nil
}

func (v *genericUpdate) objectGrant(ctx context.Context, row genericModelRow) error {
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	record, err := v.form.policyRecord(row, true)
	if err != nil {
		return err
	}
	identity, err := v.form.identity(row)
	if err != nil {
		return err
	}
	err = v.form.model.policy.Authorize(ctx, auth.FromContext(ctx), "change", auth.Resource{
		App: v.form.model.schema.AppLabel, Model: v.form.model.schema.Name, ID: identity, Object: record,
	})
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	if err == auth.ErrPermissionDenied || err == auth.ErrUnauthenticated {
		return ErrNotFound
	}
	return err
}

func (v *genericUpdate) hydrate(ctx context.Context, row genericModelRow) (models.Model, models.Record, error) {
	model := v.form.options.Factory()
	if genericNil(model) || !genericContextOK(ctx) {
		return nil, nil, ErrUnavailable
	}
	record, err := models.Bind(model)
	if err != nil || record.State() == nil || record.State().Persisted || !v.form.matches(record) || !genericContextOK(ctx) {
		return nil, nil, ErrUnavailable
	}
	for _, name := range v.form.model.selected {
		field, _ := v.form.model.schema.Field(name)
		value, err := normalizeGenericModelValue(field, row.values[name])
		if err != nil || record.Set(name, value) != nil {
			return nil, nil, ErrUnavailable
		}
	}
	record.State().Persisted, record.State().Database = true, row.database
	record.State().Deferred, record.State().Related = nil, nil
	current, err := v.form.snapshot(record)
	if err != nil || !reflect.DeepEqual(current.values, row.values) || !v.form.matches(record) || !genericContextOK(ctx) {
		return nil, nil, ErrUnavailable
	}
	return model, record, nil
}

func (v *genericUpdate) modelForm(ctx context.Context, record models.Record, row genericModelRow, data url.Values, bound bool) (*forms.ModelForm, error) {
	fields := make([]string, 0, len(v.form.editable))
	initial := make(map[string]any, len(v.form.editable))
	overrides := map[string]forms.Field{}
	for _, name := range v.form.options.Fields {
		if !v.form.editable[name] {
			continue
		}
		fields = append(fields, name)
		metadata, _ := v.form.model.schema.Field(name)
		value, err := normalizeGenericModelValue(metadata, row.values[name])
		if err != nil {
			return nil, ErrUnavailable
		}
		field, err := forms.FieldFromModel(metadata)
		if err != nil {
			return nil, ErrUnavailable
		}
		if field.Kind == forms.JSON {
			// Stored Go strings are JSON scalar values, not JSON document text.
			// SQL NULL remains an empty control; explicit JSON null renders null.
			if value != nil {
				raw, err := json.Marshal(value)
				if err != nil || len(raw) > 1<<20 {
					return nil, ErrUnavailable
				}
				value = string(raw)
			}
			overrides[name] = genericUpdateJSONField(field)
		}
		initial[name] = value
	}
	options := []forms.Option{forms.WithInitial(initial)}
	if bound {
		options = append(options, forms.WithData(data))
	}
	form, err := forms.NewModelForm(ctx, record, forms.ModelFormOptions{
		Fields: fields, Overrides: overrides, Checker: genericCreateLocalChecker{},
	}, options...)
	if err != nil || !genericContextOK(ctx) {
		return nil, ErrUnavailable
	}
	return form, nil
}

// Keep the ordinary JSON cleaner for all values except explicit top-level
// null. The string-backed outer control distinguishes document text from empty
// input before the builtin JSON field turns both null and SQL NULL into nil.
// This is private to Update and does not change public Field or ModelForm rules.
func genericUpdateJSONField(base forms.Field) forms.Field {
	field := base.Clone()
	field.Kind, field.Widget = forms.Char, forms.InputWidget{Type: "textarea"}
	strip := false
	field.Strip = &strip
	field.MinLength, field.MaxLength = 0, 0
	field.MinValue, field.MaxValue, field.Validators = nil, nil, nil
	field.Clean = func(ctx context.Context, value any) (any, error) {
		text, ok := value.(string)
		if !ok || len(text) > 1<<20 {
			return nil, forms.Error{Code: "max_length", Message: "This value is too large."}
		}
		if strings.TrimSpace(text) == "null" {
			for _, validate := range base.Validators {
				if err := validate(ctx, models.JSONNull); err != nil {
					return nil, err
				}
			}
			if base.Clean != nil {
				return base.Clean(ctx, models.JSONNull)
			}
			return models.JSONNull, nil
		}
		inner, err := forms.New([]forms.Field{base}, forms.WithContext(ctx), forms.WithData(url.Values{base.Name: {text}}))
		if err != nil {
			return nil, forms.Error{Code: "invalid", Message: "Enter valid JSON."}
		}
		if !inner.IsValid() {
			for _, failure := range inner.Errors()[base.Name] {
				return nil, failure
			}
			return nil, forms.Error{Code: "invalid", Message: "Enter valid JSON."}
		}
		return inner.CleanedData()[base.Name], nil
	}
	return field
}

func (v *genericUpdate) renderForm(call *readViewCall, form *forms.ModelForm, row genericModelRow, invalid bool) (Response, error) {
	ctx := call.base.Context()
	valid := form.IsValid()
	if form.Err() != nil || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	html, err := form.Render("div")
	if err != nil || len(html) > v.form.template.maxBytes || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	failures := map[string]any{}
	for name, items := range form.Errors() {
		if !v.form.editable[name] && name != forms.NonFieldErrors {
			continue
		}
		messages := make([]string, 0, len(items))
		for _, item := range items {
			messages = append(messages, item.Message)
		}
		failures[name] = messages
	}
	token := security.CSRFToken(call.base)
	if !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	response, err := v.form.template.renderWith(call, templates.Context{
		"form": map[string]any{"html": html, "is_bound": form.IsBound(), "is_valid": valid, "errors": failures}, "csrf_token": token,
	})
	if err != nil {
		return Response{}, err
	}
	if err := v.authorize(call); err != nil {
		return Response{}, err
	}
	if err := v.objectGrant(ctx, row); err != nil {
		return Response{}, err
	}
	if invalid {
		response.Status = http.StatusUnprocessableEntity
	}
	return response, nil
}
