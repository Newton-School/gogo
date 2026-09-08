package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

const firstID = "00000000-0000-4000-8000-000000000001"
const secondID = "00000000-0000-4000-8000-000000000002"

type testDialect struct{}

func (testDialect) Name() string                             { return "postgres" }
func (testDialect) QuoteIdentifier(s string) (string, error) { return `"` + s + `"`, nil }
func (testDialect) Placeholder(i int) string                 { return fmt.Sprintf("$%d", i) }
func (testDialect) FieldType(models.Field) (string, error)   { return "text", nil }

type testBackend struct {
	db.Backend
	query func(context.Context, string, []any) (db.Rows, error)
	calls atomic.Int32
}

func (*testBackend) Alias() string       { return "default" }
func (*testBackend) Dialect() db.Dialect { return testDialect{} }
func (b *testBackend) Query(ctx context.Context, sql string, args ...any) (db.Rows, error) {
	b.calls.Add(1)
	return b.query(ctx, sql, args)
}

type testRows struct {
	values        []Info
	i             int
	err, closeErr error
	closeHook     func()
}

func (*testRows) Columns() ([]string, error) {
	return []string{"id", "domain", "display_name", "active"}, nil
}
func (r *testRows) Next() bool {
	if r.i >= len(r.values) {
		return false
	}
	r.i++
	return true
}
func (r *testRows) Scan(dest ...any) error {
	v := r.values[r.i-1]
	*dest[0].(*string), *dest[1].(*string), *dest[2].(*string), *dest[3].(*bool) = v.ID, v.Domain, v.DisplayName, v.Active
	return nil
}
func (r *testRows) Err() error { return r.err }
func (r *testRows) Close() error {
	if r.closeHook != nil {
		r.closeHook()
	}
	return r.closeErr
}

func fixtureInfo() Info {
	return Info{ID: firstID, Domain: "example.test", DisplayName: "Example", Active: true}
}
func fixtureBackend() *testBackend {
	return &testBackend{query: func(ctx context.Context, sql string, args []any) (db.Rows, error) {
		return &testRows{values: []Info{fixtureInfo()}}, nil
	}}
}

func TestSitesSchemaAndCanonicalProvisioning(t *testing.T) {
	site, err := NewSite("EXAMPLE.TEST.", "Example")
	if err != nil || site.Domain != "example.test" || !site.Active {
		t.Fatal(site, err)
	}
	if id, err := canonicalID(site.ID); err != nil || id != site.ID {
		t.Fatal(site.ID, err)
	}
	if _, err := models.Bind(site); err != nil {
		t.Fatal(err)
	}
	one, two := Migrations(), Migrations()
	a, err := one[0].Checksum()
	if err != nil {
		t.Fatal(err)
	}
	b, err := two[0].Checksum()
	if err != nil || a != b {
		t.Fatal(a, b, err)
	}
	one[0].Operations[0].Schema.Fields[1].Name = "changed"
	if two[0].Operations[0].Schema.Fields[1].Name != "domain" {
		t.Fatal("shared schema metadata")
	}
	site.Domain = "SECOND.TEST."
	record, _ := models.Bind(site)
	if err := Prepare(context.Background(), record); err != nil || site.Domain != "second.test" {
		t.Fatal(site, err)
	}
	site.Active = false
	if err := models.ApplyDefaults(record); err != nil || site.Active {
		t.Fatal("constructor explicit false overwritten", err)
	}
	for _, name := range []string{"", strings.Repeat("x", 256), "bad\x00name", string([]byte{255})} {
		if _, err := NewSite("example.test", name); err == nil {
			t.Fatal("invalid display name", len(name))
		}
	}
}

func TestSitesHostPolicies(t *testing.T) {
	for input, want := range map[string]string{"EXAMPLE.TEST.": "example.test", "xn--bcher-kva.test": "xn--bcher-kva.test", "127.0.0.1": "127.0.0.1", "2001:0db8::1": "2001:db8::1"} {
		if got, err := NormalizeDomain(input); err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"", "bücher.test", "https://example.test", "example.test:80", "example.test..", "a..test", "-a.test", "a-.test", "a_b.test", "*.test", "a@test", "fe80::1%zone", "[::1]", strings.Repeat("x", 64) + ".test", "example.test\x00"} {
		if got, err := NormalizeDomain(input); err == nil || got != "" {
			t.Fatal("invalid domain", input, got, err)
		}
	}
	for input, want := range map[string]string{"EXAMPLE.TEST.:443": "example.test", "example.test:00080": "example.test", "[2001:db8::1]:443": "2001:db8::1", "[::1]": "::1", "127.0.0.1:8000": "127.0.0.1"} {
		if got, err := requestDomain(input); err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"example.test:", "example.test:0", "example.test:65536", "example.test:+80", "example.test: 80", "example.test:080000", "example.test:80/path", "[example.test]:80", "::1", "[fe80::1%zone]:80", "example.test?x", "example.test#x"} {
		if got, err := requestDomain(input); err == nil || got != "" {
			t.Fatal("invalid authority", input, got, err)
		}
	}
}

