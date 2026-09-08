package http

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
)

// ListViewOptions composes scoped, explicitly projected model rows with a named
// template. Nil Ordering uses the model order; an empty nonnil slice uses only
// the primary-key tie-breaker. This initial boundary has no count or cursor.
type ListViewOptions struct {
	TemplateViewOptions
	ModelReadOptions
	Ordering   []string
	Pagination pagination.Config
}

// DetailViewOptions requires an explicit decoder for every primary-key field.
// Key returns ErrInvalidLookup only for malformed route input; operational
// errors must remain distinct. No lookup ever falls back to an unscoped query.
type DetailViewOptions struct {
	TemplateViewOptions
	ModelReadOptions
	Key func(*http.Request) (map[string]any, error)
}

// NewListView creates a bounded GET/HEAD list. The template receives object_list
// and page navigation metadata, never raw models or automatically exposed keys.
// Offset pagination can drift under concurrent writes; it is not a snapshot.
func NewListView(options ListViewOptions) (http.Handler, error) {
	model, err := newGenericModel(options.ModelReadOptions)
	if err != nil {
		return nil, err
	}
	order, err := genericModelOrdering(model.schema, options.Ordering)
	if err != nil {
		return nil, err
	}
	config := options.Pagination
	if config.Mode == "" {
		config.Mode = pagination.PageNumber
	}
	if config.Mode != pagination.PageNumber && config.Mode != pagination.LimitOffset {
		return nil, ErrGenericConfiguration
	}
	paginator, err := pagination.New(config)
	if err != nil {
		return nil, ErrGenericConfiguration
	}
	renderer, err := newGenericTemplate(options.TemplateViewOptions)
	if err != nil {
		return nil, err
	}
	return newReadView(model.readOptions(options.ReadViewOptions), func(call *readViewCall) (readViewResult, error) {
		ctx := call.base.Context()
		query, err := model.query(ctx)
		if err != nil {
			return readViewResult{}, err
		}
		values, err := url.ParseQuery(call.rawQuery())
		if err != nil || !genericPaginationValues(values, config.Mode) {
			return readViewResult{response: genericStatusResponse(http.StatusBadRequest)}, nil
		}
		page, err := paginator.Parse(values)
		if err != nil {
			return readViewResult{response: genericStatusResponse(http.StatusBadRequest)}, nil
		}
		rows, err := model.readRows(ctx, query.OrderBy(order...).Limit(page.Size+1).Offset(page.Offset), page.Size+1)
		if err != nil {
			return readViewResult{}, err
		}
		hasMore := len(rows) > page.Size
		if hasMore {
			rows = rows[:page.Size]
		}
		objects, finalize, err := model.projectRows(ctx, rows, false)
		if err != nil {
			return readViewResult{}, err
		}
		next, previous := paginator.Links(values, page, hasMore)
		response, err := renderer.renderWith(call, templates.Context{
			"object_list": objects,
			"page": map[string]any{
				"size": page.Size, "offset": page.Offset,
				"has_next": next != "", "has_previous": previous != "",
				"next": next, "previous": previous,
			},
		})
		return readViewResult{response: response, finalize: finalize}, err
	})
}

// NewDetailView creates a scoped GET/HEAD detail. Missing/denied objects return
// 404; duplicate identities and provider, decoding or completion failures do not.
func NewDetailView(options DetailViewOptions) (http.Handler, error) {
	key := options.Key
	if key == nil {
		return nil, ErrGenericConfiguration
	}
	model, err := newGenericModel(options.ModelReadOptions)
	if err != nil {
		return nil, err
	}
	renderer, err := newGenericTemplate(options.TemplateViewOptions)
	if err != nil {
		return nil, err
	}
	return newReadView(model.readOptions(options.ReadViewOptions), func(call *readViewCall) (readViewResult, error) {
		ctx := call.base.Context()
		query, err := model.query(ctx)
		if err != nil {
			return readViewResult{}, err
		}
		return model.detailResponse(call, renderer, key, query)
	})
}

// The caller supplies an already scoped query, optionally narrowed by a dated
// detail boundary. Identity decoding, cardinality, projection and final grants
// remain identical for both constructors.
func (model *genericModel) detailResponse(call *readViewCall, renderer *genericTemplate, key func(*http.Request) (map[string]any, error), query orm.Query[*models.MapRecord]) (readViewResult, error) {
	ctx := call.base.Context()
	identity := model.schema.PKFields()
	raw, err := key(call.request())
	if err != nil {
		if err == ErrInvalidLookup {
			return readViewResult{}, ErrNotFound
		}
		return readViewResult{}, ErrUnavailable
	}
	if len(raw) != len(identity) {
		return readViewResult{}, ErrNotFound
	}
	// Decode and freeze every key value before any later context/provider
	// callback. Only intrinsic scalar parsers run; never model validators.
	for _, field := range identity {
		value, err := decodeGenericLookupValue(field, raw[field.Name])
		if err != nil {
			return readViewResult{}, ErrNotFound
		}
		query = query.Filter(orm.Q(field.Name, value))
	}
	rows, err := model.readRows(ctx, query.OrderBy().Limit(2), 2)
	if err != nil {
		return readViewResult{}, err
	}
	if len(rows) == 0 {
		return readViewResult{}, ErrNotFound
	}
	if len(rows) != 1 {
		return readViewResult{}, ErrUnavailable
	}
	objects, finalize, err := model.projectRows(ctx, rows, true)
	if err != nil {
		return readViewResult{}, err
	}
	response, err := renderer.renderWith(call, templates.Context{"object": objects[0]})
	return readViewResult{response: response, finalize: finalize}, err
}

func genericPaginationValues(values url.Values, mode pagination.Mode) bool {
	for key, items := range values {
		if len(items) != 1 {
			return false
		}
		if mode == pagination.PageNumber && (key == "page" || key == "page_size") {
			continue
		}
		if mode == pagination.LimitOffset && (key == "limit" || key == "offset") {
			continue
		}
		return false
	}
	return true
}

func genericModelOrdering(schema models.Schema, requested []string) ([]string, error) {
	if requested == nil {
		requested = schema.Ordering
	}
	if len(requested) > 64 {
		return nil, ErrGenericConfiguration
	}
	order := append([]string(nil), requested...)
	seen := make(map[string]bool, len(order))
	for _, value := range order {
		name := strings.TrimPrefix(value, "-")
		field, ok := schema.Field(name)
		if !ok || !models.ValidIdentifier(name) || seen[name] || !genericModelFieldSupported(field, true) {
			return nil, ErrGenericConfiguration
		}
		seen[name] = true
	}
	for _, field := range schema.PKFields() {
		if !seen[field.Name] {
			order = append(order, field.Name)
		}
	}
	return order, nil
}
