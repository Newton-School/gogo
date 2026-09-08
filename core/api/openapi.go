package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/urls"
)

const openAPIMaxBytes = 8 << 20

type OpenAPIOptions struct {
	// Title and Version are explicit public product metadata, never inferred
	// from model names, settings, environment variables or a request Host.
	Title, Version string
}

// OpenAPI returns a deterministic OpenAPI 3.1.1 document for a selected API
// router made with urls.New and Resource.ReadOpenAPIRoutes. All its routes must
// have sealed read contracts. Generate for the API router, not an unrelated
// whole-site router; missing/custom/mutation metadata fails, never disappears.
//
// This initial generator supports literal Include prefixes, list/detail GET,
// implicit HEAD and the router's bodyless OPTIONS. Regex/custom converters,
// altered methods, overlapping paths and opaque handler wrappers are rejected.
// Authentication declarations describe middleware outside this router; they do
// not install it or claim that outside middleware's response bodies are known.
// No route, authorization, model, codec or representation callback is executed.
//
// Projection is frozen before context callbacks, limited to 256 routes, 256
// parameters per operation, and 8 MiB of schema data/encoded output. Cancellation
// returns no partial bytes. This emits bytes only: no server, file, client code
// or management command is created, and no remote schema is fetched.
func OpenAPI(ctx context.Context, router *urls.Router, options OpenAPIOptions) (document []byte, err error) {
	defer func() {
		if recover() != nil {
			document, err = nil, ErrOpenAPI
		}
	}()
	value, buildErr := buildOpenAPI(router, options)
	if err := outputSchemaContextError(ctx); err != nil {
		return nil, openAPIContextError(err)
	}
	if buildErr != nil {
		return nil, buildErr
	}
	document, err = json.Marshal(value)
	if err != nil || len(document) > openAPIMaxBytes {
		return nil, ErrOpenAPI
	}
	if err := outputSchemaContextError(ctx); err != nil {
		return nil, openAPIContextError(err)
	}
	return document, nil
}

func openAPIContextError(err error) error {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	return ErrOpenAPI
}

type openAPIDocument struct {
	OpenAPI           string                                 `json:"openapi"`
	JSONSchemaDialect string                                 `json:"jsonSchemaDialect"`
	Info              openAPIInfo                            `json:"info"`
	Paths             map[string]map[string]openAPIOperation `json:"paths"`
	Components        openAPIComponents                      `json:"components"`
}
type openAPIInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}
type openAPIComponents struct {
	SecuritySchemes map[string]openAPIScheme `json:"securitySchemes,omitempty"`
}
type openAPIScheme struct {
	Type   string `json:"type"`
	Scheme string `json:"scheme,omitempty"`
	In     string `json:"in,omitempty"`
	Name   string `json:"name,omitempty"`
}
type openAPIOperation struct {
	ID          string                     `json:"operationId"`
	Description string                     `json:"description,omitempty"`
	Parameters  []openAPIParameter         `json:"parameters,omitempty"`
	Security    []map[string][]string      `json:"security"`
	Responses   map[string]openAPIResponse `json:"responses"`
}
type openAPIParameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      any    `json:"schema"`
	Style       string `json:"style,omitempty"`
	Explode     *bool  `json:"explode,omitempty"`
}
type openAPIResponse struct {
	Description string                   `json:"description"`
	Content     map[string]openAPIMedia  `json:"content,omitempty"`
	Headers     map[string]openAPIHeader `json:"headers,omitempty"`
}
type openAPIMedia struct {
	Schema any `json:"schema"`
}
type openAPIHeader struct {
	Schema any `json:"schema"`
}