func TestSitesSelectionHostValidationAndContext(t *testing.T) {
	b := fixtureBackend()
	allowed := []string{".example.test"}
	r, err := New(Config{Backend: b, AllowedHosts: allowed, SiteID: firstID})
	if err != nil {
		t.Fatal(err)
	}
	allowed[0] = "evil.test"
	b.query = func(ctx context.Context, sql string, args []any) (db.Rows, error) {
		if !strings.Contains(sql, `"id" = $1`) || len(args) != 2 || args[0] != firstID || args[1] != 2 || !strings.Contains(sql, "LIMIT $2") {
			t.Fatal(sql, args)
		}
		return &testRows{values: []Info{fixtureInfo()}}, nil
	}
	request := httptest.NewRequest("GET", "https://child.example.test:443/", nil)
	request.Header.Set("X-Forwarded-Host", "evil.test")
	ctx, err := r.WithCurrent(request)
	if err != nil {
		t.Fatal(err)
	}
	info, ok := FromContext(ctx)
	if !ok || info != fixtureInfo() {
		t.Fatal(info, ok)
	}
	info.DisplayName = "caller changed"
	if original, _ := FromContext(ctx); original.DisplayName != "Example" {
		t.Fatal("context shared output")
	}
	if _, ok := FromContext(request.Context()); ok {
		t.Fatal("parent context mutated")
	}
	for _, host := range []string{"evil.test", "badexample.test", "child.example.test.evil", "*.example.test", "example.test:invalid"} {
		request.Host = host
		before := b.calls.Load()
		if got, err := r.Current(request); !errors.Is(err, ErrInvalidHost) || got != (Info{}) || b.calls.Load() != before {
			t.Fatal(host, got, err)
		}
	}
	jobs, _ := New(Config{Backend: b})
	if got, err := jobs.ByID(context.Background(), firstID); err != nil || got.ID != firstID {
		t.Fatal(got, err)
	}
	if _, err := jobs.Current(httptest.NewRequest("GET", "https://example.test/", nil)); !errors.Is(err, ErrInvalidHost) {
		t.Fatal(err)
	}
	if _, err := jobs.ByID(context.Background(), ""); !errors.Is(err, ErrSiteNotConfigured) {
		t.Fatal(err)
	}
}

func TestSitesReadFailuresNeverYieldPartialOrDifferentSite(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "inactive", "wrong-id", "wrong-domain", "noncanonical", "bad-name", "query-error", "query-panic", "row-error", "close-error", "late-cancel"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := fixtureBackend()
			want := ErrUnavailable
			b.query = func(context.Context, string, []any) (db.Rows, error) {
				rows := &testRows{values: []Info{fixtureInfo()}}
				switch name {
				case "missing":
					rows.values = nil
					want = ErrSiteNotConfigured
				case "duplicate":
					rows.values = append(rows.values, fixtureInfo())
					want = ErrSiteNotConfigured
				case "inactive":
					rows.values[0].Active = false
					want = ErrSiteNotConfigured
				case "wrong-id":
					rows.values[0].ID = secondID
				case "wrong-domain":
					rows.values[0].Domain = "invalid:80"
				case "noncanonical":
					rows.values[0].Domain = "EXAMPLE.TEST"
				case "bad-name":
					rows.values[0].DisplayName = "\x00bad"
				case "query-error":
					return nil, errors.New("private provider detail")
				case "query-panic":
					panic("private provider detail")
				case "row-error":
					rows.err = errors.New("private provider detail")
				case "close-error":
					rows.closeErr = errors.New("private provider detail")
				case "late-cancel":
					rows.closeHook = cancel
					want = context.Canceled
				}
				return rows, nil
			}
			r, _ := New(Config{Backend: b})
			got, err := r.ByID(ctx, firstID)
			if got != (Info{}) || !errors.Is(err, want) || strings.Contains(err.Error(), "private") {
				t.Fatal(got, err, want)
			}
		})
	}
}

type memoryCache struct {
	cache.Store
	mu             sync.Mutex
	values         map[string][]byte
	gets, sets     int
	getErr, setErr error
	onGet, onSet   func()
}

