package forms

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/models"
	modelfields "github.com/Newton-School/gogo/models/fields"
)

const (
	managementTotalForms   = "TOTAL_FORMS"
	managementInitialForms = "INITIAL_FORMS"
	managementMinForms     = "MIN_NUM_FORMS"
	managementMaxForms     = "MAX_NUM_FORMS"
	formsetDeleteField     = "DELETE"
	formsetOrderField      = "ORDER"
)

// FormSetFormOptions configures one form inside a formset.
type FormSetFormOptions struct {
	Index   int
	Prefix  string
	Data    map[string]any
	Initial map[string]any
}

// FormSetFormFactory builds one form for an index.
type FormSetFormFactory func(FormSetFormOptions) *Form

// FormSetOptions configures a Django-style formset.
type FormSetOptions struct {
	Prefix      string
	Data        map[string]any
	Initial     []map[string]any
	Extra       int
	MinForms    int
	MaxForms    int
	CanDelete   bool
	CanOrder    bool
	FormFactory FormSetFormFactory
}

// ManagementForm stores parsed formset management counters.
type ManagementForm struct {
	TotalForms   int
	InitialForms int
	MinForms     int
	MaxForms     int
}

// BoundForm stores a form and its formset index.
type BoundForm struct {
	Index int
	Form  *Form
}

// FormSet coordinates repeated instances of one form.
type FormSet struct {
	prefix       string
	data         map[string]any
	initial      []map[string]any
	canDelete    bool
	canOrder     bool
	formFactory  FormSetFormFactory
	management   ManagementForm
	forms        []BoundForm
	nonFormErrs  ErrorList
	cleaned      bool
	managementOK bool
}

func NewFormSet(options FormSetOptions) *FormSet {
	prefix := options.Prefix
	if prefix == "" {
		prefix = "form"
	}
	factory := options.FormFactory
	if factory == nil {
		factory = func(opts FormSetFormOptions) *Form {
			return NewForm(FormOptions{Data: opts.Data, Initial: opts.Initial})
		}
	}
	formset := &FormSet{
		prefix:       prefix,
		data:         cloneAnyMap(options.Data),
		initial:      cloneInitialForms(options.Initial),
		canDelete:    options.CanDelete,
		canOrder:     options.CanOrder,
		formFactory:  factory,
		managementOK: true,
	}
	if options.Data != nil {
		formset.management = formset.parseManagement(options)
	} else {
		formset.management = ManagementForm{
			TotalForms:   len(options.Initial) + options.Extra,
			InitialForms: len(options.Initial),
			MinForms:     options.MinForms,
			MaxForms:     options.MaxForms,
		}
	}
	if formset.managementOK {
		formset.buildForms()
	}
	return formset
}

func (fs *FormSet) ManagementForm() ManagementForm {
	if fs == nil {
		return ManagementForm{}
	}
	return fs.management
}

func (fs *FormSet) Forms() []BoundForm {
	if fs == nil {
		return nil
	}
	return append([]BoundForm(nil), fs.forms...)
}

func (fs *FormSet) InitialForms() []BoundForm {
	if fs == nil {
		return nil
	}
	end := minInt(fs.management.InitialForms, len(fs.forms))
	return append([]BoundForm(nil), fs.forms[:end]...)
}

func (fs *FormSet) NewForms() []BoundForm {
	if fs == nil {
		return nil
	}
	start := minInt(fs.management.InitialForms, len(fs.forms))
	return append([]BoundForm(nil), fs.forms[start:]...)
}

func (fs *FormSet) DeletedForms() []BoundForm {
	if fs == nil || !fs.canDelete {
		return nil
	}
	deleted := make([]BoundForm, 0)
	for _, form := range fs.forms {
		if fs.isDeleted(form.Index) {
			deleted = append(deleted, form)
		}
	}
	return deleted
}

func (fs *FormSet) OrderedForms() []BoundForm {
	if fs == nil {
		return nil
	}
	ordered := make([]BoundForm, 0, len(fs.forms))
	for _, form := range fs.forms {
		if fs.isDeleted(form.Index) || fs.isEmptyExtra(form.Index) {
			continue
		}
		ordered = append(ordered, form)
	}
	if !fs.canOrder {
		return ordered
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return fs.orderValue(ordered[i].Index) < fs.orderValue(ordered[j].Index)
	})
	return ordered
}

