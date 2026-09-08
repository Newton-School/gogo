package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/urls"
)

// ErrOpenAPI reports incomplete, unsupported or oversized API metadata. A
// failed generation never returns a partial document or inspects sample data.
var ErrOpenAPI = errors.New("api: OpenAPI metadata is unavailable")

// OpenAPISecurity describes an explicitly configured outer authentication
// boundary. Type is public, bearer or session. Session requires the actual
// cookie name. This is a documentation assertion, not authentication middleware
// or a grant of access. Multiple declarations represent alternatives (OR).
type OpenAPISecurity struct {
	Type       string
	CookieName string
}

type ReadOpenAPIOptions struct {
	// At least one explicit declaration is required, including for public APIs.
	Security []OpenAPISecurity
}

// ReadOpenAPIRoutes binds the ordinary list/detail handlers and their schemas
// to the same private resource/serializer snapshot. It leaves Routes and the
// standalone handlers unchanged. Include the returned routes with urls.New;
// OpenAPI refuses unbound, altered, ambiguous or unsupported route contracts.
//
// Registration runs no application callbacks and performs no I/O. Custom output
// still requires Field.OutputSchema, whose assertion the application must honor.
// Backends, registries, policies and other executable providers remain trusted
// read-only application dependencies, not a sandbox or a database snapshot.
func (s *Resource) ReadOpenAPIRoutes(prefix, basename string, options ReadOpenAPIOptions) (routes []urls.Route, err error) {
	defer func() {
		if recover() != nil {
			routes, err = nil, ErrOpenAPI
		}
	}()
	if s == nil || s.config.Store == nil || s.config.Serializer == nil || s.filters == nil || s.paginator == nil {
		return nil, ErrOpenAPI
	}
	security, err := freezeReadSecurity(options.Security, s.config.AllowAnonymous)
	if err != nil {
		return nil, err
	}
	// NewResource owns these normalized filter/paginator/cursor objects. None
	// has an exported mutable handle. Store and serializers do, so copy those
	// handles before building closures; never retain the caller's *Resource.
	resource := *s
	store := *s.config.Store
	resource.config.Store = &store
	resource.config.SelectRelated = slices.Clone(s.config.SelectRelated)
	budget := outputSchemaBudget{nodes: outputSchemaMaxNodes, text: outputSchemaMaxBytes}
	resource.config.Serializer, err = freezeReadSerializer(s.config.Serializer, &budget, 0)
	if err != nil {
		return nil, err
	}
	// The surrounding OpenAPI document declares the dialect. Inline schemas
	// must not carry a standalone $schema at a non-resource-root subschema.
	schemaBudget := outputSchemaBudget{nodes: outputSchemaMaxNodes, text: outputSchemaMaxBytes}
	inline, err := schemaBudget.serializer(resource.config.Serializer, 0, resource.config.AllowField != nil)
	if err != nil {
		return nil, errors.Join(ErrOpenAPI, err)
	}
	output, err := json.Marshal(inline)
	if err != nil || len(output) > outputSchemaMaxBytes {
		return nil, errors.Join(ErrOpenAPI, ErrOutputSchema)
	}
	if _, err := resource.paginator.Configuration(); err != nil {
		return nil, ErrOpenAPI
	}
	routes, err = resource.Routes(prefix, basename)
	if err != nil {
		return nil, ErrOpenAPI
	}
	for i := range routes {
		routes[i].Handler = &readOpenAPIHandler{
			handler: routes[i].Handler, resource: &resource, suffix: "/" + routes[i].Pattern,
			list: i == 0, output: string(output), security: security,
		}
	}
	return routes, nil
}

type readOpenAPIHandler struct {
	handler  http.Handler
	resource *Resource
	suffix   string
	list     bool
	output   string
	security []OpenAPISecurity
}

func (h *readOpenAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

// Serializer fields and their model/output metadata are already privately
// frozen by New. Only Nested exposes another replaceable serializer handle.
// Copy the complete structural graph under one budget; never invoke codecs,
// validators, defaults, representation hooks or methods on metadata values.
func freezeReadSerializer(source *Serializer, budget *outputSchemaBudget, depth int) (*Serializer, error) {
	if source == nil || !budget.node(depth) || len(source.fields) > budget.nodes {
		return nil, ErrOpenAPI
	}
	result := *source
	result.fields = make([]Field, len(source.fields))
	for i, field := range source.fields {
		var err error
		result.fields[i], err = freezeReadField(field, budget, depth+1)
		if err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func freezeReadField(field Field, budget *outputSchemaBudget, depth int) (Field, error) {
	if !budget.node(depth) {
		return Field{}, ErrOpenAPI
	}
	if field.Nested != nil {
		var err error
		field.Nested, err = freezeReadSerializer(field.Nested, budget, depth+1)
		if err != nil {
			return Field{}, err
		}
	}
	if field.Element != nil {
		child, err := freezeReadField(*field.Element, budget, depth+1)
		if err != nil {
			return Field{}, err
		}
		field.Element = &child
	}
	return field, nil
}

func freezeReadSecurity(declarations []OpenAPISecurity, anonymous bool) ([]OpenAPISecurity, error) {
	if len(declarations) == 0 || len(declarations) > 8 {
		return nil, ErrOpenAPI
	}
	result := slices.Clone(declarations)
	seen := map[OpenAPISecurity]bool{}
	for _, item := range result {
		if seen[item] {
			return nil, ErrOpenAPI
		}
		seen[item] = true
		switch item.Type {
		case "public":
			if !anonymous || item.CookieName != "" {
				return nil, ErrOpenAPI
			}
		case "bearer":
			if item.CookieName != "" {
				return nil, ErrOpenAPI
			}
		case "session":
			if !openAPICookieName(item.CookieName) {
				return nil, ErrOpenAPI
			}
		default:
			return nil, ErrOpenAPI
		}
	}
	slices.SortFunc(result, func(a, b OpenAPISecurity) int {
		if n := strings.Compare(a.Type, b.Type); n != 0 {
			return n
		}
		return strings.Compare(a.CookieName, b.CookieName)
	})
	return result, nil
}

func openAPICookieName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