func (c *memoryCache) Get(ctx context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.onGet != nil {
		c.onGet()
	}
	if c.getErr != nil {
		return nil, c.getErr
	}
	b, ok := c.values[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), b...), nil
}
func (c *memoryCache) Set(ctx context.Context, key string, b []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	if c.onSet != nil {
		c.onSet()
	}
	if c.setErr != nil {
		return c.setErr
	}
	if c.values == nil {
		c.values = map[string][]byte{}
	}
	c.values[key] = append([]byte(nil), b...)
	for i := range b {
		b[i] = 0
	} // Provider mutation must not modify the loader's return value.
	return nil
}
func cachedResolver(t *testing.T, b *testBackend, c *memoryCache, version uint64, bypass bool) *Resolver {
	t.Helper()
	r, err := New(Config{Backend: b, AllowedHosts: []string{"example.test"}, Cache: &CacheConfig{Store: c, Namespace: "project.database", Version: version, TTL: time.Minute, BypassUnavailable: bypass}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSitesCacheScopeIdentityFreshnessAndFailurePolicies(t *testing.T) {
	ctx := context.Background()
	b, c := fixtureBackend(), &memoryCache{}
	r := cachedResolver(t, b, c, 1, false)
	for range 2 {
		if got, err := r.ByID(ctx, firstID); err != nil || got != fixtureInfo() {
			t.Fatal(got, err)
		}
	}
	if b.calls.Load() != 1 || c.sets != 1 {
		t.Fatal("cache missed", b.calls.Load(), c.sets)
	}
	key := r.state.cached.key("id", firstID)
	for _, corrupt := range []string{`{}`, strings.Repeat("x", 8193), `{"Site":null}`, `{"Version":1,"Version":2}`} {
		c.values[key] = []byte(corrupt)
		before := b.calls.Load()
		if _, err := r.ByID(ctx, firstID); err != nil || b.calls.Load() != before+1 {
			t.Fatal("corrupt entry trusted", err)
		}
	}
	entry, ok := r.state.cached.decode(key, c.values[key])
	if !ok {
		t.Fatal("missing valid entry")
	}
	entry.Site.ID = secondID
	wrong, _ := r.state.cached.encode("id", firstID, entry.Site)
	c.values[key] = wrong
	before := b.calls.Load()
	if _, err := r.ByID(ctx, firstID); err != nil || b.calls.Load() != before+1 {
		t.Fatal("wrong identity trusted", err)
	}
	second := cachedResolver(t, b, c, 2, false)
	before = b.calls.Load()
	if _, err := second.ByID(ctx, firstID); err != nil || b.calls.Load() != before+1 {
		t.Fatal("config versions shared", err)
	}
	c.getErr = errors.New("private unavailable")
	before = b.calls.Load()
	if got, err := r.ByID(ctx, firstID); got != (Info{}) || !errors.Is(err, ErrUnavailable) || b.calls.Load() != before {
		t.Fatal(got, err)
	}
	bypass := cachedResolver(t, b, c, 1, true)
	c.setErr = errors.New("private unavailable")
	if got, err := bypass.ByID(ctx, firstID); err != nil || got != fixtureInfo() || b.calls.Load() != before+1 {
		t.Fatal("bypass skipped real loader", got, err)
	}
	c.getErr = cache.ErrMiss
	if got, err := r.ByID(ctx, firstID); got != (Info{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatal("required write failure ignored", got, err)
	}
}

func TestSitesCacheFreezesResolverAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, c := fixtureBackend(), &memoryCache{}
	r := cachedResolver(t, b, c, 1, false)
	replacement, _ := New(Config{Backend: fixtureBackend(), SiteID: secondID})
	c.onGet = func() { *r = *replacement }
	if got, err := r.ByID(ctx, firstID); err != nil || got.ID != firstID || b.calls.Load() != 1 {
		t.Fatal(got, err)
	}
	r = cachedResolver(t, b, c, 1, false)
	c.onGet = cancel
	if got, err := r.ByID(ctx, firstID); got != (Info{}) || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
}

func TestSitesCacheCoalescesConcurrentLoads(t *testing.T) {
	b, c := fixtureBackend(), &memoryCache{}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	b.query = func(ctx context.Context, sql string, args []any) (db.Rows, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return &testRows{values: []Info{fixtureInfo()}}, nil
	}
	r := cachedResolver(t, b, c, 1, false)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := r.ByID(context.Background(), firstID)
			if err != nil || got != fixtureInfo() {
				t.Error(got, err)
			}
		})
	}
	<-entered
	close(release)
	wg.Wait()
	if b.calls.Load() != 1 {
		t.Fatal("loads not coalesced", b.calls.Load())
	}
}

