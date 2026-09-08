// Package redirects provides explicitly migrated, site-bound redirects at a
// router's final not-found boundary. Site selection is not an authorization
// grant. Applications opt into HTTP access and external destinations separately.
package redirects

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
)

const (
	// MaxURIBytes bounds both stored keys and emitted ASCII locations. The
	// bound also keeps the built-in composite key within PostgreSQL limits.
	MaxURIBytes    = 2048
	DefaultMaxHops = 16
	MaxHops        = 32
)

var (
	ErrConfiguration     = errors.New("redirects: invalid configuration")
	ErrInvalid           = errors.New("redirects: invalid URI or record")
	ErrUnavailable       = errors.New("redirects: lookup unavailable")
	ErrSiteNotConfigured = errors.New("redirects: site is not configured")
	ErrCycle             = errors.New("redirects: redirect cycle")
	ErrLimit             = errors.New("redirects: traversal limit exceeded")
	ErrTransaction       = errors.New("redirects: lookup requires its own transaction")
	ErrForbidden         = errors.New("redirects: access denied")
)

type Config struct {
	Backend db.Backend
	// AppendSlash retries a missing exact key with one trailing slash before
	// its unchanged query. It never cleans a path or removes a slash.
	AppendSlash bool
	// PreserveQuery copies the incoming query only when the target has no
	// query delimiter. An explicit empty target query suppresses copying.
	PreserveQuery bool
	// Zero selects DefaultMaxHops. Values above MaxHops are rejected.
	MaxHops int
}

// LookupInput is explicit server-owned selection. Lookup does not grant HTTP
// access or authorize an external destination; HandlerOptions owns that policy.
// Origin is the validated request origin, including its scheme and port.
type LookupInput struct{ SiteID, URI, Origin string }

// Match is a detached first-hop result, returned only after the bounded local
// chain has been checked in one database snapshot. It does not flatten chains
// or establish the safety of redirects subsequently issued by remote servers.
type Match struct {
	ID, SiteID, OldPath, NewPath, Location string
	Permanent, External                    bool
}

type HandlerOptions struct {
	Sites *sites.Resolver
	// Authorize is required and runs before lookup and again before emission.
	// Return ErrForbidden for a denial; other errors fail safely unavailable.
	// Grant callbacks are trusted read-only checks. Info is a content-selection
	// snapshot, possibly cached, not fresh authorization evidence; check current
	// application authority using its immutable ID. Concurrent grant changes
	// need application-owned synchronization when stronger guarantees are needed.
	Authorize func(context.Context, sites.Info) error
	// AllowExternal is required for a cross-origin Location. It receives the
	// complete normalized first-hop location; nil denies external redirects.
	// Like Authorize, it must not mutate grants or request policy.
	AllowExternal func(context.Context, sites.Info, string) error
	// NotFound handles an absent record or a method that is not GET/HEAD.
	// Nil uses the standard 404. Matched views never pass through this handler.
	NotFound http.Handler
	// Zero selects five seconds; accepted range is one millisecond to a minute.
	Timeout time.Duration
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

// Context implementations are application callbacks, too. Do not let a nil or
// panicking Err method expose a partially validated redirect.
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
