package http

import (
	"context"
	"io/fs"
	"net/http"
	"path"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/templates"
)

// TemplateViewOptions composes a named template with read-only request hooks.
// Context precedence, lowest first, is processors, captured route parameters,
// ExtraContext and Context. No request, principal, model or database is exposed
// to templates automatically: the application selects public context fields.
type TemplateViewOptions struct {
	ReadViewOptions
	Templates      templates.Config
	TemplateName   string
	Context        func(*http.Request) (templates.Context, error)
	ExtraContext   templates.Context
	MaxOutputBytes int
}

// NewTemplateView creates a reusable GET/HEAD handler with a private engine.
// Template sources and extensions are trusted application code. Context strings
// are values, not executable template source; SafeHTML is a trust assertion,
// not sanitization. The default output limit is 8 MiB, with a 16 MiB maximum.
func NewTemplateView(options TemplateViewOptions) (http.Handler, error) {
	renderer, err := newGenericTemplate(options)
	if err != nil {
		return nil, err
	}
	return newReadView(options.ReadViewOptions, func(call *readViewCall) (readViewResult, error) {
		response, err := renderer.render(call)
		return readViewResult{response: response}, err
	})
}

type genericTemplate struct {
	engine   *templates.Engine
	name     string
	extra    templates.Context
	context  func(*http.Request) (templates.Context, error)
	maxBytes int
}

type genericTemplateContextKey struct{}

func newGenericTemplate(options TemplateViewOptions) (*genericTemplate, error) {
	if !genericTemplateName(options.TemplateName) || options.MaxOutputBytes < 0 || options.MaxOutputBytes > 16<<20 {
		return nil, ErrGenericConfiguration
	}
	if options.MaxOutputBytes == 0 {
		options.MaxOutputBytes = 8 << 20
	}
	extra, err := snapshotTemplateContext(options.ExtraContext)
	if err != nil {
		return nil, ErrGenericConfiguration
	}
	config := options.Templates
	if len(config.Loaders) == 0 || len(config.Loaders) > 32 || len(config.Processors) > 32 || config.MaxDepth < 0 || config.MaxDepth > 64 || config.MaxIterations < 0 || config.MaxIterations > 100000 {
		return nil, ErrGenericConfiguration
	}
	config.Loaders = append([]templates.Loader(nil), config.Loaders...)
	for i, loader := range config.Loaders {
		if genericNil(loader) {
			return nil, ErrGenericConfiguration
		}
		var source templates.MapLoader
		switch value := loader.(type) {
		case templates.MapLoader:
			source = value
		case *templates.MapLoader:
			source = *value
		default:
			continue
		}
		copy := templates.MapLoader{}
		for name, content := range source {
			copy[name] = content
		}
		config.Loaders[i] = copy
	}
	processors := append([]templates.Processor(nil), config.Processors...)
	for _, processor := range processors {
		if processor == nil {
			return nil, ErrGenericConfiguration
		}
	}
	// Keep processor execution inside Engine, after its locale initialization.
	// Freeze every result and the aggregate before another callback can run.
	config.Processors = []templates.Processor{func(ctx context.Context) (templates.Context, error) {
		data := templates.Context{}
		for _, processor := range processors {
			if !genericContextOK(ctx) {
				return nil, ErrUnavailable
			}
			extra, err := processor(ctx)
			if err != nil || !genericContextOK(ctx) {
				return nil, ErrUnavailable
			}
			extra, err = snapshotTemplateContext(extra)
			if err != nil {
				return nil, ErrUnavailable
			}
			for k, v := range extra {
				data[k] = v
			}
			data, err = snapshotTemplateContext(data)
			if err != nil {
				return nil, ErrUnavailable
			}
		}
		// Explicit values win, but still share the final aggregate budget with
		// processor values. The request-local key never lives on the engine.
		explicit, _ := ctx.Value(genericTemplateContextKey{}).(templates.Context)
		for k, v := range explicit {
			data[k] = v
		}
		return snapshotTemplateContext(data)
	}}
	return &genericTemplate{engine: templates.New(config), name: options.TemplateName, extra: extra, context: options.Context, maxBytes: options.MaxOutputBytes}, nil
}

func (t *genericTemplate) render(call *readViewCall) (Response, error) {
	values := templates.Context(call.params())
	for k, v := range t.extra {
		values[k] = v
	}
	if t.context != nil {
		data, err := t.context(call.request())
		if !genericContextOK(call.base.Context()) {
			return Response{}, ErrUnavailable
		}
		if err != nil {
			return Response{}, err
		}
		data, err = snapshotTemplateContext(data)
		if err != nil {
			return Response{}, ErrUnavailable
		}
		for k, v := range data {
			values[k] = v
		}
	}
	values, err := snapshotTemplateContext(values)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	ctx := context.WithValue(call.base.Context(), genericTemplateContextKey{}, values)
	output, err := t.engine.Render(ctx, t.name, values)
	if err != nil || !genericContextOK(call.base.Context()) || len(output) > t.maxBytes {
		return Response{}, ErrUnavailable
	}
	return HTML(http.StatusOK, output), nil
}

func genericNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func genericTemplateName(name string) bool {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	parts := strings.Split(name, "#")
	if len(parts) > 2 || !fs.ValidPath(parts[0]) || path.Clean(parts[0]) != parts[0] || parts[0] == "." {
		return false
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return false
		}
		for _, c := range parts[1] {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
				return false
			}
		}
	}
	return true
}
