package admin

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
)

type testRecord struct {
	models.Base
	ID                   int64
	Tenant, Name, Secret string
}

func (*testRecord) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Product", Fields: []models.Field{models.BigAutoField("ID"), models.CharField("Tenant", models.ReadOnly), models.CharField("Name", models.WithMaxLength(100)), models.CharField("Secret", models.WithMaxLength(100))}}
}

type testDB struct {
	mu            sync.Mutex
	records       map[string]testRecord
	logs          []LogEntry
	failAudit     bool
	foreignDelete bool
	sequence      int64
}

func (d *testDB) Scope(_ context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	return &testScope{db: d, tenant: p.ID, site: site}, nil
}

type testScope struct {
	db           *testDB
	tenant, site string
}

func (s *testScope) object(value testRecord) Object {
	value.ModelState().Persisted = true
	record, _ := models.Bind(&value)
	return Object{Record: record, ID: strconv.FormatInt(value.ID, 10), Version: value.Name, Label: value.Name}
}
func (s *testScope) List(_ context.Context, q ListQuery) (Page, error) {
	result := Page{}
	for _, v := range s.db.records {
		if v.Tenant == s.tenant && (q.Search == "" || strings.Contains(v.Name, q.Search)) {
			result.Count++
			result.Objects = append(result.Objects, s.object(v))
		}
	}
	if q.Offset >= len(result.Objects) {
		result.Objects = nil
	} else {
		result.Objects = result.Objects[q.Offset:min(q.Offset+q.Limit, len(result.Objects))]
	}
	return result, nil
}
func (s *testScope) Get(_ context.Context, key string, _ bool) (Object, error) {
	v, ok := s.db.records[key]
	if !ok || v.Tenant != s.tenant {
		return Object{}, ErrNotFound
	}
	return s.object(v), nil
}
func (s *testScope) New(context.Context) (Object, error) {
	value := &testRecord{Tenant: s.tenant, Secret: "server-owned"}
	record, _ := models.Bind(value)
	return Object{Record: record, Label: "New product"}, nil
}
func (s *testScope) Save(_ context.Context, object Object) (Object, error) {
	model, _ := models.Underlying(object.Record)
	value := model.(*testRecord)
	if value.ID == 0 {
		s.db.sequence++
		value.ID = s.db.sequence
	}
	value.ModelState().Persisted = true
	s.db.records[strconv.FormatInt(value.ID, 10)] = *value
	return s.object(*value), nil
}
func (s *testScope) Delete(_ context.Context, object Object) error {
	delete(s.db.records, object.ID)
	return nil
}
func (s *testScope) DeleteAuthorized(ctx context.Context, object Object, authorize func(context.Context, Deletion) error) error {
	graph, err := s.CollectDeletion(ctx, object)
	if err != nil {
		return err
	}
	if err = authorize(ctx, graph); err != nil {
		return err
	}
	return s.Delete(ctx, object)
}
func (s *testScope) History(_ context.Context, key string, _, _ int) ([]LogEntry, error) {
	result := []LogEntry{}
	for _, entry := range s.db.logs {
		if entry.ObjectID == key && entry.Site == s.site && entry.ActorID == s.tenant {
			result = append(result, entry)
		}
	}
	return result, nil
}
func (s *testScope) Audit(_ context.Context, entry LogEntry) error {
	if s.db.failAudit {
		return errors.New("audit down")
	}
	s.db.logs = append(s.db.logs, entry)
	return nil
}
func (s *testScope) Atomic(ctx context.Context, fn func(context.Context) error) error {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	before := map[string]testRecord{}
	for k, v := range s.db.records {
		before[k] = v
	}
	logs := append([]LogEntry(nil), s.db.logs...)
	err := fn(ctx)
	if err != nil {
		s.db.records = before
		s.db.logs = logs
	}
	return err
}
func (s *testScope) CollectDeletion(_ context.Context, object Object) (Deletion, error) {
	if s.db.foreignDelete {
		return Deletion{Objects: []Object{object, s.object(s.db.records["2"])}}, nil
	}
	return Deletion{Objects: []Object{object}}, nil
}

