// Package urls implements ordered, named, reversible URL configurations.
package urls

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var ErrReverse = errors.New("URL reverse failed")
var ErrNotFound = errors.New("URL not found")

type Converter struct {
	Pattern string
	Decode  func(string) (any, error)
	Encode  func(any) (string, error)
}
type Route struct {
	Pattern, Name, Namespace string
	Methods                  []string
	Handler                  http.Handler
	Children                 []Route
	Regex                    bool
}

func Path(pattern string, handler http.Handler, name string, methods ...string) Route {
	return Route{Pattern: pattern, Name: name, Handler: handler, Methods: methods}
}
func RePath(pattern string, handler http.Handler, name string, methods ...string) Route {
	r := Path(pattern, handler, name, methods...)
	r.Regex = true
	return r
}
func Include(prefix, namespace string, routes ...Route) Route {
	return Route{Pattern: prefix, Namespace: namespace, Children: routes}
}

type segment struct {
	literal, name string
	converter     Converter
}
type compiledRoute struct {
	route      Route
	expression *regexp.Regexp
	segments   []segment
	parameters []string
	converters map[string]Converter
}
type Router struct {
	routes []compiledRoute
	names  map[string]int
}
type Match struct {
	Name    string
	Pattern string
	Params  map[string]any
	Handler http.Handler
}
type matchKey struct{}

func Params(r *http.Request) map[string]any {
	m, _ := r.Context().Value(matchKey{}).(Match)
	out := map[string]any{}
	for k, v := range m.Params {
		out[k] = v
	}
	return out
}
func Param(r *http.Request, name string) any {
	m, _ := r.Context().Value(matchKey{}).(Match)
	return m.Params[name]
}
func Name(r *http.Request) string { m, _ := r.Context().Value(matchKey{}).(Match); return m.Name }

