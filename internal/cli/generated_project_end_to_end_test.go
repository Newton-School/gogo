//go:build integration

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/auth"
)

func TestGeneratedProjectEndToEndVerification(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sampleproject")
	if err := NewStartprojectCommand().Run(context.Background(), []string{"sampleproject", target}); err != nil {
		t.Fatalf("startproject error = %v", err)
	}
	for _, appName := range []string{"accounts", "blog"} {
		appTarget := filepath.Join(target, "apps", appName)
		if err := NewStartappCommand().Run(context.Background(), []string{appName, appTarget}); err != nil {
			t.Fatalf("startapp %s error = %v", appName, err)
		}
	}

	staticRoot := filepath.Join(target, "staticfiles")
	mediaRoot := filepath.Join(target, "media")
	if err := os.MkdirAll(staticRoot, 0o755); err != nil {
		t.Fatalf("mkdir static root: %v", err)
	}
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		t.Fatalf("mkdir media root: %v", err)
	}
	writeTextFile(t, filepath.Join(staticRoot, "staticfiles.json"), "{}\n")
	writeTextFile(t, filepath.Join(target, ".env"), generatedEndToEndEnv(staticRoot, mediaRoot))
	writeTextFile(t, filepath.Join(target, "apps", "accounts", "author_models.go"), generatedAccountsAuthorModelSource())
	writeTextFile(t, filepath.Join(target, "apps", "blog", "post_models.go"), generatedBlogPostModelSource())
	writeTextFile(t, filepath.Join(target, "end_to_end_test.go"), generatedEndToEndTestSource())

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	runGeneratedCommand(t, target, "go", "mod", "edit", "-replace", "github.com/cybersaksham/gogo="+filepath.ToSlash(repoRoot))
	runGeneratedCommand(t, target, "go", "mod", "tidy")

	withWorkingDirectory(t, filepath.Join(target, "apps"), func() {
		for _, appName := range []string{"accounts", "blog"} {
			var stdout bytes.Buffer
			if err := NewRoot().Execute(context.Background(), []string{"makemigrations", "--app", appName, "--name", "end_to_end", "--check", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("makemigrations check %s error = %v", appName, err)
			}
			want := "no changes detected"
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("makemigrations %s stdout = %q, want %q", appName, stdout.String(), want)
			}
		}
	})

	withWorkingDirectory(t, target, func() {
		var migrateStdout bytes.Buffer
		if err := NewRoot().Execute(context.Background(), []string{"migrate", "--database", "default"}, &migrateStdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("migrate error = %v", err)
		}
		if !strings.Contains(migrateStdout.String(), "applied migrations on database default") {
			t.Fatalf("migrate stdout = %q", migrateStdout.String())
		}
	})

	store, _ := auth.NewMemoryUserStore()
	if err := NewCreateSuperuserCommand(store).Run(context.Background(), []string{
		"--username", "admin",
		"--email", "admin@example.com",
		"--password", "CorrectHorseBatteryStaple42",
		"--noinput",
	}); err != nil {
		t.Fatalf("createsuperuser error = %v", err)
	}
	user, ok, err := store.FindByUsername(context.Background(), "admin")
	if err != nil || !ok || !user.IsStaff || !user.IsSuperuser {
		t.Fatalf("created superuser = %#v, ok=%v, err=%v", user, ok, err)
	}

	withWorkingDirectory(t, target, func() {
		var runserver RunserverConfig
		command := NewRunserverCommand(func(_ context.Context, config RunserverConfig) error {
			runserver = config
			return nil
		})
		if err := command.Run(context.Background(), []string{"--addr", "127.0.0.1:0"}); err != nil {
			t.Fatalf("runserver error = %v", err)
		}
		if runserver.Addr != "127.0.0.1:0" || runserver.Settings.Env != "production" {
			t.Fatalf("runserver config = %#v", runserver)
		}

		var deployStdout bytes.Buffer
		if err := NewRoot().Execute(context.Background(), []string{"check", "--deploy", "--tag", "deploy"}, &deployStdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("deploy check error = %v\n%s", err, deployStdout.String())
		}
		if !strings.Contains(deployStdout.String(), "INFO deploy production deploy checks passed") {
			t.Fatalf("deploy check stdout = %q", deployStdout.String())
		}
	})

	runGeneratedCommand(t, target, "go", "test", "./...")
	assertNoInternalFrameworkImports(t, target)
}