func (fs *FormSet) EmptyForms() []BoundForm {
	if fs == nil {
		return nil
	}
	empty := make([]BoundForm, 0)
	for _, form := range fs.forms {
		if fs.isEmptyExtra(form.Index) {
			empty = append(empty, form)
		}
	}
	return empty
}

func (fs *FormSet) IsValid() bool {
	if fs == nil {
		return false
	}
	fs.fullClean()
	return len(fs.nonFormErrs) == 0
}

func (fs *FormSet) NonFormErrors() ErrorList {
	if fs == nil {
		return nil
	}
	fs.fullClean()
	return append(ErrorList(nil), fs.nonFormErrs...)
}

func (fs *FormSet) fullClean() {
	if fs.cleaned {
		return
	}
	if !fs.managementOK {
		fs.cleaned = true
		return
	}
	active := 0
	for _, boundForm := range fs.forms {
		if fs.isDeleted(boundForm.Index) || fs.isEmptyExtra(boundForm.Index) {
			continue
		}
		active++
		if boundForm.Form != nil && !boundForm.Form.IsValid() {
			fs.nonFormErrs = append(fs.nonFormErrs, ValidationError{Message: fmt.Sprintf("form %d has errors", boundForm.Index)})
		}
	}
	if fs.management.MinForms > 0 && active < fs.management.MinForms {
		fs.nonFormErrs = append(fs.nonFormErrs, ValidationError{Message: fmt.Sprintf("Please submit at least %d forms.", fs.management.MinForms)})
	}
	if fs.management.MaxForms > 0 && active > fs.management.MaxForms {
		fs.nonFormErrs = append(fs.nonFormErrs, ValidationError{Message: fmt.Sprintf("Please submit at most %d forms.", fs.management.MaxForms)})
	}
	fs.cleaned = true
}

func (fs *FormSet) parseManagement(options FormSetOptions) ManagementForm {
	total, ok := fs.managementInt(managementTotalForms)
	if !ok {
		return ManagementForm{}
	}
	initial, ok := fs.managementInt(managementInitialForms)
	if !ok {
		return ManagementForm{}
	}
	minForms := options.MinForms
	if value, ok := fs.optionalManagementInt(managementMinForms); ok {
		minForms = value
	}
	maxForms := options.MaxForms
	if value, ok := fs.optionalManagementInt(managementMaxForms); ok {
		maxForms = value
	}
	return ManagementForm{TotalForms: total, InitialForms: initial, MinForms: minForms, MaxForms: maxForms}
}

func (fs *FormSet) managementInt(name string) (int, bool) {
	value, ok := fs.data[fs.managementKey(name)]
	if !ok {
		fs.managementOK = false
		fs.nonFormErrs = append(fs.nonFormErrs, ValidationError{Message: "management form is missing " + name})
		return 0, false
	}
	integer, err := intFromAny(value)
	if err != nil {
		fs.managementOK = false
		fs.nonFormErrs = append(fs.nonFormErrs, ValidationError{Message: "management form has invalid " + name})
		return 0, false
	}
	return integer, true
}

func (fs *FormSet) optionalManagementInt(name string) (int, bool) {
	value, ok := fs.data[fs.managementKey(name)]
	if !ok {
		return 0, false
	}
	integer, err := intFromAny(value)
	return integer, err == nil
}

func (fs *FormSet) managementKey(name string) string {
	return fs.prefix + "-" + name
}

func (fs *FormSet) buildForms() {
	total := fs.management.TotalForms
	if total < 0 {
		total = 0
	}
	fs.forms = make([]BoundForm, 0, total)
	for index := 0; index < total; index++ {
		initial := map[string]any{}
		if index < len(fs.initial) {
			initial = cloneAnyMap(fs.initial[index])
		}
		prefix := fs.indexPrefix(index)
		form := fs.formFactory(FormSetFormOptions{
			Index:   index,
			Prefix:  prefix,
			Data:    fs.formData(index),
			Initial: initial,
		})
		fs.forms = append(fs.forms, BoundForm{Index: index, Form: form})
	}
}

func (fs *FormSet) formData(index int) map[string]any {
	if fs.data == nil {
		return nil
	}
	prefix := fs.indexPrefix(index) + "-"
	data := map[string]any{}
	for key, value := range fs.data {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		if name == formsetDeleteField || name == formsetOrderField {
			continue
		}
		data[name] = value
	}
	return data
}

func (fs *FormSet) indexPrefix(index int) string {
	return fs.prefix + "-" + strconv.Itoa(index)
}

