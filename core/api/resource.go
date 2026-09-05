package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/urls"
)

type ResourceConfig struct {
	Store *orm.Store
	// Model is the exact registered app.Model key. Registration never exposes
	// a resource: all three policy, scope and serializer contracts are required.
	Model      string
	Serializer *Serializer
	Policy     auth.Policy
	// Scope must encode ALL countable/listable row visibility, including object
	// rules, before pagination. Object/field callbacks are defense-in-depth;
	// they cannot remove hidden rows from an aggregate or navigation metadata.
	Scope          func(context.Context, auth.Principal, models.Schema) (db.Predicate, error)
	AllowAnonymous bool
	// AllowField runs before reading/computing each declared output field.
	// It may narrow the serializer allowlist, never add fields. Nested output
	// still requires its own explicit serializer and representation policy.
	AllowField    func(context.Context, auth.Principal, models.Record, string) (bool, error)
	Filters       FilterConfig
	Pagination    pagination.Config
	IncludeCount  bool
	SelectRelated []string
}

// Resource currently exposes read-only list/detail handlers. Authentication is
// supplied by an explicit surrounding session/bearer/application middleware;
// handlers never trust identity fields in a body, query, or header themselves.
// Permission callbacks and the ORM store are trusted application dependencies.
type Resource struct {
	config    ResourceConfig
	schema    models.Schema
	filters   *FilterBackend
	paginator *pagination.Paginator
}

func NewResource(config ResourceConfig) (*Resource, error) {
	if config.Store == nil || config.Store.Backend == nil || config.Store.Registry == nil || config.Serializer == nil || config.Policy == nil || config.Scope == nil {
		return nil, errors.New("api: resource requires store, registered model, serializer, policy and scope")
	}
	schema, ok := config.Store.Registry.Get(config.Model)
	if !ok {
		return nil, errors.New("api: resource model is not registered")
	}
	filters, err := NewFilterBackend(schema, config.Filters)
	if err != nil {
		return nil, err
	}
	paginator, err := pagination.New(config.Pagination)
	if err != nil {
		return nil, err
	}
	config.Policy = auth.ConstrainPolicy(config.Policy)
	config.SelectRelated = slices.Clone(config.SelectRelated)
	if len(config.SelectRelated) > 16 {
		return nil, errors.New("api: too many eager relation paths")
	}
	for _, path := range config.SelectRelated {
		parts := strings.Split(path, "__")
		if len(parts) > 8 {
			return nil, errors.New("api: invalid eager relation path")
		}
		for _, part := range parts {
			if !models.ValidIdentifier(part) {
				return nil, errors.New("api: invalid eager relation path")
			}
		}
	}
	return &Resource{config: config, schema: schema, filters: filters, paginator: paginator}, nil
}

// Routes returns Django-style named patterns for inclusion in an app urls.go.
// Custom routes can reuse ListHandler/DetailHandler with explicit converters.
// Composite-key models use DetailHandler with an application key decoder.
func (s *Resource) Routes(prefix, basename string) ([]urls.Route, error) {
	if s == nil || !models.ValidIdentifier(basename) || prefix == "" || strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "<>?#\\\x00") {
		return nil, errors.New("api: invalid resource route")
	}
	keys := s.schema.PKFields()
	if len(keys) != 1 {
		return nil, errors.New("api: composite resource keys require an explicit detail decoder")
	}
	converter := "str"
	if keys[0].Kind == models.UUID {
		converter = "uuid"
	}
	routes := []urls.Route{urls.Path(prefix, s.ListHandler(), basename+"_list", "GET"), urls.Path(prefix+"<"+converter+":pk>/", s.DetailHandler(func(r *http.Request) (Values, error) { return Values{keys[0].Name: urls.Param(r, "pk")}, nil }), basename+"_detail", "GET")}
	if _, err := urls.New(routes...); err != nil {
		return nil, err
	}
	return routes, nil
}

type resourceRequest struct {
	ctx   context.Context
	query orm.Query[*models.MapRecord]
}

