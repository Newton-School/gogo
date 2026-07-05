package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/auth"
	gogohttp "github.com/cybersaksham/gogo/http"
	"github.com/cybersaksham/gogo/models"
	"github.com/cybersaksham/gogo/orm"
	sqlitedialect "github.com/cybersaksham/gogo/orm/dialects/sqlite"

	_ "modernc.org/sqlite"
)

func TestAdminURLsGenerateNamespacedRoutesAndReverse(t *testing.T) {
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}, ModelAdmin{
		CustomURLs: []URLPattern{{Name: "stats", Path: "stats/"}},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}
	names := routeNames(router.Routes())
	wantNames := []string{
		"admin:index_slash_redirect",
		"admin:index",
		"admin:login",
		"admin:logout",
		"admin:password_change",
		"admin:jsi18n",
		"admin:css",
		"admin:js",
		"admin:static",
		"admin:app_list",
		"admin:blog_post_changelist",
		"admin:blog_post_add",
		"admin:blog_post_change",
		"admin:blog_post_delete",
		"admin:blog_post_history",
		"admin:blog_post_autocomplete",
		"admin:blog_post_jsi18n",
		"admin:blog_post_stats",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("route names = %#v, want %#v", names, wantNames)
	}

	reversed, err := router.Reverse("admin:blog_post_change", map[string]any{"object_id": 42})
	if err != nil {
		t.Fatalf("Reverse(change) error = %v", err)
	}
	if reversed != "/admin/blog/post/42/change/" {
		t.Fatalf("Reverse(change) = %q", reversed)
	}
	index, err := router.Reverse("admin:index", nil)
	if err != nil || index != "/admin/" {
		t.Fatalf("Reverse(index) = %q, %v", index, err)
	}
}

func TestAdminSlashlessIndexRedirectsToCanonicalPath(t *testing.T) {
	site := DefaultSite()
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "/admin/" {
		t.Fatalf("slashless admin response = %d location %q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestAdminURLsServeEmbeddedAssets(t *testing.T) {
	site := DefaultSite()
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	tests := []struct {
		path        string
		contentType string
		want        string
	}{
		{"/admin/static/admin.css", "text/css; charset=utf-8", "#container"},
		{"/admin/static/admin.js", "application/javascript; charset=utf-8", "admin-ready"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", test.path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != test.contentType {
			t.Fatalf("%s content type = %q", test.path, got)
		}
		if !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("%s body missing %q", test.path, test.want)
		}
	}
}

func TestAdminIndexRouteRendersRegisteredModels(t *testing.T) {
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, staffAdminRequest(http.MethodGet, "/admin/"))
	if response.Code != http.StatusOK {
		t.Fatalf("index status = %d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	body := response.Body.String()
	for _, want := range []string{"Gogo administration", "Site administration", "blog", "Post", "/admin/blog/post/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("index body missing %q:\n%s", want, body)
		}
	}

	appResponse := httptest.NewRecorder()
	router.ServeHTTP(appResponse, staffAdminRequest(http.MethodGet, "/admin/blog/"))
	if appResponse.Code != http.StatusOK || !strings.Contains(appResponse.Body.String(), "Post") {
		t.Fatalf("app list = (%d, %q)", appResponse.Code, appResponse.Body.String())
	}
}

func TestAdminIndexRoutesHonorModelAdminPermissions(t *testing.T) {
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}, ModelAdmin{
		Hooks: ModelAdminHooks{
			HasModulePermission: func(*http.Request, auth.User) bool { return true },
			HasViewPermission:   func(*http.Request, auth.User) bool { return true },
			HasAddPermission:    func(*http.Request, auth.User) bool { return false },
		},
	}); err != nil {
		t.Fatalf("RegisterMetadata(blog.Post) error = %v", err)
	}
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{AppLabel: "secret", ModelName: "Token", TableName: "secret_token"}, ModelAdmin{
		Hooks: ModelAdminHooks{
			HasModulePermission: func(*http.Request, auth.User) bool { return false },
			HasViewPermission:   func(*http.Request, auth.User) bool { return true },
		},
	}); err != nil {
		t.Fatalf("RegisterMetadata(secret.Token) error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	index := httptest.NewRecorder()
	router.ServeHTTP(index, staffAdminRequest(http.MethodGet, "/admin/"))
	if index.Code != http.StatusOK {
		t.Fatalf("index status = %d body=%s", index.Code, index.Body.String())
	}
	body := index.Body.String()
	for _, want := range []string{"blog", "Post", "/admin/blog/post/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("index body missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"secret", "Token", "/admin/blog/post/add/"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("index body should not contain %q:\n%s", unwanted, body)
		}
	}

	appList := httptest.NewRecorder()
	router.ServeHTTP(appList, staffAdminRequest(http.MethodGet, "/admin/blog/"))
	if appList.Code != http.StatusOK || !strings.Contains(appList.Body.String(), "Post") || strings.Contains(appList.Body.String(), "/admin/blog/post/add/") {
		t.Fatalf("app list = %d body=%s", appList.Code, appList.Body.String())
	}
}