func buildOpenAPI(router *urls.Router, options OpenAPIOptions) (openAPIDocument, error) {
	if !openAPIText(options.Title, 256) || !openAPIText(options.Version, 128) {
		return openAPIDocument{}, ErrOpenAPI
	}
	metadata, err := router.Describe(256)
	if err != nil || !metadata.BuiltinConverters || len(metadata.Routes) == 0 {
		return openAPIDocument{}, ErrOpenAPI
	}
	result := openAPIDocument{OpenAPI: "3.1.1", JSONSchemaDialect: outputSchemaDialect,
		Info: openAPIInfo{options.Title, options.Version}, Paths: map[string]map[string]openAPIOperation{},
		Components: openAPIComponents{SecuritySchemes: map[string]openAPIScheme{}},
	}
	paths, ids := []string{}, map[string]bool{}
	remaining := openAPIMaxBytes
	for _, route := range metadata.Routes {
		handler, ok := route.Handler.(*readOpenAPIHandler)
		if !ok || handler == nil || handler.resource == nil || route.Regex || !openAPIName(route.Name) || len(route.Methods) != 1 || route.Methods[0] != "GET" {
			return openAPIDocument{}, ErrOpenAPI
		}
		path, err := openAPIPath(route, handler)
		if err != nil {
			return openAPIDocument{}, err
		}
		for _, previous := range paths {
			if openAPIPathsOverlap(path, previous) {
				return openAPIDocument{}, ErrOpenAPI
			}
		}
		paths = append(paths, path)
		// Inline schemas occur only on GET. Charge before constructing further
		// envelopes; opaque output bytes are privately generated and immutable.
		remaining -= len(handler.output) + len(path) + len(route.Name)
		if remaining < 0 {
			return openAPIDocument{}, ErrOpenAPI
		}
		parameters, err := handler.parameters()
		if err != nil || len(parameters) > 256 {
			return openAPIDocument{}, ErrOpenAPI
		}
		security := make([]map[string][]string, 0, len(handler.security))
		for _, declaration := range handler.security {
			schemeName, scheme := openAPISecurityScheme(declaration)
			if schemeName == "" {
				security = append(security, map[string][]string{})
				continue
			}
			if existing, exists := result.Components.SecuritySchemes[schemeName]; exists && existing != scheme {
				return openAPIDocument{}, ErrOpenAPI
			}
			result.Components.SecuritySchemes[schemeName] = scheme
			security = append(security, map[string][]string{schemeName: {}})
		}
		operations := map[string]openAPIOperation{}
		for _, method := range []string{"get", "head", "options"} {
			id := route.Name + "." + method
			if ids[id] {
				return openAPIDocument{}, ErrOpenAPI
			}
			ids[id] = true
			operation := openAPIOperation{ID: id, Parameters: parameters, Security: security, Responses: handler.responses(method)}
			if method == "options" {
				operation.Security = []map[string][]string{}
				operation.Parameters = nil
				if !handler.list {
					operation.Parameters = []openAPIParameter{openAPIPrimaryKey()}
				}
				operation.Description = "Router method discovery only; resource authorization and queries do not run. Outer middleware may impose additional access checks."
			}
			operations[method] = operation
		}
		result.Paths[path] = operations
	}
	return result, nil
}

func openAPISecurityScheme(declaration OpenAPISecurity) (string, openAPIScheme) {
	switch declaration.Type {
	case "bearer":
		return "bearer", openAPIScheme{Type: "http", Scheme: "bearer"}
	case "session":
		digest := sha256.Sum256([]byte(declaration.CookieName))
		return "session_" + hex.EncodeToString(digest[:]), openAPIScheme{Type: "apiKey", In: "cookie", Name: declaration.CookieName}
	default:
		return "", openAPIScheme{}
	}
}

func openAPIText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func openAPIName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.:-", c) {
			continue
		}
		return false
	}
	return true
}

func openAPIPath(route urls.Route, handler *readOpenAPIHandler) (string, error) {
	if !strings.HasSuffix(route.Pattern, handler.suffix) || len(route.Pattern) > 2048 || strings.ContainsAny(route.Pattern, "{}") {
		return "", ErrOpenAPI
	}
	path := route.Pattern
	if !handler.list {
		if strings.HasSuffix(path, "/<str:pk>/") {
			path = strings.TrimSuffix(path, "<str:pk>/") + "{pk}/"
		} else if strings.HasSuffix(path, "/<uuid:pk>/") {
			path = strings.TrimSuffix(path, "<uuid:pk>/") + "{pk}/"
		} else {
			return "", ErrOpenAPI
		}
	}
	if !strings.HasPrefix(path, "/") || !strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return "", ErrOpenAPI
	}
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "{pk}" && !handler.list {
			continue
		}
		if part == "" || part == "." || part == ".." {
			return "", ErrOpenAPI
		}
		for _, c := range part {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c) {
				continue
			}
			return "", ErrOpenAPI
		}
	}
	return path, nil
}

func openAPIPathsOverlap(a, b string) bool {
	left, right := strings.Split(a, "/"), strings.Split(b, "/")
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] && left[i] != "{pk}" && right[i] != "{pk}" {
			return false
		}
	}
	return true
}

func openAPIPrimaryKey() openAPIParameter {
	return openAPIParameter{Name: "pk", In: "path", Required: true, Schema: map[string]any{"type": "string", "minLength": 1}, Description: "Route text decoded and validated against the scoped primary key; not a JSON model value."}
}