func TestDeletionGraphCannotCrossScope(t *testing.T) {
	site, database := newTestSite(t)
	database.foreignDelete = true
	w := perform(site, "GET", "/admin/shop/product/1/delete/", principal(), nil, nil)
	if w.Code != 403 || strings.Contains(w.Body.String(), "Other tenant") {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(database.records) != 2 {
		t.Fatal("preview mutated data")
	}
}

type jsonModel struct {
	models.Base
	ID   int64
	Data map[string]any
}

func (*jsonModel) Schema() models.Schema {
	return models.Schema{AppLabel: "test", Name: "JSON", Fields: []models.Field{models.BigAutoField("ID"), models.JSONField("Data")}}
}
func TestAuditSnapshotCopiesStructuredData(t *testing.T) {
	model := &jsonModel{ID: 1, Data: map[string]any{"value": []any{1, "1"}}}
	record, _ := models.Bind(model)
	options := ModelAdmin{Schema: model.Schema()}
	before, err := snapshot(options, Object{Record: record})
	if err != nil {
		t.Fatal(err)
	}
	model.Data["value"].([]any)[0] = "1"
	after, err := snapshot(options, Object{Record: record})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff(before, after)) != 1 {
		t.Fatal("type change lost", before, after)
	}
	if before["Data"].(map[string]any)["value"].([]any)[0] == "1" {
		t.Fatal("snapshot aliased mutation")
	}
}