func TestAdminModelRoutesRenderDjangoStylePages(t *testing.T) {
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		Fields:      []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}, {Name: "title", Column: "title"}, {Name: "status", Column: "status"}},
		VerboseName: "post",
	}, ModelAdmin{
		ListDisplay:    []string{"title", "status"},
		SearchFields:   []string{"title"},
		ListFilter:     []string{"status"},
		Fields:         []string{"title", "status"},
		ReadonlyFields: []string{"id"},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	tests := []struct {
		path string
		want []string
	}{
		{"/admin/blog/post/", []string{`<body class="app-blog model-post change-list"`, `/admin/static/admin/css/changelists.css`, `/admin/static/admin/js/filters.js`, `id="changelist"`, `Add post`, `action-checkbox-column`, `searchbar`}},
		{"/admin/blog/post/add/", []string{`<body class="app-blog model-post change-form"`, `/admin/static/admin/css/forms.css`, `/admin/static/admin/js/admin/DateTimeShortcuts.js`, `/admin/static/admin/js/SelectFilter2.js`, `id="post_form"`, `Save and continue editing`, `name="_addanother"`}},
		{"/admin/blog/post/42/change/", []string{`<body class="app-blog model-post change-form"`, `/admin/static/admin/css/forms.css`, `/admin/static/admin/js/admin/DateTimeShortcuts.js`, `/admin/static/admin/js/SelectFilter2.js`, `History`, `Delete`, `name="_save"`}},
		{"/admin/blog/post/42/delete/", []string{`<body class="app-blog model-post delete-confirmation"`, `Are you sure?`, `Yes, I&rsquo;m sure`}},
		{"/admin/blog/post/42/history/", []string{`<body class="app-blog model-post history"`, `Object history`, `doesn&rsquo;t have a change history`}},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, staffAdminRequest(http.MethodGet, test.path))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", test.path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("%s content type = %q", test.path, got)
		}
		for _, want := range test.want {
			if !strings.Contains(response.Body.String(), want) {
				t.Fatalf("%s body missing %q:\n%s", test.path, want, response.Body.String())
			}
		}
	}
	addResponse := httptest.NewRecorder()
	router.ServeHTTP(addResponse, staffAdminRequest(http.MethodGet, "/admin/blog/post/add/"))
	addBody := addResponse.Body.String()
	for _, unwanted := range []string{`/admin/blog/post//delete/`, `class="deletelink"`, `&lt;nil&gt;`} {
		if strings.Contains(addBody, unwanted) {
			t.Fatalf("add form should not contain %q:\n%s", unwanted, addBody)
		}
	}

	autocomplete := httptest.NewRecorder()
	router.ServeHTTP(autocomplete, staffAdminRequest(http.MethodGet, "/admin/blog/post/autocomplete/"))
	if autocomplete.Code != http.StatusOK || autocomplete.Header().Get("Content-Type") != "application/json" || !strings.Contains(autocomplete.Body.String(), `"results"`) {
		t.Fatalf("autocomplete response = %d %q %s", autocomplete.Code, autocomplete.Header().Get("Content-Type"), autocomplete.Body.String())
	}

	jsi18n := httptest.NewRecorder()
	router.ServeHTTP(jsi18n, staffAdminRequest(http.MethodGet, "/admin/blog/post/jsi18n/"))
	if jsi18n.Code != http.StatusOK || !strings.Contains(jsi18n.Header().Get("Content-Type"), "application/javascript") || !strings.Contains(jsi18n.Body.String(), "window.gogoAdminCatalog") {
		t.Fatalf("jsi18n response = %d %q %s", jsi18n.Code, jsi18n.Header().Get("Content-Type"), jsi18n.Body.String())
	}

	rootJSI18N := httptest.NewRecorder()
	router.ServeHTTP(rootJSI18N, staffAdminRequest(http.MethodGet, "/admin/jsi18n/"))
	if rootJSI18N.Code != http.StatusOK || !strings.Contains(rootJSI18N.Header().Get("Content-Type"), "application/javascript") || !strings.Contains(rootJSI18N.Body.String(), "window.gogoAdminCatalog") {
		t.Fatalf("root jsi18n response = %d %q %s", rootJSI18N.Code, rootJSI18N.Header().Get("Content-Type"), rootJSI18N.Body.String())
	}
}

