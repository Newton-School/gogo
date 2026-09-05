package admin

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

//go:embed internal/assets/* internal/templates/*
var embedded embed.FS

type Fieldset struct {
	Name, Description string
	Fields            []string
	Classes           []string
}
type DisplayColumn struct {
	Name, Label string
	Value       func(context.Context, Object) (any, error)
}
type Action struct {
	Name, Description, Permission string
	Confirm                       bool
	Run                           func(context.Context, ScopedStore, []Object) error
}
type ModelAdmin struct {
	Schema                                                            models.Schema
	Factory                                                           func() models.Model
	Fields, Exclude, ReadonlyFields                                   []string
	ListDisplay, ListDisplayLinks, SearchFields, ListFilter, Ordering []string
	ListPerPage, ListMaxShowAll                                       int
	Fieldsets                                                         []Fieldset
	Inlines                                                           []Inline
	Columns                                                           []DisplayColumn
	Actions                                                           []Action
	SensitiveFields                                                   []string
	FormOverrides                                                     map[string]forms.Field
	ConstraintChecker                                                 models.ConstraintChecker
	GetReadonlyFields                                                 func(context.Context, Object) []string
	ResolveRelation                                                   func(context.Context, models.Field, []string) ([]any, error)
	SaveModel                                                         func(context.Context, ScopedStore, Object) (Object, error)
	SaveRelated                                                       func(context.Context, ScopedStore, Object, *http.Request) error
}
type Config struct {
	Name, Header, Title, IndexTitle, Prefix, SiteURL, LoginURL string
	Store                                                      Store
	Policy                                                     auth.Policy
	Signer                                                     *security.Signer
	CSRF                                                       security.CSRFConfig
	TemplateLoaders                                            []templates.Loader
}
type Site struct {
	config     Config
	mu         sync.RWMutex
	models     map[string]ModelAdmin
	frozen     bool
	engine     *templates.Engine
	handler    http.Handler
	css        []byte
	cssVersion string
}