func (fs *FormSet) isDeleted(index int) bool {
	if !fs.canDelete || fs.data == nil {
		return false
	}
	return widgetBool(fs.data[fs.indexPrefix(index)+"-"+formsetDeleteField])
}

func (fs *FormSet) orderValue(index int) int {
	if fs.data == nil {
		return index
	}
	value, err := intFromAny(fs.data[fs.indexPrefix(index)+"-"+formsetOrderField])
	if err != nil {
		return index + 1_000_000
	}
	return value
}

func (fs *FormSet) isEmptyExtra(index int) bool {
	if index < fs.management.InitialForms {
		return false
	}
	for _, value := range fs.formData(index) {
		if !emptyValue(value) {
			return false
		}
	}
	return true
}

// InlineFormSetOptions configures model forms tied to one parent model.
type InlineFormSetOptions struct {
	FormSetOptions
	Parent        models.Model
	RelationField string
	ModelFactory  func(index int) models.Model
	ModelFields   []modelfields.Field
	Include       []string
	Exclude       []string
	Labels        map[string]string
	HelpTexts     map[string]string
	Widgets       map[string]Widget
	FieldClasses  map[string]ModelFieldFactory
	Store         models.InstanceStore
	Context       context.Context
	SaveOptions   []models.SaveOption
}

// InlineFormSet stores a formset of child model forms.
type InlineFormSet struct {
	*FormSet

	Parent        models.Model
	RelationField string
	ModelForms    []*ModelForm
	Store         models.InstanceStore
	Context       context.Context
	SaveOptions   []models.SaveOption
}

func NewInlineFormSet(options InlineFormSetOptions) *InlineFormSet {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	modelForms := make([]*ModelForm, 0)
	formSetOptions := options.FormSetOptions
	formSetOptions.FormFactory = func(formOptions FormSetFormOptions) *Form {
		var model models.Model
		if options.ModelFactory != nil {
			model = options.ModelFactory(formOptions.Index)
		}
		modelForm := NewModelForm(ModelFormOptions{
			Model:        model,
			ModelFields:  options.ModelFields,
			Include:      options.Include,
			Exclude:      options.Exclude,
			Labels:       options.Labels,
			HelpTexts:    options.HelpTexts,
			Widgets:      options.Widgets,
			FieldClasses: options.FieldClasses,
			Data:         formOptions.Data,
			Initial:      formOptions.Initial,
			Store:        options.Store,
			Context:      ctx,
			SaveOptions:  options.SaveOptions,
		})
		modelForms = append(modelForms, modelForm)
		return modelForm.Form
	}
	return &InlineFormSet{
		FormSet:       NewFormSet(formSetOptions),
		Parent:        options.Parent,
		RelationField: options.RelationField,
		ModelForms:    modelForms,
		Store:         options.Store,
		Context:       ctx,
		SaveOptions:   append([]models.SaveOption(nil), options.SaveOptions...),
	}
}

func (fs *InlineFormSet) Save(commit bool) ([]models.Model, error) {
	if fs == nil {
		return nil, fmt.Errorf("%w: inline formset is nil", ErrValidation)
	}
	if !fs.IsValid() {
		return nil, fmt.Errorf("%w: invalid inline formset", ErrValidation)
	}
	deleted := map[int]bool{}
	for _, form := range fs.DeletedForms() {
		deleted[form.Index] = true
	}
	saved := make([]models.Model, 0, len(fs.ModelForms))
	for index, modelForm := range fs.ModelForms {
		if deleted[index] || fs.isEmptyExtra(index) {
			continue
		}
		model, err := modelForm.Save(false)
		if err != nil {
			return nil, err
		}
		if fs.RelationField != "" && fs.Parent != nil {
			if err := setModelValue(model, fs.RelationField, fs.Parent); err != nil {
				return nil, err
			}
		}
		if commit {
			if err := models.Save(fs.Context, model, fs.Store, fs.SaveOptions...); err != nil {
				return nil, err
			}
		}
		saved = append(saved, model)
	}
	return saved, nil
}

func cloneInitialForms(initial []map[string]any) []map[string]any {
	cloned := make([]map[string]any, len(initial))
	for i, values := range initial {
		cloned[i] = cloneAnyMap(values)
	}
	return cloned
}

func intFromAny(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint:
		return int(typed), nil
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil
	case string:
		return strconv.Atoi(typed)
	default:
		return strconv.Atoi(fmt.Sprint(value))
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