func TestAdminModelRoutesPersistCRUDThroughModelStore(t *testing.T) {
	meta := models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		Fields:      []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}, {Name: "title", Column: "title"}, {Name: "slug", Column: "slug"}, {Name: "created_at", Column: "created_at"}, {Name: "updated_at", Column: "updated_at"}},
		VerboseName: "post",
	}
	database, err := orm.OpenDatabase(t.Context(), orm.DatabaseConfig{Name: orm.DefaultDatabase, Driver: "sqlite", DSN: t.TempDir() + "/admin.sqlite3", Dialect: sqlitedialect.New()})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(t.Context(), `CREATE TABLE blog_post (id bigint PRIMARY KEY, title text NOT NULL, slug text NOT NULL, created_at timestamp, updated_at timestamp)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	site := DefaultSite()
	site.ModelStore = orm.NewMetadataStore(database, meta)
	site.LogStore = NewMemoryLogStore()
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{ListDisplay: []string{"title", "slug"}, Fields: []string{"title", "slug"}}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	add := httptest.NewRecorder()
	router.ServeHTTP(add, staffAdminFormRequest("/admin/blog/post/add/", "title=First&slug=first&_save=Save"))
	if add.Code != http.StatusFound || add.Header().Get("Location") != "/admin/blog/post/" {
		t.Fatalf("add response = %d location=%q body=%s", add.Code, add.Header().Get("Location"), add.Body.String())
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, staffAdminRequest(http.MethodGet, "/admin/blog/post/"))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "First") || !strings.Contains(list.Body.String(), "first") {
		t.Fatalf("changelist response = %d body=%s", list.Code, list.Body.String())
	}

	change := httptest.NewRecorder()
	router.ServeHTTP(change, staffAdminRequest(http.MethodGet, "/admin/blog/post/1/change/"))
	if change.Code != http.StatusOK || !strings.Contains(change.Body.String(), `value="First"`) || !strings.Contains(change.Body.String(), `/admin/blog/post/1/delete/`) {
		t.Fatalf("change response = %d body=%s", change.Code, change.Body.String())
	}

	update := httptest.NewRecorder()
	router.ServeHTTP(update, staffAdminFormRequest("/admin/blog/post/1/change/", "title=Updated&slug=first&_continue=Save+and+continue+editing"))
	if update.Code != http.StatusFound || update.Header().Get("Location") != "/admin/blog/post/1/change/" {
		t.Fatalf("update response = %d location=%q body=%s", update.Code, update.Header().Get("Location"), update.Body.String())
	}

	history := httptest.NewRecorder()
	router.ServeHTTP(history, staffAdminRequest(http.MethodGet, "/admin/blog/post/1/history/"))
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "Changed") || strings.Contains(history.Body.String(), "doesn&rsquo;t have a change history") {
		t.Fatalf("history response = %d body=%s", history.Code, history.Body.String())
	}

	autocomplete := httptest.NewRecorder()
	router.ServeHTTP(autocomplete, staffAdminRequest(http.MethodGet, "/admin/blog/post/autocomplete/?q=Upd"))
	if autocomplete.Code != http.StatusOK || autocomplete.Header().Get("Content-Type") != "application/json" || !strings.Contains(autocomplete.Body.String(), `"id":"1"`) || !strings.Contains(autocomplete.Body.String(), `"text":"Updated"`) {
		t.Fatalf("autocomplete response = %d %q %s", autocomplete.Code, autocomplete.Header().Get("Content-Type"), autocomplete.Body.String())
	}

	deleteResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteResponse, staffAdminFormRequest("/admin/blog/post/1/delete/", "post=yes"))
	if deleteResponse.Code != http.StatusFound || deleteResponse.Header().Get("Location") != "/admin/blog/post/" {
		t.Fatalf("delete response = %d location=%q body=%s", deleteResponse.Code, deleteResponse.Header().Get("Location"), deleteResponse.Body.String())
	}

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, staffAdminRequest(http.MethodGet, "/admin/blog/post/1/change/"))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing change response = %d body=%s", missing.Code, missing.Body.String())
	}
}

func TestAdminModelAddUsesDatabaseGeneratedPrimaryKey(t *testing.T) {
	meta := models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		Fields:      []models.FieldMeta{{Name: "id", Column: "id", Kind: "text", PrimaryKey: true, DBDefault: models.DefaultValue("post-generated")}, {Name: "title", Column: "title", Kind: "text"}, {Name: "created_at", Column: "created_at", Kind: "timestamp", Null: true}, {Name: "updated_at", Column: "updated_at", Kind: "timestamp", Null: true}},
		VerboseName: "post",
	}
	database, err := orm.OpenDatabase(t.Context(), orm.DatabaseConfig{Name: orm.DefaultDatabase, Driver: "sqlite", DSN: t.TempDir() + "/admin-generated.sqlite3", Dialect: sqlitedialect.New()})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(t.Context(), `
		CREATE TABLE blog_post (
			id text PRIMARY KEY DEFAULT 'post-generated',
			title text NOT NULL,
			created_at timestamp,
			updated_at timestamp
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	site := DefaultSite()
	site.ModelStore = orm.NewMetadataStore(database, meta)
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{Fields: []string{"title"}}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	add := httptest.NewRecorder()
	router.ServeHTTP(add, staffAdminFormRequest("/admin/blog/post/add/", "title=Generated&_continue=Save+and+continue+editing"))
	if add.Code != http.StatusFound || add.Header().Get("Location") != "/admin/blog/post/post-generated/change/" {
		t.Fatalf("add response = %d location=%q body=%s", add.Code, add.Header().Get("Location"), add.Body.String())
	}

	change := httptest.NewRecorder()
	router.ServeHTTP(change, staffAdminRequest(http.MethodGet, "/admin/blog/post/post-generated/change/"))
	if change.Code != http.StatusOK || !strings.Contains(change.Body.String(), `value="Generated"`) {
		t.Fatalf("change response = %d body=%s", change.Code, change.Body.String())
	}
}

func TestAdminChangeListUsesQueryCapableModelStore(t *testing.T) {
	meta := models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		Fields:      []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}, {Name: "title", Column: "title"}, {Name: "status", Column: "status"}},
		VerboseName: "post",
	}
	store := &queryAdminStore{
		rows:  []map[string]any{{"id": 2, "title": "Bounded", "status": "published"}},
		total: 42,
	}
	site := DefaultSite()
	site.ModelStore = store
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{
		ListDisplay:  []string{"title", "status"},
		ListFilter:   []string{"status"},
		SearchFields: []string{"title"},
		ListPerPage:  25,
		Ordering:     []string{"-title"},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, staffAdminRequest(http.MethodGet, "/admin/blog/post/?q=bound&status__exact=published&p=2&o=title"))
	if response.Code != http.StatusOK {
		t.Fatalf("changelist response = %d body=%s", response.Code, response.Body.String())
	}
	if !store.queried || store.listed {
		t.Fatalf("store queried=%v listed=%v, want query path only", store.queried, store.listed)
	}
	if store.query.Search != "bound" || store.query.Limit != 25 || store.query.Offset != 25 {
		t.Fatalf("query = %#v", store.query)
	}
	if got := store.query.Filters["status__exact"]; !reflect.DeepEqual(got, []string{"published"}) {
		t.Fatalf("filters = %#v", store.query.Filters)
	}
	if !reflect.DeepEqual(store.query.Ordering, []string{"title"}) || !reflect.DeepEqual(store.query.SearchFields, []string{"title"}) {
		t.Fatalf("query ordering/search fields = %#v", store.query)
	}
	if body := response.Body.String(); !strings.Contains(body, "Bounded") || !strings.Contains(body, "42 posts") {
		t.Fatalf("changelist body missing query result/total:\n%s", body)
	}
}