func newTestSite(t *testing.T) (*Site, *testDB) {
	t.Helper()
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(key)}, nil, "admin-edit")
	if err != nil {
		t.Fatal(err)
	}
	database := &testDB{sequence: 2, records: map[string]testRecord{"1": {ID: 1, Tenant: "one", Name: "Public record", Secret: "keep-secret"}, "2": {ID: 2, Tenant: "two", Name: "Other tenant", Secret: "hidden"}}}
	site, err := NewSite(Config{Store: database, Signer: signer, Policy: auth.PolicyFunc(func(_ context.Context, p auth.Principal, _ string, _ auth.Resource) error {
		if !p.Authenticated || !p.Active || !p.Staff {
			return auth.ErrPermissionDenied
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	err = site.Register(ModelAdmin{Schema: (&testRecord{}).Schema(), Fields: []string{"Name", "Secret"}, ReadonlyFields: []string{"Secret"}, ListDisplay: []string{"ID", "Name"}, SearchFields: []string{"Name"}})
	if err != nil {
		t.Fatal(err)
	}
	return site, database
}
func principal() auth.Principal {
	return auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true}
}
func perform(site *Site, method, path string, p auth.Principal, data url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(data.Encode()))
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://example.test")
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	site.ServeHTTP(w, r)
	return w
}
func hidden(t *testing.T, html, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`)
	m := pattern.FindStringSubmatch(html)
	if len(m) != 2 {
		t.Fatalf("missing %s in %s", name, html)
	}
	return m[1]
}

func TestAdminScopeAndNoAnonymousMetadata(t *testing.T) {
	site, _ := newTestSite(t)
	w := perform(site, "GET", "/admin/", auth.Principal{}, nil, nil)
	if w.Code != 401 || strings.Contains(w.Body.String(), "Product") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Other tenant") || !strings.Contains(w.Body.String(), "Public record") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = perform(site, "GET", "/admin/shop/product/2/change/", principal(), nil, nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestNavigationTracksCurrentPageAndDashboardStartsAtTop(t *testing.T) {
	site, _ := newTestSite(t)
	for _, path := range []string{"/admin/shop/product/", "/admin/shop/product/1/change/"} {
		response := perform(site, "GET", path, principal(), nil, nil)
		body := response.Body.String()
		if response.Code != 200 || strings.Contains(body, `class="home-link is-active"`) || !strings.Contains(body, `href="/admin/shop/product/" aria-current="location"`) {
			t.Fatal(path, response.Code, body)
		}
	}
	response := perform(site, "GET", "/admin/", principal(), nil, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `class="home-link is-active" href="/admin/" aria-current="page"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	css, err := embedded.ReadFile("internal/assets/admin.css")
	if err != nil || !strings.Contains(string(css), "margin:0 auto;align-self:start") {
		t.Fatal("dashboard must not use vertical auto margins", err)
	}
}

func TestAssetsAreContentAddressedAndStablePathRevalidates(t *testing.T) {
	site, _ := newTestSite(t)
	css, _ := embedded.ReadFile("internal/assets/admin.css")
	version := fmt.Sprintf("%x", sha256.Sum256(css))
	path := "/admin/assets/admin." + version + ".css"
	page := perform(site, "GET", "/admin/", principal(), nil, nil)
	if !strings.Contains(page.Body.String(), `href="`+path+`"`) {
		t.Fatal("missing content-addressed CSS", page.Body.String())
	}
	get := perform(site, "GET", path, auth.Principal{}, nil, nil)
	if get.Code != 200 || get.Body.String() != string(css) || !strings.Contains(get.Header().Get("Cache-Control"), "immutable") {
		t.Fatal(get.Code, get.Header())
	}
	if len(get.Result().Cookies()) != 0 || get.Header().Get("Vary") != "" {
		t.Fatal("public assets must not carry per-user CSRF state", get.Header())
	}
	unsafe := perform(site, "POST", path, auth.Principal{}, nil, nil)
	if unsafe.Code != 405 || len(unsafe.Result().Cookies()) != 0 {
		t.Fatal("asset method handling", unsafe.Code, unsafe.Header())
	}
	legacy := perform(site, "GET", "/admin/assets/admin.css", auth.Principal{}, nil, nil)
	if legacy.Header().Get("Cache-Control") != "public, no-cache" {
		t.Fatal("stable URL cannot be cached without revalidation")
	}
	r := httptest.NewRequest("GET", "http://example.test"+path, nil)
	r.Header.Set("If-None-Match", get.Header().Get("ETag"))
	w := httptest.NewRecorder()
	site.ServeHTTP(w, r)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	missing := perform(site, "GET", "/admin/assets/admin.unknown.css", auth.Principal{}, nil, nil)
	if missing.Code == 200 {
		t.Fatal("stale fingerprint served current bytes")
	}
}
func TestAdminSaveReadonlyCSRFVersionAndAudit(t *testing.T) {
	site, database := newTestSite(t)
	get := perform(site, "GET", "/admin/shop/product/1/change/", principal(), nil, nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	if strings.Contains(get.Body.String(), `name="Secret"`) {
		t.Fatal("readonly bound as input")
	}
	data := url.Values{"Name": {"Updated"}, "Secret": {"overwritten"}, "csrfmiddlewaretoken": {hidden(t, get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(t, get.Body.String(), "_edit_token")}}
	post := perform(site, "POST", "/admin/shop/product/1/change/", principal(), data, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	if database.records["1"].Name != "Updated" || database.records["1"].Secret != "keep-secret" || len(database.logs) != 1 {
		t.Fatal(database)
	}
	post = perform(site, "POST", "/admin/shop/product/1/change/", principal(), data, get.Result().Cookies())
	if post.Code != 409 {
		t.Fatal("stale token accepted", post.Code)
	}
	delete(data, "csrfmiddlewaretoken")
	post = perform(site, "POST", "/admin/shop/product/1/change/", principal(), data, get.Result().Cookies())
	if post.Code != 403 {
		t.Fatal("missing csrf accepted", post.Code)
	}
}
func TestAdminAuditFailureRollsBackSave(t *testing.T) {
	site, database := newTestSite(t)
	database.failAudit = true
	get := perform(site, "GET", "/admin/shop/product/1/change/", principal(), nil, nil)
	data := url.Values{"Name": {"Lost edit"}, "csrfmiddlewaretoken": {hidden(t, get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(t, get.Body.String(), "_edit_token")}}
	post := perform(site, "POST", "/admin/shop/product/1/change/", principal(), data, get.Result().Cookies())
	if post.Code != 503 || database.records["1"].Name != "Public record" {
		t.Fatal(post.Code, database.records)
	}
}
func TestAdminInvalidFormDoesNotWrite(t *testing.T) {
	site, database := newTestSite(t)
	get := perform(site, "GET", "/admin/shop/product/1/change/", principal(), nil, nil)
	data := url.Values{"Name": {""}, "csrfmiddlewaretoken": {hidden(t, get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(t, get.Body.String(), "_edit_token")}}
	post := perform(site, "POST", "/admin/shop/product/1/change/", principal(), data, get.Result().Cookies())
	if post.Code != 400 || database.records["1"].Name != "Public record" || !strings.Contains(post.Body.String(), "This field is required.") {
		t.Fatal(post.Code, post.Body.String())
	}
}
func TestAdminRegistrationFreezesAndValidatesOptions(t *testing.T) {
	site, _ := newTestSite(t)
	site.Handler()
	if site.Unregister("shop.Product") == nil {
		t.Fatal("site changed after freeze")
	}
	site, _ = newTestSite(t)
	if site.Register(ModelAdmin{Schema: (&testRecord{}).Schema()}) == nil {
		t.Fatal("duplicate registration")
	}
}
