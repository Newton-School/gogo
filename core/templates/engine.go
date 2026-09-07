// Package templates implements the Django template language with explicit
// loaders and registered extensions. HTML output uses contextual escaping.
package templates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/Newton-School/gogo/core/i18n"
)

var ErrNotFound = errors.New("templates: template not found")
var ErrRender = errors.New("templates: rendering failed")

type Context map[string]any

// SafeHTML must only wrap HTML created by trusted server code.
type SafeHTML string
type Loader interface {
	Load(context.Context, string) (string, error)
}
type FSLoader struct{ FS fs.FS }

func (l FSLoader) Load(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validName(name) || l.FS == nil {
		return "", ErrNotFound
	}
	b, err := fs.ReadFile(l.FS, name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", ErrRender
	}
	if len(b) > 2<<20 {
		return "", ErrRender
	}
	return string(b), nil
}

type MapLoader map[string]string

func (l MapLoader) Load(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validName(name) {
		return "", ErrNotFound
	}
	s, ok := l[name]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

type Filter func(context.Context, any, any) (any, error)
type Tag func(context.Context, Context, []any) (any, error)
type Processor func(context.Context) (Context, error)
type Config struct {
	Loaders                 []Loader
	Strict, Debug           bool
	MaxDepth, MaxIterations int
	Processors              []Processor
	Filters                 map[string]Filter
	Tags                    map[string]Tag
	Libraries               []string
	// LocaleResolver supplies the project default and the allowlist for tz
	// overrides. Without it, an inherited locale or UTC is used; templates
	// cannot load arbitrary timezone files at render time.
	LocaleResolver *i18n.Resolver
}
type Engine struct {
	config Config
	mu     sync.RWMutex
	cache  map[string]cached
}
type cached struct {
	digest [32]byte
	nodes  []node
}

func New(config Config) *Engine {
	if config.MaxDepth <= 0 {
		config.MaxDepth = 64
	}
	if config.MaxIterations <= 0 {
		config.MaxIterations = 100000
	}
	config.Loaders = append([]Loader(nil), config.Loaders...)
	filters := builtinFilters()
	for k, v := range config.Filters {
		filters[k] = v
	}
	config.Filters = filters
	tags := map[string]Tag{}
	for k, v := range config.Tags {
		tags[k] = v
	}
	config.Tags = tags
	config.Processors = append([]Processor(nil), config.Processors...)
	config.Libraries = append([]string(nil), config.Libraries...)
	return &Engine{config: config, cache: map[string]cached{}}
}
func validName(name string) bool {
	return name != "" && fs.ValidPath(name) && !strings.Contains(name, `\`) && path.Clean(name) == name && !strings.ContainsRune(name, 0)
}
func (e *Engine) load(ctx context.Context, name string) ([]node, error) {
	if !validName(name) {
		return nil, ErrNotFound
	}
	var source string
	found := false
	for _, loader := range e.config.Loaders {
		s, err := loader.Load(ctx, name)
		if err == nil {
			source = s
			found = true
			break
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	if len(source) > 2<<20 {
		return nil, ErrRender
	}
	digest := sha256.Sum256([]byte(source))
	e.mu.RLock()
	entry, ok := e.cache[name]
	e.mu.RUnlock()
	if ok && entry.digest == digest {
		return entry.nodes, nil
	}
	nodes, err := parse(source)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.cache[name] = cached{digest, nodes}
	e.mu.Unlock()
	return nodes, nil
}

func (e *Engine) Render(ctx context.Context, name string, values Context) (string, error) {
	if ctx == nil {
		return "", ErrRender
	}
	var err error
	ctx, err = e.timezoneContext(ctx)
	if err != nil {
		return "", err
	}
	data := Context{}
	for _, processor := range e.config.Processors {
		extra, err := processor(ctx)
		if err != nil {
			return "", ErrRender
		}
		for k, v := range extra {
			data[k] = v
		}
	}
	for k, v := range values {
		data[k] = v
	}
	for k, v := range data {
		safe, err := projectValue(v, 0)
		if err != nil {
			return "", err
		}
		data[k] = safe
	}
	r := &renderer{engine: e, ctx: ctx, values: map[string]any{}, autoescape: true}
	nodes, err := e.load(ctx, strings.SplitN(name, "#", 2)[0])
	if err != nil {
		return "", err
	}
	if parts := strings.SplitN(name, "#", 2); len(parts) == 2 {
		found := findNamed(nodes, "partialdef", parts[1])
		if found == nil {
			return "", ErrNotFound
		}
		nodes = found
	}
	var source strings.Builder
	if err = r.renderTemplate(nodes, data, nil, &source, 0); err != nil {
		return "", err
	}
	t, err := htmltemplate.New("output").Parse(source.String())
	if err != nil {
		return "", ErrRender
	}
	out := boundedOutput{ctx: ctx, remaining: 16 << 20}
	if err = t.Execute(&out, r.values); err != nil {
		return "", ErrRender
	}
	return out.String(), nil
}
func (e *Engine) RenderString(ctx context.Context, source string, values Context) (string, error) {
	config := e.config
	config.Loaders = append([]Loader{MapLoader{"inline": source}}, config.Loaders...)
	return New(config).Render(ctx, "inline", values)
}
func findNamed(nodes []node, kind, name string) []node {
	for _, n := range nodes {
		if n.kind == kind && strings.Fields(n.arg)[0] == name {
			return n.children
		}
		if found := findNamed(n.children, kind, name); found != nil {
			return found
		}
	}
	return nil
}

type renderer struct {
	engine            *Engine
	ctx               context.Context
	values            map[string]any
	count, iterations int
	autoescape        bool
	cycles            map[*node]*cycleState
	namedCycles       map[string]*cycleState
	lastCycle         *cycleState
	changed           map[*node]any
}

type boundedOutput struct {
	bytes.Buffer
	ctx       context.Context
	remaining int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.remaining {
		return 0, ErrRender
	}
	b.remaining -= len(p)
	return b.Buffer.Write(p)
}

func (r *renderer) emit(out *strings.Builder, value any) error {
	var err error
	value, err = projectValue(value, 0)
	if err != nil {
		return err
	}
	if value == nil {
		value = ""
	}
	if instant, ok := templateTime(r.ctx, value); ok {
		value = instant
	}
	switch v := value.(type) {
	case SafeHTML:
		value = htmltemplate.HTML(v)
	case htmltemplate.HTML:
		value = v
	}
	name := fmt.Sprintf("v%d", r.count)
	r.count++
	r.values[name] = value
	out.WriteString(`{{index . "` + name + `"}}`)
	return nil
}
func writeText(out *strings.Builder, text string) {
	out.WriteString(strings.ReplaceAll(text, "{{", `{{"{{"}}`))
}
func copyContext(c Context) Context {
	r := Context{}
	for k, v := range c {
		r[k] = v
	}
	return r
}