func generatedEndToEndEnv(staticRoot, mediaRoot string) string {
	return `
GOGO_ENV=production
GOGO_SECRET_KEY=8aUQh2zR7mN4pL6vCx9YtB3sWk5dF1gH
GOGO_DEBUG=false
GOGO_INSTALLED_APPS=gogo.contrib.sites,gogo.contrib.humanize
GOGO_ROOT_URLCONF=sampleproject.urls
GOGO_DEFAULT_AUTO_FIELD=BigAutoField
GOGO_TIME_ZONE=UTC
GOGO_LANGUAGE_CODE=en-us
DATABASE_URL=sqlite://./db.sqlite3
GOGO_HTTP_ADDR=127.0.0.1:0
GOGO_STATIC_URL=/static/
GOGO_STATIC_ROOT=` + filepath.ToSlash(staticRoot) + `
GOGO_MEDIA_URL=/media/
GOGO_MEDIA_ROOT=` + filepath.ToSlash(mediaRoot) + `
GOGO_TEMPLATE_DIRS=templates
GOGO_BROKER_URL=
GOGO_RESULT_BACKEND=
GOGO_CACHE_URL=memory://
GOGO_EMAIL_URL=smtp://mail:1025
GOGO_SESSION_COOKIE_NAME=gogo_sessionid
GOGO_SESSION_COOKIE_SECURE=true
GOGO_CSRF_COOKIE_NAME=gogo_csrftoken
GOGO_CSRF_COOKIE_SECURE=true
GOGO_CSRF_TRUSTED_ORIGINS=https://example.com
GOGO_ALLOWED_HOSTS=example.com,localhost,127.0.0.1
GOGO_HTTPS_ENABLED=true
GOGO_ADMIN_PATH=/staff-admin
GOGO_ADMIN_PATH_REVIEWED=true
GOGO_DEPLOY_MIGRATIONS_APPLIED=true
GOGO_DEPLOY_STATIC_COLLECTED=true
GOGO_PASSWORD_RESET_ENABLED=true
`
}

func generatedAccountsAuthorModelSource() string {
	return `package accounts

import "github.com/cybersaksham/gogo/models"

type Author struct {
	models.BaseModel
	Email string
	Name  string
}

func (Author) ModelMeta() models.Metadata {
	return models.Metadata{
		AppLabel:   AppLabel,
		ModelName:  "Author",
		TableName:  AppLabel + "_author",
		DBTable:    AppLabel + "_author",
		VerboseName: "author",
		VerboseNamePlural: "authors",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "email", Column: "email"},
			{Name: "name", Column: "name"},
			{Name: "created_at", Column: "created_at"},
			{Name: "updated_at", Column: "updated_at"},
		},
		Indexes: []models.Index{
			{Name: AppLabel + "_author_name_idx", Fields: []models.IndexField{models.Asc("name")}},
		},
		Constraints: []models.Constraint{
			{Name: AppLabel + "_author_email_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("email")}},
		},
	}
}
`
}

func generatedBlogPostModelSource() string {
	return `package blog

import "github.com/cybersaksham/gogo/models"

type Post struct {
	models.BaseModel
	AuthorID int64
	Title    string
	Slug     string
	Body     string
}

func (Post) ModelMeta() models.Metadata {
	return models.Metadata{
		AppLabel:   AppLabel,
		ModelName:  "Post",
		TableName:  AppLabel + "_post",
		DBTable:    AppLabel + "_post",
		VerboseName: "post",
		VerboseNamePlural: "posts",
		Ordering: []string{"-created_at", "title"},
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "author", Column: "author_id", RelationTarget: "accounts.Author", DeleteBehavior: "cascade"},
			{Name: "title", Column: "title"},
			{Name: "slug", Column: "slug"},
			{Name: "body", Column: "body"},
			{Name: "created_at", Column: "created_at"},
			{Name: "updated_at", Column: "updated_at"},
		},
		Indexes: []models.Index{
			{Name: AppLabel + "_post_author_created_idx", Fields: []models.IndexField{models.Asc("author"), models.Desc("created_at")}},
		},
		Constraints: []models.Constraint{
			{Name: AppLabel + "_post_author_slug_uniq", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("author"), models.Asc("slug")}},
		},
	}
}
`
}

