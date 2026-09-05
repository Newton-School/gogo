package forms

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
)

// FormSetOptions bounds allocation before any submitted row is inspected.
type FormSetOptions struct {
	Prefix                            string
	Minimum, Maximum, AbsoluteMaximum int
	CanDelete, CanOrder               bool
	Initial                           []map[string]any
	// ExistingIDs is the complete server-scoped set of editable existing rows.
	ExistingIDs   []string
	IdentityField string
	CanDeleteRow  func(context.Context, string) error
	// FieldsForRow can apply trusted request-scoped permissions to a row's
	// declarations before binding. Submitted values never define declarations.
	FieldsForRow func(context.Context, int, string, []Field) ([]Field, error)
	Clean        func(*FormSet) error
}
type FormSet struct {
	Forms        []*Form
	Deleted      []int
	Ordered      []int
	Errors       ErrorList
	options      FormSetOptions
	ctx          context.Context
	initialCount int
	valid        bool
}

func BindFormSet(ctx context.Context, fields []Field, values url.Values, options FormSetOptions) (*FormSet, error) {
	if ctx == nil {
		return nil, errors.New("forms: nil context")
	}
	if options.Prefix == "" {
		options.Prefix = "form"
	}
	if options.AbsoluteMaximum == 0 {
		options.AbsoluteMaximum = 1000
	}
	if options.AbsoluteMaximum < 1 || options.AbsoluteMaximum > 1000 {
		return nil, errors.New("forms: absolute maximum must be between 1 and 1000")
	}
	if options.Maximum == 0 {
		options.Maximum = options.AbsoluteMaximum
	}
	if options.Minimum < 0 || options.Maximum < options.Minimum || options.Maximum > options.AbsoluteMaximum {
		return nil, errors.New("forms: invalid formset limits")
	}
	if options.IdentityField == "" {
		options.IdentityField = "id"
	}
	set := &FormSet{options: options, ctx: ctx, valid: true}
	fail := func(code, message string) (*FormSet, error) {
		set.valid = false
		set.Errors = append(set.Errors, Error{code, message})
		return set, nil
	}
	count := func(name string) (int, error) {
		v := values[options.Prefix+"-"+name]
		if len(v) != 1 {
			return 0, errors.New("missing count")
		}
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 0 {
			return 0, errors.New("invalid count")
		}
		return n, nil
	}
	total, err := count("TOTAL_FORMS")
	if err != nil {
		return fail("management", "Management form data is missing or invalid.")
	}
	initial, err := count("INITIAL_FORMS")
	if err != nil || initial > total || initial != len(options.Initial) || total > options.AbsoluteMaximum {
		return fail("management", "Management form data is missing or invalid.")
	}
	set.initialCount = initial
	if len(options.ExistingIDs) > 0 && len(options.ExistingIDs) != initial {
		return nil, errors.New("forms: existing IDs must match initial forms")
	}
	known := map[string]bool{}
	initialByID := map[string]map[string]any{}
	for i, id := range options.ExistingIDs {
		if id == "" || known[id] {
			return nil, errors.New("forms: duplicate or missing configured row identity")
		}
		known[id] = true
		initialByID[id] = options.Initial[i]
	}
	seen := map[string]bool{}
	type ordering struct{ index, order int }
	orders := []ordering{}
	retained := 0
	for i := 0; i < total; i++ {
		prefix := fmt.Sprintf("%s-%d", options.Prefix, i)
		id := values.Get(prefix + "-" + options.IdentityField)
		if options.ExistingIDs != nil {
			if (i < initial && (!known[id] || seen[id])) || (i >= initial && id != "") {
				return fail("identity", "An object is missing, duplicated, or outside this formset.")
			}
			if i < initial {
				seen[id] = true
			}
		}
		var initialData map[string]any
		if i < initial {
			initialData = options.Initial[i]
			if len(options.ExistingIDs) > 0 {
				initialData = initialByID[id]
			}
		}
		rowFields := fields
		if options.FieldsForRow != nil {
			rowFields = make([]Field, len(fields))
			for i, field := range fields {
				rowFields[i] = field.Clone()
			}
			rowFields, err = options.FieldsForRow(ctx, i, id, rowFields)
			if err != nil {
				return nil, err
			}
		}
		f, err := New(rowFields, WithData(values), WithInitial(initialData), WithPrefix(prefix), WithContext(ctx))
		if err != nil {
			return nil, err
		}
		set.Forms = append(set.Forms, f)
		deleting := options.CanDelete && values.Get(prefix+"-DELETE") != "" && values.Get(prefix+"-DELETE") != "false" && values.Get(prefix+"-DELETE") != "0"
		if deleting {
			if options.CanDeleteRow != nil {
				if err := options.CanDeleteRow(ctx, id); err != nil {
					return fail("permission", "This row cannot be deleted.")
				}
			}
			set.Deleted = append(set.Deleted, i)
			continue
		}
		emptyExtra := true
		for _, field := range fields {
			if !empty(f.raw(field)) {
				emptyExtra = false
				break
			}
		}
		if i >= initial && emptyExtra {
			continue
		}
		retained++
		if !f.IsValid() {
			set.valid = false
		}
		order := i
		if options.CanOrder {
			if v := values.Get(prefix + "-ORDER"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					f.AddError("ORDER", Error{"invalid", "Enter a whole number."})
					set.valid = false
				} else {
					order = n
				}
			}
		}
		orders = append(orders, ordering{i, order})
	}
	if retained < options.Minimum || retained > options.Maximum {
		set.valid = false
		set.Errors = append(set.Errors, Error{"count", "The number of forms is outside the allowed limits."})
	}
	sort.SliceStable(orders, func(i, j int) bool { return orders[i].order < orders[j].order })
	for _, o := range orders {
		set.Ordered = append(set.Ordered, o.index)
	}
	if options.Clean != nil {
		if err := options.Clean(set); err != nil {
			set.valid = false
			set.Errors = append(set.Errors, errorValue(err))
		}
	}
	return set, nil
}
func (s *FormSet) IsValid() bool {
	if !s.valid || len(s.Errors) > 0 {
		return false
	}
	for _, i := range s.Ordered {
		if !s.Forms[i].IsValid() {
			return false
		}
	}
	return true
}

// Save delegates the entire validated collection to one caller-owned transaction.
func (s *FormSet) Save(atomic func(context.Context, func(context.Context) error) error, save func(context.Context, *Form, int, bool) error) error {
	if !s.IsValid() {
		return errors.New("forms: cannot save invalid formset")
	}
	if atomic == nil || save == nil {
		return errors.New("forms: atomic saver required")
	}
	return atomic(s.ctx, func(ctx context.Context) error {
		for _, i := range s.Ordered {
			if err := save(ctx, s.Forms[i], i, false); err != nil {
				return err
			}
		}
		for _, i := range s.Deleted {
			if err := save(ctx, s.Forms[i], i, true); err != nil {
				return err
			}
		}
		return nil
	})
}
