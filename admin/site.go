package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/files"
	"github.com/cybersaksham/gogo/models"
)

var (
	ErrInvalidURLPrefix = errors.New("invalid admin url prefix")
	ErrDuplicateSite    = errors.New("duplicate admin site")
)

// PermissionPolicy controls access to the admin site.
type PermissionPolicy interface {
	HasAccess(*http.Request) bool
}

// ModelObjectStore persists admin model rows for metadata-backed CRUD.
type ModelObjectStore interface {
	List(context.Context, models.Metadata) ([]map[string]any, error)
	Get(context.Context, models.Metadata, string) (map[string]any, bool, error)
	Create(context.Context, models.Metadata, map[string]any) (map[string]any, error)
	Update(context.Context, models.Metadata, string, map[string]any, bool) (map[string]any, error)
	Delete(context.Context, models.Metadata, string) error
}

// AdminLogStore persists model admin history entries.
type AdminLogStore interface {
	Log(AdminLogEntry) error
	EntriesForObject(contentType, objectID string) ([]AdminLogEntry, error)
}

// StaffPermissionPolicy allows active staff users only.
type StaffPermissionPolicy struct{}

// HasAccess reports whether the request user can access admin.
func (StaffPermissionPolicy) HasAccess(r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	return ok && user.IsActive && user.IsStaff && user.IsAuthenticated() && !user.IsAnonymous()
}

// Registry stores model admin registrations.
type Registry struct {
	mu      sync.RWMutex
	byModel map[string]ModelAdmin
	order   []string
}

// Site is one named Django-style admin site.
type Site struct {
	Name               string
	Header             string
	Title              string
	IndexTitle         string
	URLPrefix          string
	LoginView          http.Handler
	LogoutView         http.Handler
	PasswordChangeView http.Handler
	PermissionPolicy   PermissionPolicy
	ModelRegistry      *Registry
	ModelStore         ModelObjectStore
	LogStore           AdminLogStore
	FileStorage        files.Storage
	TemplateDirs       []string
}

// SiteOptions configures an admin site.
type SiteOptions struct {
	Name               string
	Header             string
	Title              string
	IndexTitle         string
	URLPrefix          string
	LoginView          http.Handler
	LogoutView         http.Handler
	PasswordChangeView http.Handler
	PermissionPolicy   PermissionPolicy
	ModelRegistry      *Registry
	ModelStore         ModelObjectStore
	LogStore           AdminLogStore
	FileStorage        files.Storage
	TemplateDirs       []string
}

// DefaultSite returns the built-in admin site.
func DefaultSite() *Site {
	site, err := NewSite(SiteOptions{
		Name:       "admin",
		Header:     "Gogo administration",
		Title:      "Gogo site admin",
		IndexTitle: "Site administration",
		URLPrefix:  "/admin",
	})
	if err != nil {
		panic(err)
	}
	return site
}

// NewSite creates a configured admin site.
func NewSite(options SiteOptions) (*Site, error) {
	prefix, err := normalizeURLPrefix(options.URLPrefix)
	if err != nil {
		return nil, err
	}
	site := &Site{
		Name:               valueOrDefault(options.Name, "admin"),
		Header:             valueOrDefault(options.Header, "Gogo administration"),
		Title:              valueOrDefault(options.Title, "Gogo site admin"),
		IndexTitle:         valueOrDefault(options.IndexTitle, "Site administration"),
		URLPrefix:          prefix,
		LoginView:          handlerOrDefault(options.LoginView),
		LogoutView:         handlerOrDefault(options.LogoutView),
		PasswordChangeView: handlerOrDefault(options.PasswordChangeView),
		PermissionPolicy:   options.PermissionPolicy,
		ModelRegistry:      options.ModelRegistry,
		ModelStore:         options.ModelStore,
		LogStore:           options.LogStore,
		FileStorage:        options.FileStorage,
		TemplateDirs:       append([]string(nil), options.TemplateDirs...),
	}
	if site.PermissionPolicy == nil {
		site.PermissionPolicy = StaffPermissionPolicy{}
	}
	if site.ModelRegistry == nil {
		site.ModelRegistry = NewRegistry()
	}
	return site, nil
}

// SiteCollection stores multiple named admin sites.
type SiteCollection struct {
	sites map[string]*Site
}

// NewSiteCollection creates an empty site collection.
func NewSiteCollection() *SiteCollection {
	return &SiteCollection{sites: make(map[string]*Site)}
}

// Register adds one site by name.
func (c *SiteCollection) Register(site *Site) error {
	if site == nil {
		return ErrInvalidURLPrefix
	}
	if _, exists := c.sites[site.Name]; exists {
		return ErrDuplicateSite
	}
	c.sites[site.Name] = site
	return nil
}

// Get returns a site by name.
func (c *SiteCollection) Get(name string) (*Site, bool) {
	site, ok := c.sites[name]
	return site, ok
}

func normalizeURLPrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.Contains(prefix, " ") || strings.Contains(prefix, "://") {
		return "", ErrInvalidURLPrefix
	}
	if len(prefix) > 1 {
		prefix = strings.TrimRight(prefix, "/")
	}
	return prefix, nil
}

func handlerOrDefault(handler http.Handler) http.Handler {
	if handler != nil {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
