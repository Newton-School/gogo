// Package flatpages provides explicitly migrated site-bound pages. Content is
// data, never template source; authentication and authorization remain explicit.
package flatpages

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/templates"
)

const (
	DefaultMaxSites  = 128
	MaxSites         = 1024
	MaxURLBytes      = 2048
	MaxContentBytes  = 1 << 20
	MaxTitleRunes    = 255
	MaxTemplateBytes = 255
)

var (
	ErrConfiguration     = errors.New("flatpages: invalid configuration")
	ErrInvalid           = errors.New("flatpages: invalid page")
	ErrUnavailable       = errors.New("flatpages: operation unavailable")
	ErrNotFound          = errors.New("flatpages: page not found")
	ErrSiteNotConfigured = errors.New("flatpages: site is not configured")
	ErrConflict          = errors.New("flatpages: page or site URL conflict")
	ErrForbidden         = errors.New("flatpages: access denied")
	ErrTransaction       = errors.New("flatpages: operation requires its own transaction")
	ErrOutcomeUnknown    = errors.New("flatpages: commit outcome unknown; reconcile before retry")
	ErrLimit             = errors.New("flatpages: limit exceeded")
)

type Draft struct {
	// Empty ID creates a new page. Nonempty ID updates only an existing page,
	// unless SaveInput.Create explicitly requests a caller-known new identity.
	ID, URL, Title, Content, TemplateName string
	RegistrationRequired                  bool
}

// Info is detached bounded content, not a grant or a safe-HTML assertion.
type Info struct {
	ID, URL, Title, Content, TemplateName string
	RegistrationRequired                  bool
}

type Saved struct {
	Page    Info
	SiteIDs []string
}
type SaveInput struct {
	Page Draft
	// Create makes a supplied page ID create-only. Persist a caller-known UUID
	// before invocation when an uncertain commit must be reconciled by identity.
	// An existing ID conflicts; this never enables an upsert or automatic retry.
	Create bool
	// The complete replacement set, not an incremental change. Empty detaches
	// every site. SaveDomain authorizes retained and removed sites as well.
	SiteIDs []string
}
type LookupInput struct{ SiteID, URL string }

type ChangeAction string

const (
	CreatePage ChangeAction = "create_page"
	UpdatePage ChangeAction = "update_page"
	AttachSite ChangeAction = "attach_site"
	RetainSite ChangeAction = "retain_site"
	DetachSite ChangeAction = "detach_site"
)

// Change is a read-only grant request. Before is nil only for creation; each
// callback receives a fresh detached copy. SiteID is empty for page actions.
type Change struct {
	Action ChangeAction
	Before *Info
	After  Info
	SiteID string
}

type Config struct {
	Backend  db.Backend
	MaxSites int
	// Required by SaveDomain, optional for read-only Lookup installations.
	// Check current page/site authority and lock any external ACL state needed
	// for transactional guarantees. Callbacks must not mutate content or grants.
	AuthorizeChange func(context.Context, Change) error
}

type RenderOptions struct {
	Templates        templates.Config
	DefaultTemplate  string
	AllowedTemplates []string
	// Optional trusted sanitizer, called only after HTTP reader authorization.
	// SafeHTML is an assertion, not a sanitizer. Loaders/templates/extensions
	// are trusted application code and must preserve the content trust policy.
	Sanitize       func(context.Context, Info) (templates.SafeHTML, error)
	MaxOutputBytes int
}

type HandlerOptions struct {
	Sites  *sites.Resolver
	Render RenderOptions
	// Mandatory read-only current authority check. Site and page are selection
	// snapshots, not ACL evidence. Return exact ErrForbidden to conceal a page
	// with 404; other failures return a safe unavailable response.
	Authorize func(context.Context, sites.Info, Info) error
	NotFound  http.Handler
	Timeout   time.Duration
}

func nilValue(value any) bool {
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
func contextError(ctx context.Context) (err error) {
	if nilValue(ctx) {
		return ErrConfiguration
	}
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	return ctx.Err()
}
