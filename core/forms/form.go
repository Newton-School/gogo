package forms

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/url"
	"reflect"
	"regexp"
	"sync"
)

const NonFieldErrors = "__all__"

type Option func(*Form)

func WithData(v url.Values) Option { return func(f *Form) { f.bound = true; f.data = cloneValues(v) } }
func WithFiles(v map[string][]*multipart.FileHeader) Option {
	return func(f *Form) {
		f.bound = true
		f.files = make(map[string][]*multipart.FileHeader, len(v))
		for k, x := range v {
			f.files[k] = append([]*multipart.FileHeader(nil), x...)
		}
	}
}
func WithInitial(v map[string]any) Option    { return func(f *Form) { f.initial = cloneMap(v) } }
func WithPrefix(v string) Option             { return func(f *Form) { f.prefix = v } }
func WithContext(ctx context.Context) Option { return func(f *Form) { f.ctx = ctx } }
func WithClean(fn func(*Form) error) Option  { return func(f *Form) { f.clean = fn } }

// Form is request-local. Cleaning is cached. Bind data is copied at construction.
// Do not mutate a Form concurrently; immutable declarations may be reused safely.
type Form struct {
	fields  []Field
	data    url.Values
	files   map[string][]*multipart.FileHeader
	initial map[string]any
	prefix  string
	bound   bool
	ctx     context.Context
	once    sync.Once
	cleaned map[string]any
	errors  ErrorDict
	changed []string
	clean   func(*Form) error
}

func New(fields []Field, options ...Option) (*Form, error) {
	f := &Form{ctx: context.Background(), initial: map[string]any{}, cleaned: map[string]any{}, errors: ErrorDict{}}
	seen := map[string]bool{}
	for _, field := range fields {
		if !fieldName.MatchString(field.Name) || seen[field.Name] || field.Name == NonFieldErrors {
			return nil, fmt.Errorf("forms: invalid or duplicate field %q", field.Name)
		}
		seen[field.Name] = true
		f.fields = append(f.fields, field.Clone())
	}
	for _, option := range options {
		option(f)
	}
	if f.ctx == nil {
		return nil, errors.New("forms: nil context")
	}
	if f.prefix != "" && !fieldName.MatchString(f.prefix) {
		return nil, errors.New("forms: invalid prefix")
	}
	return f, nil
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func (f *Form) IsBound() bool { return f.bound }
func (f *Form) IsValid() bool { f.fullClean(); return f.bound && len(f.errors) == 0 }
func (f *Form) Errors() ErrorDict {
	f.fullClean()
	r := ErrorDict{}
	for k, v := range f.errors {
		r[k] = append(ErrorList(nil), v...)
	}
	return r
}
func (f *Form) CleanedData() map[string]any { f.fullClean(); return cloneMap(f.cleaned) }
func (f *Form) ChangedData() []string       { f.fullClean(); return append([]string(nil), f.changed...) }
func (f *Form) HasChanged() bool            { return len(f.ChangedData()) > 0 }
func (f *Form) Fields() []Field {
	result := make([]Field, len(f.fields))
	for i, field := range f.fields {
		result[i] = field.Clone()
	}
	return result
}
func (f *Form) Prefix() string { return f.prefix }
func (f *Form) AddError(name string, err error) {
	if err == nil {
		return
	}
	if name == "" {
		name = NonFieldErrors
	}
	var many validationErrors
	if errors.As(err, &many) {
		f.errors[name] = append(f.errors[name], many.values...)
	} else {
		f.errors[name] = append(f.errors[name], errorValue(err))
	}
	delete(f.cleaned, name)
}
func (f *Form) HasError(name, code string) bool {
	for _, e := range f.Errors()[name] {
		if code == "" || e.Code == code {
			return true
		}
	}
	return false
}
func (f *Form) Value(name string) any { return f.cleaned[name] }
func (f *Form) name(name string) string {
	if f.prefix == "" {
		return name
	}
	return f.prefix + "-" + name
}
func (f *Form) initialValue(field Field) any {
	if v, ok := f.initial[field.Name]; ok {
		return v
	}
	return field.Initial
}
func (f *Form) raw(field Field) any {
	if field.Disabled || !f.bound {
		return f.initialValue(field)
	}
	name := f.name(field.Name)
	if field.Kind == File || field.Kind == Image {
		if files := f.files[name]; len(files) > 0 {
			return files[0]
		}
		return nil
	}
	if field.Kind == MultiValue || field.Kind == SplitDateTime {
		n := len(field.Fields)
		if field.Kind == SplitDateTime && n == 0 {
			n = 2
		}
		parts := make([]string, n)
		present := false
		for i := range parts {
			k := fmt.Sprintf("%s_%d", name, i)
			parts[i] = f.data.Get(k)
			if _, ok := f.data[k]; ok {
				present = true
			}
		}
		if !present {
			return nil
		}
		return parts
	}
	v, ok := f.data[name]
	if !ok {
		return nil
	}
	switch field.Kind {
	case MultipleChoice, TypedMultipleChoice, ModelMultipleChoice:
		return v
	default:
		if len(v) > 0 {
			return v[len(v)-1]
		}
		return ""
	}
}
func (f *Form) fullClean() {
	f.once.Do(func() {
		if !f.bound {
			return
		}
		for _, field := range f.fields {
			raw := f.raw(field)
			v, err := field.clean(f.ctx, raw)
			if err != nil {
				f.AddError(field.Name, err)
			} else {
				f.cleaned[field.Name] = v
			}
			if !field.Disabled {
				initial, initialErr := field.toValue(f.ctx, f.initialValue(field))
				if initialErr != nil || err != nil || !reflect.DeepEqual(initial, v) {
					f.changed = append(f.changed, field.Name)
				}
			}
		}
		if f.clean != nil {
			if err := f.clean(f); err != nil {
				f.AddError(NonFieldErrors, err)
			}
		}
	})
}
func cloneValues(v url.Values) url.Values {
	r := url.Values{}
	for k, x := range v {
		r[k] = append([]string(nil), x...)
	}
	return r
}
func cloneMap(v map[string]any) map[string]any {
	r := make(map[string]any, len(v))
	for k, x := range v {
		r[k] = cloneValue(x)
	}
	return r
}
