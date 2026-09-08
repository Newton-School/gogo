package integration

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/orm"
)

type sitesTestCache struct {
	cache.Store
	mu         sync.Mutex
	values     map[string][]byte
	gets, sets int
}

func (c *sitesTestCache) Get(ctx context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	value, ok := c.values[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}
func (c *sitesTestCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	if c.values == nil {
		c.values = map[string][]byte{}
	}
	c.values[key] = append([]byte(nil), value...)
	return nil
}
func (c *sitesTestCache) counts() (int, int) { c.mu.Lock(); defer c.mu.Unlock(); return c.gets, c.sets }

func TestPostgresSitesMigrationsCanonicalLookupAndContext(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: sites.Migrations()}
	for range 2 {
		if err := runner.Apply(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, nil)
	first, err := sites.NewSite("EXAMPLE.TEST.", "Example")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sites.NewSite("second.test", "Second")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []*sites.Site{first, second} {
		if err := store.Save(ctx, site, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err != nil {
			t.Fatal(err)
		}
	}
	duplicate, _ := sites.NewSite("eXaMpLe.TeSt", "Duplicate")
	if err := store.Save(ctx, duplicate, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err == nil {
		t.Fatal("canonical unique domain not enforced")
	}
	r, err := sites.New(sites.Config{Backend: backend, AllowedHosts: []string{"example.test", "second.test", "missing.test"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "https://EXAMPLE.TEST.:443/", nil)
	request.Header.Set("X-Forwarded-Host", "second.test")
	info, err := r.Current(request)
	if err != nil || info.ID != first.ID || info.Domain != "example.test" {
		t.Fatal(info, err)
	}
	selected, err := r.WithCurrent(request)
	if err != nil {
		t.Fatal(err)
	}
	if current, ok := sites.FromContext(selected); !ok || current != info {
		t.Fatal(current, ok)
	}
	if _, ok := sites.FromContext(request.Context()); ok {
		t.Fatal("request mutated")
	}
	job, err := r.WithSite(ctx, strings.ToUpper(second.ID))
	if err != nil {
		t.Fatal(err)
	}
	if current, ok := sites.FromContext(job); !ok || current.ID != second.ID {
		t.Fatal(current, ok)
	}
	explicit, err := sites.New(sites.Config{Backend: backend, SiteID: second.ID, AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := explicit.Current(request); err != nil || got.ID != second.ID {
		t.Fatal("configured ID not preferred", got, err)
	}
	second.Active = false
	if err := store.Save(ctx, second, orm.SaveOptions{UpdateFields: []string{"active"}, Prepare: sites.Prepare}); err != nil {
		t.Fatal(err)
	}
	if got, err := explicit.Current(request); !errors.Is(err, sites.ErrSiteNotConfigured) || got != (sites.Info{}) {
		t.Fatal("inactive site fell back", got, err)
	}
	for _, host := range []string{"second.test", "missing.test"} {
		request.Host = host
		if got, err := r.Current(request); !errors.Is(err, sites.ErrSiteNotConfigured) || got != (sites.Info{}) {
			t.Fatal(host, got, err)
		}
	}
	request.Host = "attacker.test"
	if _, err := r.Current(request); !errors.Is(err, sites.ErrInvalidHost) {
		t.Fatal(err)
	}
	if err := runner.Reverse(ctx, "gogo_sites.zero"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ByID(ctx, first.ID); got != (sites.Info{}) || !errors.Is(err, sites.ErrUnavailable) {
		t.Fatal("missing table invented site", got, err)
	}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ByID(ctx, first.ID); got != (sites.Info{}) || !errors.Is(err, sites.ErrSiteNotConfigured) {
		t.Fatal("migration seeded an implicit site", got, err)
	}
}

func TestPostgresSitesCacheNeverCrossesTransactionOrProject(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: sites.Migrations()}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, nil)
	site, _ := sites.NewSite("example.test", "Committed")
	if err := store.Save(ctx, site, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err != nil {
		t.Fatal(err)
	}
	c := &sitesTestCache{}
	config := sites.Config{Backend: backend, Cache: &sites.CacheConfig{Store: c, Namespace: "project.one.database", Version: 1, TTL: time.Minute}}
	r, err := sites.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByID(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	gets, sets := c.counts()
	rollback := errors.New("intentional rollback")
	err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(tx context.Context) error {
		row, err := orm.For(store, func() *sites.Site { return &sites.Site{} }).Filter(orm.Q("id", site.ID)).Get(tx)
		if err != nil {
			return err
		}
		row.DisplayName = "Uncommitted"
		if err := store.Save(tx, row, orm.SaveOptions{UpdateFields: []string{"display_name"}, Prepare: sites.Prepare}); err != nil {
			return err
		}
		if got, err := r.ByID(tx, site.ID); err != nil || got.DisplayName != "Uncommitted" {
			t.Fatal("cache overrode own transaction", got, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if g, s := c.counts(); g != gets || s != sets {
		t.Fatal("transaction accessed cache", g, s, gets, sets)
	}
	if got, err := r.ByID(ctx, site.ID); err != nil || got.DisplayName != "Committed" {
		t.Fatal("rollback leaked into shared cache", got, err)
	}
	// Empty-cache transactional reads never populate on commit or cancellation.
	config.Cache.Version = 2
	r, err = sites.New(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelBeforeReturn := range []bool{false, true} {
		parent, cancel := context.WithCancel(ctx)
		gets, sets = c.counts()
		err = db.Atomic(parent, backend, db.AtomicOptions{}, func(tx context.Context) error {
			if _, err := r.ByID(tx, site.ID); err != nil {
				return err
			}
			if cancelBeforeReturn {
				cancel()
			}
			return nil
		})
		cancel()
		if !cancelBeforeReturn && err != nil {
			t.Fatal(err)
		}
		if g, s := c.counts(); g != gets || s != sets {
			t.Fatal("transaction populated cache", g, s)
		}
	}
	// The same backend alias in another database/project cannot reuse the key.
	other := testservice.Postgres(t)
	otherRunner := migrations.Executor{Backend: other, Editor: other.SchemaEditor(), Migrations: sites.Migrations()}
	if err := otherRunner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	otherSite := &sites.Site{ID: site.ID, Domain: "other.test", DisplayName: "Other project", Active: true}
	if err := orm.New(other, nil).Save(ctx, otherSite, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err != nil {
		t.Fatal(err)
	}
	config.Backend = other
	config.Cache.Namespace = "project.two.database"
	otherResolver, err := sites.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := otherResolver.ByID(ctx, site.ID); err != nil || got.Domain != "other.test" {
		t.Fatal("project cache collision", got, err)
	}
	if got, err := r.ByID(ctx, site.ID); err != nil || got.Domain != "example.test" {
		t.Fatal(got, err)
	}
}