func (h *readOpenAPIHandler) parameters() ([]openAPIParameter, error) {
	if !h.list {
		result := []openAPIParameter{openAPIPrimaryKey()}
		if h.resource.config.EntityTags {
			result = append(result, openAPIParameter{Name: "If-None-Match", In: "header", Schema: map[string]any{"type": "string"}, Description: "Strong or weak entity-tag condition; authorization and representation still execute."})
		}
		return result, nil
	}
	config, err := h.resource.paginator.Configuration()
	if err != nil || len(h.resource.filters.filters) > 240 || len(h.resource.filters.ordering) > 64 {
		return nil, ErrOpenAPI
	}
	for _, name := range h.resource.filters.ordering {
		if !openAPIName(name) {
			return nil, ErrOpenAPI
		}
	}
	var result []openAPIParameter
	number := func(name string, minimum, maximum, defaultValue int) {
		result = append(result, openAPIParameter{Name: name, In: "query", Schema: map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum, "default": defaultValue}})
	}
	switch config.Mode {
	case pagination.PageNumber:
		number("page", 1, config.MaxOffset+1, 1)
		number("page_size", 1, config.MaxSize, config.DefaultSize)
		result[0].Description = "One-based page; (page - 1) * selected page_size must also fit the configured offset bound."
	case pagination.LimitOffset:
		number("limit", 1, config.MaxSize, config.DefaultSize)
		number("offset", 0, config.MaxOffset, 0)
	case pagination.CursorMode:
		number("page_size", 1, config.MaxSize, config.DefaultSize)
		result = append(result, openAPIParameter{Name: "cursor", In: "query", Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": pagination.MaxCursorBytes}, Description: "Opaque signed cursor bound to this resource, query and current visibility scope."})
	default:
		return nil, ErrOpenAPI
	}
	if len(h.resource.filters.search) > 0 {
		result = append(result, openAPIParameter{Name: "search", In: "query", Schema: map[string]any{"type": "string", "maxLength": 256}, Description: "At most sixteen whitespace-separated terms, ANDed across the declared searchable fields."})
	}
	if len(h.resource.filters.ordering) > 0 {
		result = append(result, openAPIParameter{Name: "ordering", In: "query", Schema: map[string]any{"type": "string", "maxLength": 4096}, Description: "Up to sixteen comma-separated fields with optional '-' descending prefix. Allowed: " + strings.Join(h.resource.filters.ordering, ", ") + ". Unique key tie-breakers are appended."})
	}
	for name, filter := range h.resource.filters.filters {
		parameter := openAPIParameter{Name: name, In: "query", Schema: map[string]any{"type": "string", "maxLength": 4096}, Description: "A typed filter operand; model output null/blank rules are not query-input rules."}
		if filter.Lookup == "in" || filter.Lookup == "range" {
			minimum, maximum := 1, 100
			if filter.Lookup == "range" {
				minimum, maximum = 2, 2
			}
			parameter.Schema = map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 4096}, "minItems": minimum, "maxItems": maximum}
			parameter.Style = "form"
			yes := true
			parameter.Explode = &yes
			parameter.Description = "Repeated query parameters, not comma-separated values."
		} else if filter.Lookup == "isnull" {
			parameter.Schema = map[string]any{"type": "string", "enum": []string{"true", "false"}}
		}
		result = append(result, parameter)
	}
	slices.SortFunc(result, func(a, b openAPIParameter) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

func (h *readOpenAPIHandler) responses(method string) map[string]openAPIResponse {
	text := map[string]any{"type": "string"}
	if method == "options" {
		return map[string]openAPIResponse{"204": {Description: "Available methods; no resource callback is executed.", Headers: map[string]openAPIHeader{"Allow": {Schema: text}}}}
	}
	errors := openAPIResponse{Description: "Public API error; routing failures can use plain text. External middleware responses are outside this router contract."}
	success := openAPIResponse{Description: "Successful authorized representation."}
	if method != "head" {
		var schema any = json.RawMessage(h.output)
		if h.list {
			properties := map[string]any{"results": map[string]any{"type": "array", "items": schema}, "next": text}
			required := []string{"results"}
			if h.resource.cursor == nil {
				properties["previous"] = text
			}
			if h.resource.config.IncludeCount {
				properties["count"] = map[string]any{"type": "integer"}
				required = append(required, "count")
			}
			schema = map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
		}
		success.Content = map[string]openAPIMedia{"application/json": {Schema: schema}}
		errorSchema := map[string]any{"type": "object", "required": []string{"code", "detail"}, "additionalProperties": false, "properties": map[string]any{
			"code": text, "detail": text, "request_id": text,
			"fields": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": []string{"array", "null"}, "items": text}},
		}}
		errors.Content = map[string]openAPIMedia{"application/json": {Schema: errorSchema}, "text/plain": {Schema: text}}
	}
	result := map[string]openAPIResponse{"200": success, "default": errors}
	if !h.list && h.resource.config.EntityTags {
		success.Headers = map[string]openAPIHeader{"ETag": {Schema: text}}
		result["200"] = success
		result["304"] = openAPIResponse{Description: "Representation unchanged after current authorization.", Headers: success.Headers}
	}
	return result
}
