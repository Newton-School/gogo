package http

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

func newGenericCreateModel(options CreateViewOptions) (*genericModel, map[string]bool, string, error) {
	if options.Store == nil || options.Store.Registry == nil || len(options.Fields) == 0 || len(options.Fields) > 64 || len(options.ReadonlyFields) > 64 {
		return nil, nil, "", ErrGenericConfiguration
	}
	schema, ok := options.Store.Registry.Get(options.Model)
	if !ok || len(schema.Fields) == 0 || len(schema.Fields) > 64 || schema.Abstract || schema.Proxy || schema.Unmanaged || schema.Parent != "" || schema.Concrete != "" || schema.AutoCreatedBy != "" || len(schema.Indexes) > 64 || len(schema.Constraints) > 64 {
		return nil, nil, "", ErrGenericConfiguration
	}
	if !genericCreateSchemaBounded(schema) || len(options.Store.BeforeSave) > 128 || len(options.Store.AfterSave) > 128 {
		return nil, nil, "", ErrGenericConfiguration
	}
	names := make([]string, 0, len(schema.Fields))
	for i := range schema.Fields {
		field := &schema.Fields[i]
		if !genericModelFieldSupported(*field, false) || field.Kind == models.FilePath || field.Element != nil || len(field.Choices) > 128 || len(field.Validators) > 128 {
			return nil, nil, "", ErrGenericConfiguration
		}
		names = append(names, field.Name)
		// Metadata crosses into builtin widgets only after method-free bounding
		// and detachment. Executable defaults/validators remain trusted hooks.
		var err error
		field.Default, err = cloneGenericCreateMetadata(field.Default)
		if err != nil {
			return nil, nil, "", ErrGenericConfiguration
		}
		field.Min, err = cloneGenericCreateMetadata(field.Min)
		if err != nil {
			return nil, nil, "", ErrGenericConfiguration
		}
		field.Max, err = cloneGenericCreateMetadata(field.Max)
		if err != nil {
			return nil, nil, "", ErrGenericConfiguration
		}
		for j := range field.Choices {
			field.Choices[j].Value, err = cloneGenericCreateMetadata(field.Choices[j].Value)
			if err != nil {
				return nil, nil, "", ErrGenericConfiguration
			}
		}
	}
	keys := map[string]bool{}
	for _, field := range schema.PKFields() {
		keys[field.Name] = true
	}
	readonly := map[string]bool{}
	for _, name := range options.ReadonlyFields {
		if readonly[name] || !slices.Contains(options.Fields, name) {
			return nil, nil, "", ErrGenericConfiguration
		}
		readonly[name] = true
	}
	seen, editable := map[string]bool{}, map[string]bool{}
	for _, name := range options.Fields {
		field, exists := schema.Field(name)
		if !exists || seen[name] {
			return nil, nil, "", ErrGenericConfiguration
		}
		seen[name] = true
		if readonly[name] || !field.IsEditable() || keys[name] {
			continue
		}
		if name == "csrfmiddlewaretoken" {
			return nil, nil, "", ErrGenericConfiguration
		}
		if _, err := forms.FieldFromModel(field); err != nil {
			return nil, nil, "", ErrGenericConfiguration
		}
		editable[name] = true
	}
	if len(editable) == 0 {
		return nil, nil, "", ErrGenericConfiguration
	}
	registry := &models.Registry{}
	if registry.Register(schema) != nil || registry.Freeze() != nil {
		return nil, nil, "", ErrGenericConfiguration
	}
	store := *options.Store
	store.Registry = registry
	store.BeforeSave = slices.Clone(store.BeforeSave)
	store.AfterSave = slices.Clone(store.AfterSave)
	model, err := newGenericModel(ModelReadOptions{Store: &store, Model: options.Model, Policy: options.Policy, AllowAnonymous: options.AllowAnonymous, Scope: options.Scope, Fields: names})
	if err != nil {
		return nil, nil, "", err
	}
	scope := options.Scope
	model.scope = func(ctx context.Context, principal auth.Principal, schema models.Schema) (db.Predicate, error) {
		copy, err := cloneGenericCreateSchema(schema)
		if err != nil {
			return db.Predicate{}, ErrUnavailable
		}
		return scope(ctx, principal, copy)
	}
	fingerprint, err := schema.Fingerprint()
	if err != nil {
		return nil, nil, "", ErrGenericConfiguration
	}
	return model, editable, fingerprint, nil
}

// Initial form validation deliberately does not issue uniqueness/constraint
// queries. The real Store checker runs after final preparation in the write
// transaction. Model Clean and validators are trusted read-only application code.
type genericCreateLocalChecker struct{}

func (genericCreateLocalChecker) ValidateUnique(context.Context, models.Record, []string) error {
	return nil
}
func (genericCreateLocalChecker) ValidateConstraints(context.Context, models.Record, []string) error {
	return nil
}