func (s *Resource) request(r *http.Request) (resourceRequest, error) {
	if s == nil {
		return resourceRequest{}, ghttp.ErrUnavailable
	}
	ctx := r.Context()
	if err := ctx.Err(); err != nil {
		return resourceRequest{}, err
	}
	p := auth.FromContext(ctx)
	if !p.Authenticated && !s.config.AllowAnonymous {
		return resourceRequest{}, auth.ErrUnauthenticated
	}
	if p.Authenticated && !p.Active {
		return resourceRequest{}, auth.ErrPermissionDenied
	}
	if err := s.config.Policy.Authorize(ctx, p, "view", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name}); err != nil {
		return resourceRequest{}, err
	}
	if err := ctx.Err(); err != nil {
		return resourceRequest{}, err
	}
	if _, err := Negotiate(strings.Join(r.Header.Values("Accept"), ","), "application/json"); err != nil {
		return resourceRequest{}, err
	}
	// Materialize the mandatory root scope before parsing filters/counts. Keep
	// each schema's predicate stable for this request, including joined targets.
	root, err := s.config.Scope(ctx, auth.FromContext(ctx), s.schema.Clone())
	if err != nil {
		return resourceRequest{}, err
	}
	if err := ctx.Err(); err != nil {
		return resourceRequest{}, err
	}
	scopes := map[string]db.Predicate{s.schema.Key(): root}
	scope := func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		if err := ctx.Err(); err != nil {
			return db.Predicate{}, err
		}
		if predicate, ok := scopes[schema.Key()]; ok {
			return predicate, nil
		}
		predicate, err := s.config.Scope(ctx, auth.FromContext(ctx), schema.Clone())
		if err != nil {
			return db.Predicate{}, err
		}
		scopes[schema.Key()] = predicate
		return predicate, nil
	}
	factory := func() *models.MapRecord { record, _ := models.NewRecord(s.schema); return record }
	query := orm.For(s.config.Store, factory).WithScope(scope).SelectRelated(s.config.SelectRelated...)
	return resourceRequest{ctx: ctx, query: query}, ctx.Err()
}

func readQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 16<<10 {
		return nil, invalidQuery()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalidQuery()
	}
	return values, nil
}

type Collection struct {
	Results  []Values `json:"results"`
	Count    *int64   `json:"count,omitempty"`
	Next     string   `json:"next,omitempty"`
	Previous string   `json:"previous,omitempty"`
}

func (s *Resource) ListHandler() http.Handler {
	return s.readHandler(func(r *http.Request) (any, error) {
		request, err := s.request(r)
		if err != nil {
			return nil, err
		}
		values, err := readQuery(r)
		if err != nil {
			return nil, err
		}
		options, err := s.filters.Parse(request.ctx, values)
		if err != nil {
			return nil, err
		}
		page, err := s.paginator.Parse(values)
		if err != nil {
			return nil, mediaError(400, "INVALID_PAGINATION", "Invalid pagination parameters")
		}
		query := request.query.Filter(options.Predicate).OrderBy(options.Ordering...)
		result := Collection{Results: []Values{}}
		if s.config.IncludeCount {
			count, err := query.Count(request.ctx)
			if err != nil {
				return nil, err
			}
			result.Count = &count
		}
		rows, err := query.Limit(page.Size + 1).Offset(page.Offset).All(request.ctx)
		if err != nil {
			return nil, err
		}
		hasMore := len(rows) > page.Size
		if hasMore {
			rows = rows[:page.Size]
		}
		for _, row := range rows {
			value, err := s.represent(request, row, false)
			if err != nil {
				return nil, err
			}
			result.Results = append(result.Results, value)
		}
		result.Next, result.Previous = s.paginator.Links(values, page, hasMore)
		return result, request.ctx.Err()
	})
}

// ErrInvalidKey is returned by an application decoder for malformed route
// input. Provider/context errors must retain their actual failure class.
var ErrInvalidKey = errors.New("api: invalid resource route key")

