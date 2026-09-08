package sites

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
)

// Info is a detached content-selection snapshot, never an authorization grant.
type Info struct {
	ID, Domain, DisplayName string
	Active                  bool
}

type Config struct {
	Backend db.Backend
	SiteID  string
	// Empty permits explicit ByID/job selection only. Current always validates
	// request Host, even when SiteID is configured; forwarded hosts are ignored.
	AllowedHosts []string
	Cache        *CacheConfig
}

// Resolver is immutable and concurrent-safe after construction. Configuration,
// selection data and ORM descriptors are never borrowed from mutable callers.
type Resolver struct{ state *resolverState }
type resolverState struct {
	store         *orm.Store
	alias, siteID string
	hosts         []hostRule
	cache         *cache.Cache
	cached        *siteCacheStore
}

func nilValue(value any) bool {
	if value == nil {
		return true
	}
	r := reflect.ValueOf(value)
	switch r.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return r.IsNil()
	}
	return false
}

func New(config Config) (*Resolver, error) {
	if nilValue(config.Backend) {
		return nil, ErrConfiguration
	}
	hosts, err := allowedRules(config.AllowedHosts)
	if err != nil {
		return nil, err
	}
	s := &resolverState{store: orm.New(config.Backend, nil), alias: config.Backend.Alias(), hosts: hosts}
	if config.SiteID != "" {
		s.siteID, err = canonicalID(config.SiteID)
		if err != nil {
			return nil, ErrConfiguration
		}
	}
	if config.Cache != nil {
		if err := s.configureCache(*config.Cache); err != nil {
			return nil, err
		}
	}
	return &Resolver{state: s}, nil
}

// Current selects the configured SiteID, or exactly one active site matching
// validated request.Host. It never trusts X-Forwarded-Host or picks another site.
func (r *Resolver) Current(request *http.Request) (Info, error) {
	if r == nil || r.state == nil || request == nil {
		return Info{}, ErrConfiguration
	}
	s := r.state
	ctx := request.Context()
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	domain, err := s.host(request.Host)
	if err != nil {
		return Info{}, err
	}
	if s.siteID != "" {
		return s.resolve(ctx, "id", s.siteID)
	}
	return s.resolve(ctx, "domain", domain)
}

// ByID is an explicit request/job content selector. Callers still enforce their
// own permissions for site-owned records. No global or only-site fallback exists.
func (r *Resolver) ByID(ctx context.Context, id string) (Info, error) {
	if r == nil || r.state == nil || nilValue(ctx) {
		return Info{}, ErrConfiguration
	}
	s := r.state
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	id, err := canonicalID(id)
	if err != nil {
		return Info{}, err
	}
	return s.resolve(ctx, "id", id)
}

func (s *resolverState) resolve(ctx context.Context, kind, selector string) (info Info, err error) {
	defer func() {
		if recover() != nil {
			info, err = Info{}, ErrUnavailable
		}
		if canceled := ctx.Err(); canceled != nil {
			info, err = Info{}, canceled
		}
		if err != nil {
			info = Info{}
		}
	}()
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	// An ambient transaction may contain uncommitted site edits or a snapshot
	// older than a shared cache entry. Neither read nor populate shared cache.
	if s.cache == nil || db.InTransaction(ctx, s.alias) {
		return s.load(ctx, kind, selector)
	}
	key := s.cached.key(kind, selector)
	encoded, err := s.cache.GetOrSet(ctx, key, func(ctx context.Context) ([]byte, error) {
		value, err := s.load(ctx, kind, selector)
		if err != nil {
			return nil, err
		}
		return s.cached.encode(kind, selector, value)
	})
	if err != nil {
		if errors.Is(err, ErrSiteNotConfigured) {
			return Info{}, ErrSiteNotConfigured
		}
		return Info{}, ErrUnavailable
	}
	entry, ok := s.cached.decode(key, encoded)
	if !ok {
		return Info{}, ErrUnavailable
	}
	return entry.Site, nil
}

func (s *resolverState) load(ctx context.Context, kind, selector string) (Info, error) {
	query := orm.For(s.store, func() *Site { return &Site{} }).Filter(orm.Q(kind, selector)).Limit(2)
	statement, args, err := query.SQLContext(ctx)
	if err != nil {
		return Info{}, ErrUnavailable
	}
	rows, err := db.ExecutorFor(ctx, s.store.Backend).Query(ctx, statement, args...)
	if err != nil {
		return Info{}, ErrUnavailable
	}
	defer rows.Close()
	var values []Info
	for rows.Next() {
		var value Info
		if err := rows.Scan(&value.ID, &value.Domain, &value.DisplayName, &value.Active); err != nil {
			return Info{}, ErrUnavailable
		}
		values = append(values, value)
		if len(values) == 2 {
			break
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Info{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	if len(values) != 1 {
		return Info{}, ErrSiteNotConfigured
	}
	info := values[0]
	if !validInfo(info, kind, selector) {
		return Info{}, ErrUnavailable
	}
	if !info.Active {
		return Info{}, ErrSiteNotConfigured
	}
	return info, nil
}

func validInfo(value Info, kind, selector string) bool {
	id, err := canonicalID(value.ID)
	if err != nil || id != value.ID || !validName(value.DisplayName) {
		return false
	}
	domain, err := NormalizeDomain(value.Domain)
	if err != nil || domain != value.Domain {
		return false
	}
	return kind == "id" && selector == id || kind == "domain" && selector == domain
}