func TestAdminChangeFormValidatesMetadataFieldKinds(t *testing.T) {
	meta := models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		Fields:      []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}, {Name: "title", Column: "title", Kind: "text"}, {Name: "rank", Column: "rank", Kind: "integer"}},
		VerboseName: "post",
	}
	database, err := orm.OpenDatabase(t.Context(), orm.DatabaseConfig{Name: orm.DefaultDatabase, Driver: "sqlite", DSN: t.TempDir() + "/admin-validation.sqlite3", Dialect: sqlitedialect.New()})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(t.Context(), `CREATE TABLE blog_post (id bigint PRIMARY KEY, title text NOT NULL, rank integer NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	site := DefaultSite()
	site.ModelStore = orm.NewMetadataStore(database, meta)
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{Fields: []string{"title", "rank"}}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, staffAdminFormRequest("/admin/blog/post/add/", "title=Bad&rank=not-a-number&_save=Save"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid add response = %d body=%s", invalid.Code, invalid.Body.String())
	}
	list, err := site.ModelStore.List(t.Context(), meta)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("invalid form should not create rows: %#v", list)
	}
}

func TestAdminChangeListExecutesCustomActions(t *testing.T) {
	meta := models.Metadata{
		AppLabel:    "blog",
		ModelName:   "Post",
		TableName:   "blog_post",
		Fields:      []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}, {Name: "title", Column: "title"}},
		VerboseName: "post",
	}
	database, err := orm.OpenDatabase(t.Context(), orm.DatabaseConfig{Name: orm.DefaultDatabase, Driver: "sqlite", DSN: t.TempDir() + "/admin-actions.sqlite3", Dialect: sqlitedialect.New()})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(t.Context(), `CREATE TABLE blog_post (id bigint PRIMARY KEY, title text NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	store := orm.NewMetadataStore(database, meta)
	if _, err := store.Create(t.Context(), meta, map[string]any{"title": "Action target"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var selected []map[string]any
	site := DefaultSite()
	site.ModelStore = store
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{
		ListDisplay: []string{"title"},
		ActionDefinitions: []Action{{
			Name:  "mark_reviewed",
			Label: "Mark reviewed",
			Handler: func(ctx ActionContext) (ActionResult, error) {
				selected = append([]map[string]any(nil), ctx.Selected...)
				return ActionResult{Message: "Marked reviewed"}, nil
			},
		}},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, staffAdminFormRequest("/admin/blog/post/", "action=mark_reviewed&_selected_action=1&index=0"))
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/admin/blog/post/" {
		t.Fatalf("action response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if len(selected) != 1 || selected[0]["title"] != "Action target" {
		t.Fatalf("selected rows = %#v", selected)
	}
}

func TestAdminModelPostRejectsMissingCSRF(t *testing.T) {
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields:    []models.FieldMeta{{Name: "id", Column: "id", PrimaryKey: true}, {Name: "title", Column: "title"}},
	}
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{Fields: []string{"title"}}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	request := staffAdminRequest(http.MethodPost, "/admin/blog/post/add/")
	body := "title=First&_save=Save"
	request.Body = io.NopCloser(strings.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing csrf admin POST response = %d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminAuthViewsRenderDjangoStyleForms(t *testing.T) {
	site := DefaultSite()
	config := AuthViewConfig{Site: site}

	for _, test := range []struct {
		name    string
		handler http.Handler
		request *http.Request
		want    []string
	}{
		{
			name:    "login",
			handler: LoginView(config),
			request: httptest.NewRequest(http.MethodGet, "/admin/login/?next=/admin/", nil),
			want:    []string{`<body class="login"`, `id="login-form"`, `csrfmiddlewaretoken`, `Log in`},
		},
		{
			name:    "password change",
			handler: PasswordChangeView(config),
			request: staffAdminRequest(http.MethodGet, "/admin/password_change/"),
			want:    []string{`<body class="dashboard password-change"`, `id="password-change-form"`, `old_password`, `new_password`, `Change my password`},
		},
	} {
		response := httptest.NewRecorder()
		test.handler.ServeHTTP(response, test.request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", test.name, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("%s content type = %q", test.name, got)
		}
		if !strings.Contains(response.Header().Get("Set-Cookie"), gogohttp.CSRFCookieName+"=") {
			t.Fatalf("%s Set-Cookie = %q, want csrf cookie", test.name, response.Header().Get("Set-Cookie"))
		}
		if strings.Contains(response.Body.String(), `name="csrfmiddlewaretoken" value=""`) {
			t.Fatalf("%s rendered empty csrf token:\n%s", test.name, response.Body.String())
		}
		for _, want := range test.want {
			if !strings.Contains(response.Body.String(), want) {
				t.Fatalf("%s body missing %q:\n%s", test.name, want, response.Body.String())
			}
		}
	}
}

func TestAdminRoutesRequireActiveStaffUser(t *testing.T) {
	site := DefaultSite()
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if unauthenticated.Code != http.StatusFound || unauthenticated.Header().Get("Location") != "/admin/login/?next=%2Fadmin%2F" {
		t.Fatalf("unauthenticated admin response = %d location %q", unauthenticated.Code, unauthenticated.Header().Get("Location"))
	}

	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/admin/login/", nil))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}

	plainUser := auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 2, IsActive: true, Authenticated: true},
		Username:         "plain",
	}}
	forbiddenRequest := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	forbiddenRequest = forbiddenRequest.WithContext(auth.ContextWithUser(forbiddenRequest.Context(), plainUser))
	forbidden := httptest.NewRecorder()
	router.ServeHTTP(forbidden, forbiddenRequest)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("non-staff admin response = %d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func staffAdminRequest(method, path string) *http.Request {
	staff := auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true},
		Username:         "staff",
		IsStaff:          true,
	}}
	request := httptest.NewRequest(method, path, nil)
	return request.WithContext(auth.ContextWithUser(request.Context(), staff))
}