// DetailHandler accepts an explicit route-key decoder. It must return exactly
// every primary-key field; missing, extra and malformed keys become safe 404s.
// Return ErrInvalidKey for malformed input, not for a provider failure.
func (s *Resource) DetailHandler(key func(*http.Request) (Values, error)) http.Handler {
	return s.readHandler(func(r *http.Request) (any, error) {
		request, err := s.request(r)
		if err != nil {
			return nil, err
		}
		queryValues, err := readQuery(r)
		if err != nil {
			return nil, err
		}
		if len(queryValues) > 0 {
			return nil, invalidQuery()
		}
		if key == nil {
			return nil, ghttp.ErrUnavailable
		}
		values, err := key(r)
		if request.ctx.Err() != nil {
			return nil, request.ctx.Err()
		}
		if err != nil {
			if errors.Is(err, ErrInvalidKey) {
				return nil, ghttp.ErrNotFound
			}
			return nil, err
		}
		fields := s.schema.PKFields()
		if len(values) != len(fields) {
			return nil, ghttp.ErrNotFound
		}
		var predicates []db.Predicate
		for _, field := range fields {
			raw, ok := values[field.Name]
			if !ok || raw == nil {
				return nil, ghttp.ErrNotFound
			}
			value, err := cleanQueryValue(request.ctx, field, raw)
			if err != nil {
				if request.ctx.Err() != nil {
					return nil, request.ctx.Err()
				}
				return nil, ghttp.ErrNotFound
			}
			predicates = append(predicates, orm.Q(field.Name, value))
		}
		row, err := request.query.Filter(predicates...).Get(request.ctx)
		if err != nil {
			return nil, err
		}
		return s.represent(request, row, true)
	})
}

func (s *Resource) represent(request resourceRequest, record models.Record, hidden bool) (Values, error) {
	key := Values{}
	for _, field := range s.schema.PKFields() {
		value, err := record.Get(field.Name)
		if err != nil {
			return nil, err
		}
		key[field.Name] = value
	}
	if err := s.config.Policy.Authorize(request.ctx, auth.FromContext(request.ctx), "view", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name, ID: key, Object: record}); err != nil {
		if hidden && (errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, auth.ErrUnauthenticated)) {
			return nil, ghttp.ErrNotFound
		}
		return nil, err
	}
	serializer := *s.config.Serializer
	if s.config.AllowField != nil {
		serializer.fields = nil
		for _, field := range s.config.Serializer.fields {
			if field.Hidden || field.WriteOnly {
				continue
			}
			allowed, err := s.config.AllowField(request.ctx, auth.FromContext(request.ctx), record, field.Name)
			if err != nil {
				return nil, err
			}
			if allowed {
				serializer.fields = append(serializer.fields, field)
			}
		}
	}
	return serializer.Representation(request.ctx, record)
}

func (s *Resource) readHandler(read func(*http.Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", "Accept, Authorization, Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			ghttp.WriteError(w, r, mediaError(405, "METHOD_NOT_ALLOWED", "Method not allowed"))
			return
		}
		// No response bytes are emitted before all queries, policies, projection
		// and JSON encoding finish. Provider panic values are never logged here.
		response, err := readResponse(r, read)
		if err != nil {
			ghttp.WriteError(w, r, err)
			return
		}
		// A failed network write may already have emitted bytes. Never append
		// a second JSON document/error to a partial response.
		_ = response.Write(w, r)
	})
}

func readResponse(r *http.Request, read func(*http.Request) (any, error)) (response ghttp.Response, err error) {
	defer func() {
		if recover() != nil {
			response = ghttp.Response{}
			err = ghttp.ErrUnavailable
		}
	}()
	value, err := read(r)
	if err != nil {
		return ghttp.Response{}, err
	}
	if err := r.Context().Err(); err != nil {
		return ghttp.Response{}, err
	}
	response, err = ghttp.JSON(http.StatusOK, value)
	if err == nil {
		err = r.Context().Err()
	}
	return response, err
}