func TestSitesRejectsConfigurationBeforeQueries(t *testing.T) {
	b := fixtureBackend()
	for _, config := range []Config{{}, {Backend: (*testBackend)(nil)}, {Backend: b, SiteID: "bad"}, {Backend: b, AllowedHosts: []string{"*"}}, {Backend: b, AllowedHosts: []string{"EXAMPLE.TEST", "example.test"}}, {Backend: b, AllowedHosts: []string{".127.0.0.1"}}, {Backend: b, Cache: &CacheConfig{}}, {Backend: b, Cache: &CacheConfig{Store: &memoryCache{}, Namespace: "", Version: 1, TTL: time.Minute}}} {
		if r, err := New(config); r != nil || !errors.Is(err, ErrConfiguration) {
			t.Fatal(r, err)
		}
	}
	if b.calls.Load() != 0 {
		t.Fatal("configuration ran queries")
	}
	r, _ := New(Config{Backend: b})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := r.ByID(ctx, firstID); got != (Info{}) || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if got, err := r.ByID(nil, firstID); got != (Info{}) || !errors.Is(err, ErrConfiguration) {
		t.Fatal(got, err)
	}
	if _, ok := FromContext(nil); ok {
		t.Fatal("nil selected context")
	}
}

func TestSitesCacheRejectsExpiryAndKeepsConfigurationSnapshot(t *testing.T) {
	b, c := fixtureBackend(), &memoryCache{}
	options := &CacheConfig{Store: c, Namespace: "project.database", Version: 1, TTL: time.Minute}
	r, err := New(Config{Backend: b, Cache: options})
	if err != nil {
		t.Fatal(err)
	}
	*options = CacheConfig{}
	if _, err := r.ByID(context.Background(), firstID); err != nil {
		t.Fatal(err)
	}
	key := r.state.cached.key("id", firstID)
	var entry cacheEntry
	if err := json.Unmarshal(c.values[key], &entry); err != nil {
		t.Fatal(err)
	}
	for _, expires := range []int64{time.Now().Add(-time.Second).UnixNano(), time.Now().Add(2 * time.Hour).UnixNano()} {
		entry.ExpiresAt = expires
		c.values[key], _ = json.Marshal(entry)
		before := b.calls.Load()
		if _, err := r.ByID(context.Background(), firstID); err != nil || b.calls.Load() != before+1 {
			t.Fatal("expired/future entry reused", err)
		}
	}
}

func TestSitesContextFreezeAndFailedLoadsAreNeverCached(t *testing.T) {
	type marker struct{}
	parent := context.WithValue(context.Background(), marker{}, "original")
	request := httptest.NewRequest("GET", "https://example.test/", nil).WithContext(parent)
	b, c := fixtureBackend(), &memoryCache{}
	r := cachedResolver(t, b, c, 1, false)
	b.query = func(context.Context, string, []any) (db.Rows, error) {
		*request = *request.WithContext(context.WithValue(context.Background(), marker{}, "replaced"))
		return &testRows{values: []Info{fixtureInfo()}}, nil
	}
	ctx, err := r.WithCurrent(request)
	if err != nil || ctx.Value(marker{}) != "original" {
		t.Fatal("request context changed during lookup", ctx, err)
	}
	b, c = fixtureBackend(), &memoryCache{}
	r = cachedResolver(t, b, c, 1, false)
	b.query = func(context.Context, string, []any) (db.Rows, error) { return &testRows{}, nil }
	for range 2 {
		if got, err := r.ByID(context.Background(), firstID); got != (Info{}) || !errors.Is(err, ErrSiteNotConfigured) {
			t.Fatal(got, err)
		}
	}
	if b.calls.Load() != 2 || c.sets != 0 {
		t.Fatal("missing site negatively cached", b.calls.Load(), c.sets)
	}
	b.query = func(context.Context, string, []any) (db.Rows, error) { return nil, errors.New("unavailable") }
	if got, err := r.ByID(context.Background(), firstID); got != (Info{}) || !errors.Is(err, ErrUnavailable) || c.sets != 0 {
		t.Fatal(got, err)
	}
}

func FuzzSitesHostNormalization(f *testing.F) {
	for _, seed := range []string{"EXAMPLE.TEST.:443", "[::1]:8000", "bücher.test", "example.test:65536", "x..test"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, host string) {
		domain, err := requestDomain(host)
		if err != nil {
			if domain != "" {
				t.Fatal("partial invalid host")
			}
			return
		}
		normalized, err := NormalizeDomain(domain)
		if err != nil || normalized != domain || len(domain) > 253 {
			t.Fatal(host, domain, normalized, err)
		}
	})
}