func staffAdminFormRequest(path, body string) *http.Request {
	token := "test-csrf-token"
	request := staffAdminRequest(http.MethodPost, path)
	if !strings.Contains(body, gogohttp.CSRFFormFieldName+"=") {
		if body != "" {
			body += "&"
		}
		body += gogohttp.CSRFFormFieldName + "=" + token
	}
	request.Body = io.NopCloser(strings.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: gogohttp.CSRFCookieName, Value: token})
	return request
}

func routeNames(routes []gogohttp.Route) []string {
	names := make([]string, len(routes))
	for i, route := range routes {
		names[i] = route.Name
	}
	return names
}

type queryAdminStore struct {
	rows    []map[string]any
	total   int
	query   models.ObjectQuery
	queried bool
	listed  bool
}

func (s *queryAdminStore) List(context.Context, models.Metadata) ([]map[string]any, error) {
	s.listed = true
	return append([]map[string]any(nil), s.rows...), nil
}

func (s *queryAdminStore) Query(_ context.Context, _ models.Metadata, query models.ObjectQuery) (models.ObjectQueryResult, error) {
	s.queried = true
	s.query = query
	return models.ObjectQueryResult{Rows: append([]map[string]any(nil), s.rows...), Total: s.total}, nil
}

func (s *queryAdminStore) Get(context.Context, models.Metadata, string) (map[string]any, bool, error) {
	return nil, false, nil
}

func (s *queryAdminStore) Create(context.Context, models.Metadata, map[string]any) (map[string]any, error) {
	return nil, nil
}

func (s *queryAdminStore) Update(context.Context, models.Metadata, string, map[string]any, bool) (map[string]any, error) {
	return nil, nil
}

func (s *queryAdminStore) Delete(context.Context, models.Metadata, string) error {
	return nil
}