func generatedEndToEndTestSource() string {
	return `package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	accounts "sampleproject/apps/accounts"
	blog "sampleproject/apps/blog"
	project "sampleproject/sampleproject"

	"github.com/cybersaksham/gogo/admin"
	"github.com/cybersaksham/gogo/api"
	"github.com/cybersaksham/gogo/app"
	"github.com/cybersaksham/gogo/models"
	"github.com/cybersaksham/gogo/migrations"
	"github.com/cybersaksham/gogo/queue"
	"github.com/cybersaksham/gogo/queue/backends"
	"github.com/cybersaksham/gogo/queue/brokers"
	"github.com/cybersaksham/gogo/queue/canvas"
	"github.com/cybersaksham/gogo/templates"
)

func TestGeneratedEndToEndSurface(t *testing.T) {
	ctx := context.Background()

	router, err := project.NewRouter()
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("homepage status = %d", response.Code)
	}

	verifyModelMetadata(t)
	verifyAppRegistry(t, ctx)
	verifyAdminRegistration(t)
	verifyAPIViewSets(t, ctx)
	verifyFormsAndTemplates(t)
	verifyQueueWorkerBeatAndCanvas(t, ctx)
}

func verifyModelMetadata(t *testing.T) {
	t.Helper()
	registry := models.NewRegistry()
	if err := registry.Register(accounts.Author{}); err != nil {
		t.Fatalf("register Author model: %v", err)
	}
	if err := registry.Register(blog.Post{}); err != nil {
		t.Fatalf("register Post model: %v", err)
	}
	post, ok := registry.Lookup("blog.Post")
	if !ok {
		t.Fatal("blog.Post metadata missing")
	}
	if len(post.Fields) != 7 || post.Fields[1].RelationTarget != "accounts.Author" || post.Fields[1].DeleteBehavior != "cascade" {
		t.Fatalf("post fields = %#v", post.Fields)
	}
	if len(post.Indexes) != 1 || !reflect.DeepEqual(post.Indexes[0].FieldNames(), []string{"author", "created_at"}) {
		t.Fatalf("post indexes = %#v", post.Indexes)
	}
	if len(post.Constraints) != 1 || post.Constraints[0].Name != "blog_post_author_slug_uniq" {
		t.Fatalf("post constraints = %#v", post.Constraints)
	}
	state := migrations.StateFromRegistry(registry)
	postState := state.Models["blog.Post"]
	foundFK := false
	for _, constraint := range postState.Constraints {
		if constraint.Type == "foreign_key" && constraint.Fields[0] == "author_id" && constraint.ReferencesTable == "accounts_author" {
			foundFK = true
		}
	}
	if !foundFK {
		t.Fatalf("post migration constraints = %#v", postState.Constraints)
	}
}

func verifyAppRegistry(t *testing.T, ctx context.Context) {
	t.Helper()
	appRegistry := app.NewRegistry()
	for _, ready := range []func(context.Context, *app.Registry) error{
		accounts.NewConfig().Ready,
		blog.NewConfig().Ready,
	} {
		if err := ready(ctx, appRegistry); err != nil {
			t.Fatalf("app ready error = %v", err)
		}
	}
	if len(appRegistry.Models()) != 2 || len(appRegistry.Admin()) != 2 || len(appRegistry.APIRoutes()) != 2 || len(appRegistry.Tasks()) != 2 || len(appRegistry.Migrations()) != 2 {
		t.Fatalf("app registry incomplete: models=%d admin=%d api=%d tasks=%d migrations=%d", len(appRegistry.Models()), len(appRegistry.Admin()), len(appRegistry.APIRoutes()), len(appRegistry.Tasks()), len(appRegistry.Migrations()))
	}
}

func verifyAdminRegistration(t *testing.T) {
	t.Helper()
	registry := admin.NewRegistry()
	if err := accounts.RegisterAdmin(registry); err != nil {
		t.Fatalf("accounts RegisterAdmin() error = %v", err)
	}
	if err := blog.RegisterAdmin(registry); err != nil {
		t.Fatalf("blog RegisterAdmin() error = %v", err)
	}
	if err := registry.RegisterMetadata(accounts.Author{}.ModelMeta(), admin.ModelAdmin{ListDisplay: []string{"email", "name"}}); err != nil {
		t.Fatalf("register Author admin: %v", err)
	}
	if err := registry.RegisterMetadata(blog.Post{}.ModelMeta(), admin.ModelAdmin{ListDisplay: []string{"title", "author", "updated_at"}, SearchFields: []string{"title", "slug"}}); err != nil {
		t.Fatalf("register Post admin: %v", err)
	}
	for _, label := range []string{"accounts.Item", "accounts.Author", "blog.Item", "blog.Post"} {
		if !registry.IsRegistered(label) {
			t.Fatalf("admin registry missing %s", label)
		}
	}
	site := project.NewAdminSite()
	if site.ModelRegistry == nil || site.URLPrefix != "/admin" {
		t.Fatalf("admin site = %#v", site)
	}
}

func verifyAPIViewSets(t *testing.T, ctx context.Context) {
	t.Helper()
	router := api.NewRouter(api.WithAPIPrefix("api"))
	if err := accounts.RegisterAPI(router); err != nil {
		t.Fatalf("accounts RegisterAPI() error = %v", err)
	}
	if err := blog.RegisterAPI(router); err != nil {
		t.Fatalf("blog RegisterAPI() error = %v", err)
	}

	viewset := &api.ModelViewSet{
		Store: &memoryPostStore{rows: []map[string]any{{"id": 1, "title": "Hello", "slug": "hello", "author": 7}}},
		Serializer: api.NewSerializer(
			api.IntegerField("id", api.FieldOptions{ReadOnly: true}),
			api.StringField("title", api.FieldOptions{Required: true}),
			api.SlugField("slug", api.FieldOptions{Required: true}),
			api.PrimaryKeyRelatedField("author", api.FieldOptions{Required: true}),
		),
	}
	viewset.RegisterAction("published", api.ViewSetAction{
		Detail:  true,
		Methods: []string{http.MethodGet},
		Handler: func(context.Context, *api.Request) api.Response {
			return api.JSON(http.StatusOK, map[string]any{"ok": true})
		},
	})
	if err := router.Register("blog/posts", "blog-post", viewset); err != nil {
		t.Fatalf("viewset register error = %v", err)
	}

	for _, path := range []string{"/api/accounts/items/", "/api/blog/items/", "/api/blog/posts/", "/api/blog/posts/1/", "/api/blog/posts/1/published/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func verifyFormsAndTemplates(t *testing.T) {
	t.Helper()
	form := blog.NewItemForm(map[string]any{"name": "Hello", "slug": "hello"})
	if !form.IsValid() || form.CleanedData["slug"] != "hello" {
		t.Fatalf("form valid=%v data=%#v errors=%#v", form.IsValid(), form.CleanedData, form.Errors())
	}
	engine := templates.NewEngine(templates.WithTemplates(map[string]string{
		"post": "<article><h1>{{.title}}</h1><p>{{.author}}</p></article>",
	}))
	rendered, err := engine.Render("post", templates.Context{"title": "Hello", "author": "Admin"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if rendered != "<article><h1>Hello</h1><p>Admin</p></article>" {
		t.Fatalf("rendered template = %q", rendered)
	}
}

func verifyQueueWorkerBeatAndCanvas(t *testing.T, ctx context.Context) {
	t.Helper()
	queueApp := queue.NewApp(queue.AppOptions{})
	if err := accounts.RegisterTasks(queueApp); err != nil {
		t.Fatalf("accounts RegisterTasks() error = %v", err)
	}
	if err := blog.RegisterTasks(queueApp); err != nil {
		t.Fatalf("blog RegisterTasks() error = %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})

	workerMessage, err := queueApp.SendTask(ctx, broker, queue.NewSignature("blog.example"), queue.SendOptions{ID: "worker-task"})
	if err != nil {
		t.Fatalf("SendTask() error = %v", err)
	}
	worker := queue.NewWorker(queueApp, broker, backend, queue.WorkerOptions{Queues: []string{"default"}, Concurrency: 1})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("worker RunOnce() error = %v", err)
	}
	result, err := backend.GetResult(ctx, workerMessage.Envelope.ID)
	if err != nil || result.State != queue.StateSuccess || result.Result != "ok" {
		t.Fatalf("worker result = %#v, err=%v", result, err)
	}

	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := queue.NewMemoryScheduleStore(queue.MemoryScheduleStoreOptions{Now: func() time.Time { return now }})
	if err := store.Save(ctx, queue.ScheduleEntry{
		Name:      "blog-every-minute",
		Signature: queue.NewSignature("blog.example"),
		Schedule:  queue.IntervalSchedule{Every: time.Minute, StartAt: now.Add(-time.Minute)},
		Enabled:   true,
	}); err != nil {
		t.Fatalf("schedule save error = %v", err)
	}
	beat := queue.NewBeat(queueApp, broker, store, queue.BeatOptions{Now: func() time.Time { return now }})
	enqueued, err := beat.Tick(ctx)
	if err != nil || enqueued != 1 {
		t.Fatalf("beat Tick() = %d, %v", enqueued, err)
	}

	chain, err := canvas.NewChain(
		canvas.Task(queue.NewSignature("blog.example")),
		canvas.Task(queue.NewSignature("accounts.example")),
	).ApplyAsync(ctx, canvas.ApplyOptions{App: queueApp, Broker: broker, Backend: backend})
	if err != nil || len(chain.TaskIDs) != 2 {
		t.Fatalf("chain result = %#v, err=%v", chain, err)
	}

	group := canvas.NewGroup(
		canvas.Task(queue.NewSignature("blog.example")),
		canvas.Task(queue.NewSignature("accounts.example")),
	)
	groupResult, err := group.ApplyAsync(ctx, canvas.ApplyOptions{App: queueApp, Broker: broker, Backend: backend, GroupID: "group-1"})
	if err != nil || groupResult.GroupID != "group-1" || len(groupResult.TaskIDs) != 2 {
		t.Fatalf("group result = %#v, err=%v", groupResult, err)
	}
	storedGroup, err := backend.GroupResult(ctx, "group-1", groupResult.TaskIDs)
	if err != nil || len(storedGroup.Children) != 2 {
		t.Fatalf("stored group = %#v, err=%v", storedGroup, err)
	}

	chord := canvas.NewChord(group, canvas.Task(queue.NewSignature("blog.example")))
	chordResult, err := chord.Complete(ctx, canvas.ApplyOptions{App: queueApp, Broker: broker, Backend: backend, ChordID: "chord-1"}, []any{"ok"})
	if err != nil || chordResult.ChordID != "chord-1" || len(chordResult.TaskIDs) != 1 {
		t.Fatalf("chord result = %#v, err=%v", chordResult, err)
	}

	infos, err := broker.InspectQueues(ctx)
	if err != nil {
		t.Fatalf("inspect queues error = %v", err)
	}
	if len(infos) == 0 || infos[0].Ready == 0 {
		t.Fatalf("expected queued canvas/beat messages, got %#v", infos)
	}
}

type memoryPostStore struct {
	rows []map[string]any
}

func (s *memoryPostStore) List(context.Context, *api.Request) ([]map[string]any, error) {
	return cloneRows(s.rows), nil
}

func (s *memoryPostStore) Retrieve(_ context.Context, _ *api.Request, id string) (map[string]any, error) {
	for _, row := range s.rows {
		if fmt.Sprint(row["id"]) == id {
			return cloneRow(row), nil
		}
	}
	return nil, api.ErrNotFound
}

func (s *memoryPostStore) Create(_ context.Context, _ *api.Request, data map[string]any) (map[string]any, error) {
	row := cloneRow(data)
	row["id"] = len(s.rows) + 1
	s.rows = append(s.rows, row)
	return cloneRow(row), nil
}

func (s *memoryPostStore) Update(_ context.Context, _ *api.Request, id string, data map[string]any, partial bool) (map[string]any, error) {
	for index, row := range s.rows {
		if fmt.Sprint(row["id"]) != id {
			continue
		}
		if !partial {
			row = map[string]any{"id": s.rows[index]["id"]}
		}
		for key, value := range data {
			row[key] = value
		}
		s.rows[index] = row
		return cloneRow(row), nil
	}
	return nil, api.ErrNotFound
}

func (s *memoryPostStore) Destroy(_ context.Context, _ *api.Request, id string) error {
	for index, row := range s.rows {
		if fmt.Sprint(row["id"]) == id {
			s.rows = append(s.rows[:index], s.rows[index+1:]...)
			return nil
		}
	}
	return api.ErrNotFound
}

func cloneRows(rows []map[string]any) []map[string]any {
	cloned := make([]map[string]any, len(rows))
	for index, row := range rows {
		cloned[index] = cloneRow(row)
	}
	return cloned
}

func cloneRow(row map[string]any) map[string]any {
	cloned := make(map[string]any, len(row))
	for key, value := range row {
		cloned[key] = value
	}
	return cloned
}
`
}