func NewSite(config Config) (*Site, error) {
	if config.Name == "" {
		config.Name = "admin"
	}
	if config.Prefix == "" {
		config.Prefix = "/admin/"
	}
	if config.Header == "" {
		config.Header = "Gogo administration"
	}
	if config.Title == "" {
		config.Title = "Gogo Admin"
	}
	if config.IndexTitle == "" {
		config.IndexTitle = "Site administration"
	}
	if !strings.HasPrefix(config.Prefix, "/") || !strings.HasSuffix(config.Prefix, "/") || strings.ContainsAny(config.Prefix, "?#\\") || strings.Contains(config.Prefix, "//") {
		return nil, errors.New("admin: prefix must be a local absolute path ending in slash")
	}
	if config.Store == nil || config.Policy == nil || config.Signer == nil {
		return nil, errors.New("admin: scoped store, policy and signer are required")
	}
	if config.CSRF.Exempt != nil {
		return nil, errors.New("admin: CSRF exemptions are not allowed")
	}
	if config.LoginURL != "" && (!strings.HasPrefix(config.LoginURL, "/") || strings.HasPrefix(config.LoginURL, "//") || strings.ContainsAny(config.LoginURL, "\\\r\n")) {
		return nil, errors.New("admin: login URL must be local")
	}
	templatesFS, _ := fs.Sub(embedded, "internal/templates")
	loaders := append([]templates.Loader(nil), config.TemplateLoaders...)
	loaders = append(loaders, templates.FSLoader{FS: templatesFS})
	s := &Site{config: config, models: map[string]ModelAdmin{}, engine: templates.New(templates.Config{Loaders: loaders})}
	css, err := embedded.ReadFile("internal/assets/admin.css")
	if err != nil {
		return nil, err
	}
	s.css = css
	s.cssVersion = fmt.Sprintf("%x", sha256.Sum256(s.css))
	csrf, err := security.CSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	protected := csrf(http.HandlerFunc(s.serve))
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public embedded assets have no user state and reject unsafe methods.
		// Do not attach CSRF cookies to shared-cacheable static responses.
		if r.URL.Path == config.Prefix+"assets/admin.css" || r.URL.Path == config.Prefix+"assets/admin."+s.cssVersion+".css" {
			s.serve(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
	return s, nil
}

func (s *Site) Register(options ModelAdmin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return errors.New("admin: site is frozen")
	}
	if err := options.Schema.Validate(); err != nil {
		return err
	}
	key := options.Schema.Key()
	if _, ok := s.models[key]; ok {
		return fmt.Errorf("admin: %s already registered", key)
	}
	if options.ListPerPage == 0 {
		options.ListPerPage = 100
	}
	if options.ListMaxShowAll == 0 {
		options.ListMaxShowAll = 1000
	}
	if len(options.Fieldsets) > 0 {
		if len(options.Fields) > 0 {
			return errors.New("admin: Fields and Fieldsets are mutually exclusive")
		}
		seen := map[string]bool{}
		for _, fieldset := range options.Fieldsets {
			for _, name := range fieldset.Fields {
				if seen[name] {
					return errors.New("admin: duplicate field in fieldsets")
				}
				seen[name] = true
				options.Fields = append(options.Fields, name)
			}
		}
	}
	if options.ListPerPage < 1 || options.ListPerPage > 1000 || options.ListMaxShowAll < options.ListPerPage || options.ListMaxShowAll > 1000 {
		return errors.New("admin: invalid pagination bounds")
	}
	if len(options.Fields) == 0 {
		for _, field := range options.Schema.Fields {
			if field.IsEditable() {
				options.Fields = append(options.Fields, field.Name)
			}
		}
	}
	if len(options.ListDisplay) == 0 {
		for _, field := range options.Schema.Fields {
			if field.Kind != models.ManyToMany {
				options.ListDisplay = append(options.ListDisplay, field.Name)
				if len(options.ListDisplay) == 3 {
					break
				}
			}
		}
	}
	displays := map[string]bool{}
	for _, column := range options.Columns {
		if column.Name == "" || column.Value == nil || displays[column.Name] {
			return errors.New("admin: invalid display column")
		}
		displays[column.Name] = true
	}
	groups := [][]string{options.Fields, options.Exclude, options.ReadonlyFields, options.ListDisplay, options.ListDisplayLinks, options.SearchFields, options.ListFilter, options.Ordering, options.SensitiveFields}
	for _, group := range groups {
		for _, name := range group {
			name = strings.TrimPrefix(name, "-")
			if _, ok := options.Schema.Field(name); !ok && !displays[name] {
				return fmt.Errorf("admin: unknown configured field %s", name)
			}
		}
	}
	for _, fieldset := range options.Fieldsets {
		for _, name := range fieldset.Fields {
			if _, ok := options.Schema.Field(name); !ok {
				return fmt.Errorf("admin: unknown fieldset field %s", name)
			}
		}
	}
	actions := map[string]bool{}
	for _, action := range options.Actions {
		if !models.ValidIdentifier(action.Name) || action.Permission == "" || action.Run == nil || actions[action.Name] {
			return errors.New("admin: actions need unique name, permission and handler")
		}
		actions[action.Name] = true
	}
	options.Schema = options.Schema.Clone()
	options.Fields = slices.Clone(options.Fields)
	options.Exclude = slices.Clone(options.Exclude)
	options.ReadonlyFields = slices.Clone(options.ReadonlyFields)
	options.ListDisplay = slices.Clone(options.ListDisplay)
	options.ListDisplayLinks = slices.Clone(options.ListDisplayLinks)
	options.SearchFields = slices.Clone(options.SearchFields)
	options.ListFilter = slices.Clone(options.ListFilter)
	options.Ordering = slices.Clone(options.Ordering)
	options.SensitiveFields = slices.Clone(options.SensitiveFields)
	options.Actions = slices.Clone(options.Actions)
	options.Columns = slices.Clone(options.Columns)
	options.FormOverrides = cloneOverrides(options.FormOverrides)
	options.Fieldsets = append([]Fieldset(nil), options.Fieldsets...)
	for i := range options.Fieldsets {
		options.Fieldsets[i].Fields = slices.Clone(options.Fieldsets[i].Fields)
		options.Fieldsets[i].Classes = slices.Clone(options.Fieldsets[i].Classes)
	}
	options.Inlines = slices.Clone(options.Inlines)
	names := map[string]bool{}
	for i := range options.Inlines {
		if names[options.Inlines[i].Name] {
			return errors.New("admin: duplicate inline name")
		}
		names[options.Inlines[i].Name] = true
		options.Inlines[i].Schema = options.Inlines[i].Schema.Clone()
		options.Inlines[i].Fields = slices.Clone(options.Inlines[i].Fields)
		options.Inlines[i].Exclude = slices.Clone(options.Inlines[i].Exclude)
		options.Inlines[i].Readonly = slices.Clone(options.Inlines[i].Readonly)
		options.Inlines[i].FormOverrides = cloneOverrides(options.Inlines[i].FormOverrides)
		if err := options.Inlines[i].validate(options.Schema); err != nil {
			return err
		}
	}
	s.models[key] = options
	return nil
}

func cloneOverrides(overrides map[string]forms.Field) map[string]forms.Field {
	if overrides == nil {
		return nil
	}
	result := make(map[string]forms.Field, len(overrides))
	for name, field := range overrides {
		result[name] = field.Clone()
	}
	return result
}
func (s *Site) Unregister(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return errors.New("admin: site is frozen")
	}
	if _, ok := s.models[key]; !ok {
		return errors.New("admin: model is not registered")
	}
	delete(s.models, key)
	return nil
}
func (s *Site) IsRegistered(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.models[key]
	return ok
}
func (s *Site) Handler() http.Handler                            { s.mu.Lock(); s.frozen = true; s.mu.Unlock(); return s.handler }
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }
func (s *Site) allowed(ctx context.Context, p auth.Principal, action string, options ModelAdmin, object Object) error {
	return s.config.Policy.Authorize(ctx, p, action, auth.Resource{App: options.Schema.AppLabel, Model: options.Schema.Name, ID: object.ID, Object: object.Record})
}
