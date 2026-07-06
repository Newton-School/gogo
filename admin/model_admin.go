package admin

import (
	"net/http"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

// Fieldset describes grouped admin form fields.
type Fieldset struct {
	Name   string
	Fields []string
}

// InlineKind identifies an inline rendering style.
type InlineKind string

const (
	InlineStacked InlineKind = "stacked"
	InlineTabular InlineKind = "tabular"
)

// Inline describes one inline admin relation.
type Inline struct {
	Model          string
	Kind           InlineKind
	Extra          int
	MinNum         int
	MaxNum         int
	CanDelete      bool
	ShowChangeLink bool
	FKName         string
	HasPermission  func(*http.Request, auth.User) bool
}

// URLPattern describes an admin URL extension.
type URLPattern struct {
	Name    string
	Path    string
	Handler http.Handler
}

// ModelAdminHooks stores per-request ModelAdmin extension points.
type ModelAdminHooks struct {
	GetQuerySet           func(*http.Request) any
	GetOrdering           func(*http.Request) []string
	GetSearchResults      func(*http.Request, any, string) (any, bool)
	GetListDisplay        func(*http.Request) []string
	GetListFilter         func(*http.Request) []string
	GetReadonlyFields     func(*http.Request) []string
	GetFields             func(*http.Request) []string
	GetFieldsets          func(*http.Request) []Fieldset
	GetForm               func(*http.Request) any
	SaveModel             func(*http.Request, any) error
	SaveForm              func(*http.Request, any) (any, error)
	SaveFormset           func(*http.Request, any) error
	DeleteModel           func(*http.Request, any) error
	DeleteQueryset        func(*http.Request, any) error
	SaveRelated           func(*http.Request, any) error
	ResponseAdd           func(*http.Request, any) http.Handler
	ResponseChange        func(*http.Request, any) http.Handler
	ResponseDelete        func(*http.Request, any) http.Handler
	MessageUser           func(*http.Request, string)
	LookupAllowed         func(string, string) bool
	GetDeletedObjects     func(*http.Request, []any) []string
	GetChangeList         func(*http.Request) any
	GetPaginator          func(*http.Request) any
	GetAutocompleteFields func(*http.Request) []string
	GetPrepopulatedFields func(*http.Request) map[string][]string
	GetListSelectRelated  func(*http.Request) []string
	GetSortableBy         func(*http.Request) []string
	GetInlineInstances    func(*http.Request) []Inline
	GetInlines            func(*http.Request) []Inline
	HasAddPermission      func(*http.Request, auth.User) bool
	HasChangePermission   func(*http.Request, auth.User) bool
	HasDeletePermission   func(*http.Request, auth.User) bool
	HasViewPermission     func(*http.Request, auth.User) bool
	HasModulePermission   func(*http.Request, auth.User) bool
	GetURLs               func(*http.Request) []URLPattern
}

// ModelAdmin stores model-specific admin configuration.
type ModelAdmin struct {
	Model                   models.Metadata
	Handler                 string
	AllowUnmanaged          bool
	Actions                 []string
	ActionsOnTop            bool
	ActionsOnBottom         bool
	ActionsSelectionCounter bool
	AutocompleteFields      []string
	DateHierarchy           string
	EmptyValueDisplay       string
	Exclude                 []string
	Fields                  []string
	Fieldsets               []Fieldset
	FilterHorizontal        []string
	FilterVertical          []string
	Form                    string
	FormfieldOverrides      map[string]string
	Inlines                 []Inline
	ListDisplay             []string
	ListDisplayLinks        []string
	ListEditable            []string
	ListFilter              []string
	ListMaxShowAll          int
	ListPerPage             int
	ListSelectRelated       []string
	Ordering                []string
	Paginator               string
	PrepopulatedFields      map[string][]string
	PreserveFilters         bool
	RadioFields             map[string]string
	RawIDFields             []string
	ReadonlyFields          []string
	ReadOnly                bool
	SaveAs                  bool
	SaveAsContinue          bool
	SaveOnTop               bool
	SearchFields            []string
	SearchHelpText          string
	ShowFacets              bool
	SortableBy              []string
	ViewOnSite              bool
	CustomURLs              []URLPattern
	ComputedColumns         map[string]ComputedColumn
	ActionDefinitions       []Action
	Hooks                   ModelAdminHooks
}

// Normalize returns a copy with Django-style defaults.
func (a ModelAdmin) Normalize() ModelAdmin {
	clone := a.clone()
	if clone.EmptyValueDisplay == "" {
		clone.EmptyValueDisplay = "-"
	}
	if clone.ListPerPage == 0 {
		clone.ListPerPage = 100
	}
	if clone.ListMaxShowAll == 0 {
		clone.ListMaxShowAll = 200
	}
	clone.ActionsOnTop = true
	clone.ActionsSelectionCounter = true
	clone.PreserveFilters = true
	clone.SaveAsContinue = true
	clone.ViewOnSite = true
	return clone
}

// GetOrdering returns per-request ordering.
func (a ModelAdmin) GetOrdering(r *http.Request) []string {
	if a.Hooks.GetOrdering != nil {
		return append([]string(nil), a.Hooks.GetOrdering(r)...)
	}
	return append([]string(nil), a.Ordering...)
}

// GetListDisplay returns per-request list display columns.
func (a ModelAdmin) GetListDisplay(r *http.Request) []string {
	if a.Hooks.GetListDisplay != nil {
		return append([]string(nil), a.Hooks.GetListDisplay(r)...)
	}
	return append([]string(nil), a.ListDisplay...)
}

// GetReadonlyFields returns per-request readonly fields.
func (a ModelAdmin) GetReadonlyFields(r *http.Request) []string {
	if a.Hooks.GetReadonlyFields != nil {
		return append([]string(nil), a.Hooks.GetReadonlyFields(r)...)
	}
	return append([]string(nil), a.ReadonlyFields...)
}

// GetURLs returns configured and per-request custom URLs.
func (a ModelAdmin) GetURLs(r *http.Request) []URLPattern {
	urls := cloneURLs(a.CustomURLs)
	if a.Hooks.GetURLs != nil {
		urls = append(urls, cloneURLs(a.Hooks.GetURLs(r))...)
	}
	return urls
}

// HasViewPermission checks per-request view permission.
func (a ModelAdmin) HasViewPermission(r *http.Request, user auth.User) bool {
	if a.Hooks.HasViewPermission != nil {
		return a.Hooks.HasViewPermission(r, user)
	}
	return user.IsActive && user.IsStaff
}

func (a ModelAdmin) clone() ModelAdmin {
	a.Model = a.Model.Clone()
	a.Actions = append([]string(nil), a.Actions...)
	a.AutocompleteFields = append([]string(nil), a.AutocompleteFields...)
	a.Exclude = append([]string(nil), a.Exclude...)
	a.Fields = append([]string(nil), a.Fields...)
	a.Fieldsets = cloneFieldsets(a.Fieldsets)
	a.FilterHorizontal = append([]string(nil), a.FilterHorizontal...)
	a.FilterVertical = append([]string(nil), a.FilterVertical...)
	a.FormfieldOverrides = cloneStringMap(a.FormfieldOverrides)
	a.Inlines = append([]Inline(nil), a.Inlines...)
	a.ListDisplay = append([]string(nil), a.ListDisplay...)
	a.ListDisplayLinks = append([]string(nil), a.ListDisplayLinks...)
	a.ListEditable = append([]string(nil), a.ListEditable...)
	a.ListFilter = append([]string(nil), a.ListFilter...)
	a.ListSelectRelated = append([]string(nil), a.ListSelectRelated...)
	a.Ordering = append([]string(nil), a.Ordering...)
	a.PrepopulatedFields = cloneStringSliceMap(a.PrepopulatedFields)
	a.RadioFields = cloneStringMap(a.RadioFields)
	a.RawIDFields = append([]string(nil), a.RawIDFields...)
	a.ReadonlyFields = append([]string(nil), a.ReadonlyFields...)
	a.SearchFields = append([]string(nil), a.SearchFields...)
	a.SortableBy = append([]string(nil), a.SortableBy...)
	a.CustomURLs = cloneURLs(a.CustomURLs)
	a.ComputedColumns = cloneComputedColumns(a.ComputedColumns)
	a.ActionDefinitions = append([]Action(nil), a.ActionDefinitions...)
	return a
}

func cloneFieldsets(values []Fieldset) []Fieldset {
	copied := make([]Fieldset, len(values))
	for i, value := range values {
		copied[i] = Fieldset{Name: value.Name, Fields: append([]string(nil), value.Fields...)}
	}
	return copied
}

func cloneURLs(values []URLPattern) []URLPattern {
	return append([]URLPattern(nil), values...)
}

func cloneComputedColumns(values map[string]ComputedColumn) map[string]ComputedColumn {
	if values == nil {
		return nil
	}
	copied := make(map[string]ComputedColumn, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