func (v *genericCreate) run(call *readViewCall, data url.Values) (Response, error) {
	ctx := call.base.Context()
	// Freeze scope before Factory, Initial, cleaning, rendering or write hooks.
	query, err := v.model.query(ctx)
	if err != nil {
		return Response{}, err
	}
	model := v.options.Factory()
	if genericNil(model) || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	record, err := models.Bind(model)
	if err != nil || record.State() == nil || record.State().Persisted || !v.matches(record) || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	fields := make([]string, 0, len(v.editable))
	for _, name := range v.options.Fields {
		if v.editable[name] {
			fields = append(fields, name)
		}
	}
	// Factory-provided editable initials are bounded before widget formatting,
	// just like Initial callback data. Never format an opaque model value.
	initialValues := map[string]any{}
	for _, name := range fields {
		value, err := record.Get(name)
		if err != nil {
			return Response{}, ErrUnavailable
		}
		initialValues[name] = value
	}
	if _, err := snapshotTemplateContext(initialValues); err != nil {
		return Response{}, ErrUnavailable
	}
	for name, value := range initialValues {
		copy, err := cloneGenericCreateMetadata(value)
		if err != nil {
			return Response{}, ErrUnavailable
		}
		initialValues[name] = copy
	}
	if !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	formOptions := []forms.Option{forms.WithInitial(initialValues)}
	if call.base.Method == http.MethodPost {
		formOptions = append(formOptions, forms.WithData(data))
	} else if v.options.Initial != nil {
		initial, err := v.options.Initial(call.request())
		if err != nil || !genericContextOK(ctx) {
			return Response{}, ErrUnavailable
		}
		copy, err := snapshotTemplateContext(templates.Context(initial))
		if err != nil {
			return Response{}, ErrUnavailable
		}
		for name := range copy {
			if !v.editable[name] {
				return Response{}, ErrUnavailable
			}
		}
		formOptions = append(formOptions, forms.WithInitial(copy))
	}
	form, err := forms.NewModelForm(ctx, record, forms.ModelFormOptions{Fields: fields, Checker: genericCreateLocalChecker{}}, formOptions...)
	if err != nil || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	valid := form.IsValid()
	if form.Err() != nil || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	if call.base.Method == http.MethodPost && valid {
		if _, err := form.Save(false); err != nil {
			return Response{}, ErrUnavailable
		}
		return v.save(call, model, record, query)
	}
	html, err := form.Render("div")
	if err != nil || len(html) > v.template.maxBytes || !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	errors := map[string]any{}
	for name, items := range form.Errors() {
		if !v.editable[name] && name != forms.NonFieldErrors {
			continue
		}
		messages := make([]string, 0, len(items))
		for _, item := range items {
			messages = append(messages, item.Message)
		}
		errors[name] = messages
	}
	token := security.CSRFToken(call.base)
	if !genericContextOK(ctx) {
		return Response{}, ErrUnavailable
	}
	response, err := v.template.renderWith(call, templates.Context{
		"form":       map[string]any{"html": html, "is_bound": form.IsBound(), "is_valid": valid, "errors": errors},
		"csrf_token": token,
	})
	if err != nil {
		return Response{}, err
	}
	if err := v.authorize(call); err != nil {
		return Response{}, err
	}
	if call.base.Method == http.MethodPost {
		response.Status = http.StatusUnprocessableEntity
	}
	return response, nil
}

func (v *genericCreate) matches(record models.Record) bool {
	schema := record.Schema()
	if !genericCreateSchemaBounded(schema) {
		return false
	}
	fingerprint, err := schema.Fingerprint()
	return err == nil && fingerprint == v.fingerprint
}

func (v *genericCreate) snapshot(record models.Record) (genericModelRow, error) {
	row := genericModelRow{values: make(map[string]any, len(v.model.selected)), database: v.model.store.Backend.Alias()}
	budget := templateContextBudget{remainingValues: templateContextMaxValues, remainingBytes: templateContextMaxBytes}
	jsonBudget := &genericModelJSONBudget{values: templateContextMaxValues, text: templateContextMaxBytes, raw: templateContextMaxBytes}
	for _, name := range v.model.selected {
		field, _ := v.model.schema.Field(name)
		raw, err := record.Get(name)
		if err != nil || !budget.key(name, 1) {
			return genericModelRow{}, ErrUnavailable
		}
		if raw == nil && !record.State().Persisted && (field.IsAuto() || field.DBDefault != "") {
			field.Null = true // Private pending marker, not a nullable schema change.
		}
		value, err := normalizeGenericModelValueWithJSONBudget(field, raw, jsonBudget)
		if err != nil {
			return genericModelRow{}, ErrUnavailable
		}
		if _, ok := budget.copy(reflect.ValueOf(value), 1); !ok {
			return genericModelRow{}, ErrUnavailable
		}
		row.values[name] = value
	}
	return row, nil
}

func (v *genericCreate) policyRecord(row genericModelRow, persisted bool) (*models.MapRecord, error) {
	schema, err := cloneGenericCreateSchema(v.model.schema)
	if err != nil {
		return nil, ErrUnavailable
	}
	record, err := models.NewRecord(schema)
	if err != nil {
		return nil, ErrUnavailable
	}
	record.State().Persisted = persisted
	record.State().Database = row.database
	for _, name := range v.model.selected {
		field, _ := v.model.schema.Field(name)
		if row.values[name] == nil && !persisted && (field.IsAuto() || field.DBDefault != "") {
			field.Null = true
		}
		value, err := normalizeGenericModelValue(field, row.values[name])
		if err != nil || record.Set(name, value) != nil {
			return nil, ErrUnavailable
		}
	}
	return record, nil
}

func (v *genericCreate) identity(row genericModelRow) (map[string]any, error) {
	identity := map[string]any{}
	for _, field := range v.model.schema.PKFields() {
		value, err := decodeGenericLookupValue(field, row.values[field.Name])
		if err != nil {
			return nil, ErrUnavailable
		}
		identity[field.Name] = value
	}
	return identity, nil
}

func (v *genericCreate) persisted(ctx context.Context, query orm.Query[*models.MapRecord], expected genericModelRow) error {
	identity, err := v.identity(expected)
	if err != nil {
		return err
	}
	for _, field := range v.model.schema.PKFields() {
		query = query.Filter(orm.Q(field.Name, identity[field.Name]))
	}
	rows, err := v.model.readRows(ctx, query.Limit(2), 2)
	if err != nil {
		return ErrUnavailable
	}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].values, expected.values) {
		return genericCreateDrift
	}
	return nil
}

var genericCreateDrift = auth.ErrPermissionDenied