func Builtins() map[string]Converter {
	stringConverter := func(pattern string) Converter {
		return Converter{Pattern: pattern, Decode: func(s string) (any, error) { return s, nil }, Encode: func(v any) (string, error) {
			s, ok := v.(string)
			if !ok {
				return "", ErrReverse
			}
			return s, nil
		}}
	}
	return map[string]Converter{
		"str": stringConverter(`[^/]+`), "slug": stringConverter(`[-a-zA-Z0-9_]+`), "path": stringConverter(`.+`), "uuid": stringConverter(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`),
		"int": {Pattern: `[0-9]+`, Decode: func(s string) (any, error) { return strconv.ParseInt(s, 10, 64) }, Encode: func(v any) (string, error) {
			switch n := v.(type) {
			case int:
				if n >= 0 {
					return strconv.Itoa(n), nil
				}
			case int64:
				if n >= 0 {
					return strconv.FormatInt(n, 10), nil
				}
			case uint64:
				return strconv.FormatUint(n, 10), nil
			}
			return "", ErrReverse
		}},
	}
}
func New(routes ...Route) (*Router, error) { return NewWithConverters(Builtins(), routes...) }
func NewWithConverters(converters map[string]Converter, routes ...Route) (*Router, error) {
	for name, c := range converters {
		if !validIdentifier(name) || c.Pattern == "" || c.Decode == nil || c.Encode == nil {
			return nil, errors.New("invalid URL converter")
		}
		re, e := regexp.Compile("^(?:" + c.Pattern + ")$")
		if e != nil || re.NumSubexp() != 0 {
			return nil, errors.New("converter patterns must not contain captures")
		}
	}
	r := &Router{names: map[string]int{}}
	var add func(string, string, []Route) error
	add = func(prefix, namespace string, items []Route) error {
		for _, item := range items {
			if item.Namespace != "" {
				if namespace != "" {
					item.Namespace = namespace + ":" + item.Namespace
				}
			} else {
				item.Namespace = namespace
			}
			if len(item.Children) > 0 {
				if item.Handler != nil || item.Regex {
					return errors.New("invalid URL include")
				}
				if err := add(prefix+item.Pattern, item.Namespace, item.Children); err != nil {
					return err
				}
				continue
			}
			item.Pattern = prefix + item.Pattern
			if !strings.HasPrefix(item.Pattern, "/") {
				item.Pattern = "/" + item.Pattern
			}
			if item.Handler == nil {
				return errors.New("URL handler required")
			}
			if item.Name != "" && item.Namespace != "" {
				item.Name = item.Namespace + ":" + item.Name
			}
			if item.Name != "" {
				if _, ok := r.names[item.Name]; ok {
					return fmt.Errorf("duplicate URL name: %s", item.Name)
				}
				r.names[item.Name] = len(r.routes)
			}
			item.Methods = slices.Clone(item.Methods)
			for _, method := range item.Methods {
				if method == "" || method != strings.ToUpper(method) || strings.ContainsAny(method, " \t\r\n") {
					return errors.New("invalid HTTP method")
				}
			}
			compiled, err := compile(item, converters)
			if err != nil {
				return err
			}
			r.routes = append(r.routes, compiled)
		}
		return nil
	}
	if err := add("", "", routes); err != nil {
		return nil, err
	}
	return r, nil
}
func validIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
func compile(route Route, converters map[string]Converter) (compiledRoute, error) {
	c := compiledRoute{route: route, converters: map[string]Converter{}}
	var expression strings.Builder
	if route.Regex {
		re, err := regexp.Compile(route.Pattern)
		if err != nil {
			return c, err
		}
		c.expression = re
		for _, name := range re.SubexpNames() {
			if name != "" {
				if !validIdentifier(name) {
					return c, errors.New("invalid regex parameter")
				}
				c.parameters = append(c.parameters, name)
			}
		}
		return c, nil
	}
	expression.WriteString("^")
	rest := route.Pattern
	for rest != "" {
		idx := strings.IndexByte(rest, '<')
		if idx < 0 {
			expression.WriteString(regexp.QuoteMeta(rest))
			c.segments = append(c.segments, segment{literal: rest})
			break
		}
		literal := rest[:idx]
		if literal != "" {
			expression.WriteString(regexp.QuoteMeta(literal))
			c.segments = append(c.segments, segment{literal: literal})
		}
		end := strings.IndexByte(rest[idx:], '>')
		if end < 0 {
			return c, errors.New("unterminated URL parameter")
		}
		token := rest[idx+1 : idx+end]
		kind, name, ok := strings.Cut(token, ":")
		if !ok {
			name = kind
			kind = "str"
		}
		converter, ok := converters[kind]
		if !ok || !validIdentifier(name) {
			return c, errors.New("unknown URL converter or parameter")
		}
		if _, ok := c.converters[name]; ok {
			return c, errors.New("duplicate URL parameter")
		}
		c.converters[name] = converter
		c.parameters = append(c.parameters, name)
		c.segments = append(c.segments, segment{name: name, converter: converter})
		expression.WriteString("(?P<" + name + ">" + converter.Pattern + ")")
		rest = rest[idx+end+1:]
	}
	expression.WriteString("$")
	re, err := regexp.Compile(expression.String())
	c.expression = re
	return c, err
}
func (c compiledRoute) match(path string) (map[string]any, bool) {
	indices := c.expression.FindStringSubmatchIndex(path)
	if indices == nil || indices[0] != 0 {
		return nil, false
	}
	values := c.expression.FindStringSubmatch(path)
	params := map[string]any{}
	for _, name := range c.parameters {
		index := c.expression.SubexpIndex(name)
		raw := values[index]
		if converter, ok := c.converters[name]; ok {
			value, err := converter.Decode(raw)
			if err != nil {
				return nil, false
			}
			params[name] = value
		} else {
			params[name] = raw
		}
	}
	return params, true
}
func (r *Router) Resolve(path string) (Match, error) {
	for _, route := range r.routes {
		if params, ok := route.match(path); ok {
			return Match{route.route.Name, route.route.Pattern, params, route.route.Handler}, nil
		}
	}
	return Match{}, ErrNotFound
}
func (r *Router) Reverse(name string, params map[string]any, query url.Values) (string, error) {
	index, ok := r.names[name]
	if !ok {
		return "", ErrReverse
	}
	route := r.routes[index]
	if route.route.Regex || len(params) != len(route.parameters) {
		return "", ErrReverse
	}
	var path strings.Builder
	for _, s := range route.segments {
		if s.name == "" {
			path.WriteString(s.literal)
			continue
		}
		value, ok := params[s.name]
		if !ok {
			return "", ErrReverse
		}
		raw, err := s.converter.Encode(value)
		if err != nil {
			return "", ErrReverse
		}
		valid, _ := regexp.MatchString("^(?:"+s.converter.Pattern+")$", raw)
		if !valid || strings.ContainsAny(raw, "\r\n\\") {
			return "", ErrReverse
		}
		parts := strings.Split(raw, "/")
		for i, part := range parts {
			if part == "." || part == ".." {
				return "", ErrReverse
			}
			parts[i] = url.PathEscape(part)
		}
		path.WriteString(strings.Join(parts, "/"))
	}
	out := path.String()
	if strings.HasPrefix(out, "//") {
		return "", ErrReverse
	}
	if q := query.Encode(); q != "" {
		out += "?" + q
	}
	return out, nil
}
func (r *Router) Routes() []Route {
	out := make([]Route, len(r.routes))
	for i, c := range r.routes {
		out[i] = c.route
		out[i].Methods = slices.Clone(c.route.Methods)
		out[i].Children = nil
	}
	return out
}
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	allow := []string{}
	for _, route := range r.routes {
		params, ok := route.match(req.URL.Path)
		if !ok {
			continue
		}
		methods := route.route.Methods
		permitted := len(methods) == 0 || slices.Contains(methods, req.Method) || req.Method == "HEAD" && slices.Contains(methods, "GET")
		if !permitted {
			allow = append(allow, methods...)
			continue
		}
		match := Match{route.route.Name, route.route.Pattern, params, route.route.Handler}
		req = req.WithContext(context.WithValue(req.Context(), matchKey{}, match))
		if req.Method == "HEAD" {
			w = headWriter{w}
		}
		route.route.Handler.ServeHTTP(w, req)
		return
	}
	if len(allow) > 0 {
		if slices.Contains(allow, "GET") {
			allow = append(allow, "HEAD")
		}
		allow = append(allow, "OPTIONS")
		slices.Sort(allow)
		allow = slices.Compact(allow)
		w.Header().Set("Allow", strings.Join(allow, ", "))
		if req.Method == "OPTIONS" {
			w.WriteHeader(204)
		} else {
			http.Error(w, "method not allowed", 405)
		}
		return
	}
	http.NotFound(w, req)
}

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(p []byte) (int, error) { return len(p), nil }
