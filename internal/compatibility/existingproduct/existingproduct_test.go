package existingproduct

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/admin"
	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/contrib/postgres/vector"
	gogohttp "github.com/cybersaksham/gogo/http"
	"github.com/cybersaksham/gogo/internal/schema"
	"github.com/cybersaksham/gogo/management"
	"github.com/cybersaksham/gogo/migrations"
	"github.com/cybersaksham/gogo/migrations/operations"
	"github.com/cybersaksham/gogo/models"
	"github.com/cybersaksham/gogo/orm"
	postgresdialect "github.com/cybersaksham/gogo/orm/dialects/postgres"
	sqlitedialect "github.com/cybersaksham/gogo/orm/dialects/sqlite"
	"github.com/cybersaksham/gogo/queue"
	"github.com/cybersaksham/gogo/queue/backends"
	_ "github.com/cybersaksham/gogo/queue/backends/redis"
	"github.com/cybersaksham/gogo/queue/brokers"
	_ "github.com/cybersaksham/gogo/queue/brokers/redis"

	_ "modernc.org/sqlite"
)

func TestExistingProductHTTPAdminAndQueueCompatibility(t *testing.T) {
	router := gogohttp.NewRouter()
	if err := router.HandleHTTP("legacy-detail", "/legacy/<slug:slug>/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		_, _ = io.WriteString(w, r.PathValue("slug")+"|"+user.Username)
	}), http.MethodGet); err != nil {
		t.Fatalf("HandleHTTP legacy route: %v", err)
	}
	if err := router.Handle("legacy-stream", "/events/", func(context.Context, *gogohttp.Request) gogohttp.Response {
		return gogohttp.Stream("text/plain", func(w io.Writer) error {
			_, err := io.WriteString(w, "chunk-1\nchunk-2")
			return err
		})
	}, http.MethodGet); err != nil {
		t.Fatalf("Handle stream route: %v", err)
	}
	if err := router.HandleHTTP("legacy-upgrade", "/legacy-upgrade/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacker unavailable", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack legacy upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: legacy\r\n\r\nupgraded")
		_ = rw.Flush()
	}), http.MethodGet); err != nil {
		t.Fatalf("HandleHTTP legacy upgrade route: %v", err)
	}

	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, Authenticated: true}, Username: "legacy-admin", IsStaff: true}}
	request := httptest.NewRequest(http.MethodGet, "/legacy/order-1/", nil)
	request = request.WithContext(auth.ContextWithUser(request.Context(), user))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Body.String() != "order-1|legacy-admin" {
		t.Fatalf("legacy raw response = %q", response.Body.String())
	}

	stream := httptest.NewRecorder()
	router.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/events/", nil))
	if stream.Body.String() != "chunk-1\nchunk-2" {
		t.Fatalf("stream body = %q", stream.Body.String())
	}

	server := httptest.NewServer(router)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("dial legacy upgrade server: %v", err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /legacy-upgrade/ HTTP/1.1\r\nHost: example.test\r\nConnection: Upgrade\r\nUpgrade: legacy\r\n\r\n")
	upgradeResponse, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read legacy upgrade response: %v", err)
	}
	defer upgradeResponse.Body.Close()
	if upgradeResponse.StatusCode != http.StatusSwitchingProtocols || upgradeResponse.Header.Get("Upgrade") != "legacy" {
		t.Fatalf("legacy upgrade response = %d %q", upgradeResponse.StatusCode, upgradeResponse.Header.Get("Upgrade"))
	}

	managed := false
	meta := legacyOrderMetadata(managed)
	registry := admin.NewRegistry()
	if err := registry.RegisterMetadata(meta, admin.ModelAdmin{AllowUnmanaged: true, ReadOnly: true, ReadonlyFields: []string{"id"}}); err != nil {
		t.Fatalf("register unmanaged read-only admin: %v", err)
	}
	registered, ok := registry.GetAdmin("legacy.Order")
	if !ok || !registered.ReadOnly || len(registered.ReadonlyFields) != 1 {
		t.Fatalf("unmanaged admin registration = %#v, ok=%v", registered, ok)
	}

	queueApp := queue.NewApp(queue.AppOptions{})
	if _, err := queueApp.RegisterTask("legacy.sync", func(context.Context, ...any) (any, error) {
		return "synced", nil
	}, queue.TaskOptions{}); err != nil {
		t.Fatalf("register queue task: %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	message, err := queueApp.SendTask(context.Background(), broker, queue.NewSignature("legacy.sync"), queue.SendOptions{ID: "legacy-sync"})
	if err != nil {
		t.Fatalf("send memory task: %v", err)
	}
	worker := queue.NewWorker(queueApp, broker, backend, queue.WorkerOptions{Queues: []string{"default"}})
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("memory worker: %v", err)
	}
	result, err := backend.GetResult(context.Background(), message.Envelope.ID)
	if err != nil || result.Result != "synced" {
		t.Fatalf("memory queue result = %#v, err=%v", result, err)
	}

	if redisURL := redisTestURL(); redisURL != "" {
		redisBroker, err := queue.NewBrokerFromURL(queue.RuntimeConfig{BrokerURL: redisURL})
		if err != nil {
			t.Fatalf("redis broker factory: %v", err)
		}
		defer redisBroker.Close()
		redisBackend, err := queue.NewResultBackendFromURL(queue.RuntimeConfig{ResultBackend: redisURL})
		if err != nil {
			t.Fatalf("redis backend factory: %v", err)
		}
		if closer, ok := redisBackend.(interface{ Close() error }); ok {
			defer closer.Close()
		}
		if pinger, ok := redisBroker.(interface{ Ping(context.Context) error }); ok {
			if err := pinger.Ping(context.Background()); err != nil {
				t.Skipf("Redis configured but unreachable: %v", err)
			}
		}
		redisMessage, err := queueApp.SendTask(context.Background(), redisBroker, queue.NewSignature("legacy.sync"), queue.SendOptions{ID: "legacy-redis-sync"})
		if err != nil {
			t.Fatalf("send redis task: %v", err)
		}
		redisWorker := queue.NewWorker(queueApp, redisBroker, redisBackend, queue.WorkerOptions{Queues: []string{"default"}})
		if err := redisWorker.RunOnce(context.Background()); err != nil {
			t.Fatalf("redis worker: %v", err)
		}
		if result, err := redisBackend.GetResult(context.Background(), redisMessage.Envelope.ID); err != nil || result.Result != "synced" {
			t.Fatalf("redis queue result = %#v, err=%v", result, err)
		}
	}
}

func TestExistingProductModelAdoptionFrameworkFixes(t *testing.T) {
	ctx := context.Background()
	database, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "sqlite",
		DSN:     filepath.Join(t.TempDir(), "adoption.sqlite3"),
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(ctx, `
		CREATE TABLE legacy_product (
			id text PRIMARY KEY DEFAULT 'legacy-product-id',
			owner_id text NOT NULL,
			embedding text,
			name text NOT NULL
		)
	`); err != nil {
		t.Fatalf("create legacy product: %v", err)
	}

	productMeta := legacyProductMetadata()
	store := orm.NewMetadataStore(database, productMeta)
	created, err := store.Create(ctx, productMeta, map[string]any{"owner": "user-1", "embedding": "[0.1,0.2]", "name": "Imported"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created["id"] != "legacy-product-id" || created["owner"] != "user-1" {
		t.Fatalf("created row = %#v", created)
	}

	registry := models.NewRegistry()
	for _, meta := range []models.Metadata{legacyUserMetadata(), productMeta} {
		if err := registry.RegisterMetadata(meta); err != nil {
			t.Fatalf("register %s: %v", meta.Label(), err)
		}
	}
	state := migrations.StateFromRegistry(registry)
	product := state.Models["legacy.Product"]
	if len(product.Fields) < 2 || product.Fields[1].Name != "owner" || product.Fields[1].Kind != "uuid" {
		t.Fatalf("relation field state = %#v", product.Fields)
	}

	vectorIndex := vector.HNSWIndex("idx_legacy_product_embedding_hnsw", "embedding", vector.CosineOps)
	indexSQL := schema.NewEditor(postgresdialect.New()).AddIndex("legacy_product", migrations.IndexState{
		Name:      vectorIndex.NameFor("legacy_product"),
		Fields:    vectorIndex.FieldNames(),
		Method:    vectorIndex.Method,
		OpClasses: append([]string(nil), vectorIndex.OpClasses...),
	})
	if !strings.Contains(indexSQL, "USING hnsw") || !strings.Contains(indexSQL, "vector_cosine_ops") {
		t.Fatalf("vector index SQL = %q", indexSQL)
	}

	expected := migrations.TableSchema{
		Name: "legacy_product",
		Columns: []migrations.ColumnSchema{
			{Name: "id", NormalizedKind: "text", PrimaryKey: true},
			{Name: "owner_id", NormalizedKind: "uuid"},
			{Name: "embedding", NormalizedKind: "vector(384)", Nullable: true},
		},
		Indexes:     []migrations.IndexSchema{{Name: "idx_legacy_product_embedding_hnsw", Fields: []string{"embedding"}, Method: "hnsw", OpClasses: []string{"vector_cosine_ops"}}},
		Constraints: []migrations.ConstraintSchema{{Name: "uniq_legacy_product_owner", Type: "unique", Fields: []string{"owner_id"}}},
	}
	actual := migrations.TableSchema{
		Name: "legacy_product",
		Columns: []migrations.ColumnSchema{
			{Name: "id", NormalizedKind: "text", PrimaryKey: true},
			{Name: "owner_id", NormalizedKind: "uuid"},
			{Name: "embedding", NormalizedKind: "vector(384)", Nullable: true},
		},
	}
	diffs := migrations.CompareTableShape(expected, actual)
	output := schemaDiffOutput(diffs)
	for _, want := range []string{"MISSING index legacy_product.idx_legacy_product_embedding_hnsw", "MISSING constraint legacy_product.uniq_legacy_product_owner"} {
		if !strings.Contains(output, want) {
			t.Fatalf("schema diff missing %q:\n%s", want, output)
		}
	}
	actual.Indexes = expected.Indexes
	actual.Constraints = expected.Constraints
	if diffs := migrations.CompareTableShape(expected, actual); len(diffs) != 0 {
		t.Fatalf("matching schema diffs = %#v", diffs)
	}
}

func TestExistingProductSchemaAdoptionManagementCommands(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	staticRoot := filepath.Join(dir, "staticfiles")
	mediaRoot := filepath.Join(dir, "media")
	if err := os.MkdirAll(staticRoot, 0o755); err != nil {
		t.Fatalf("mkdir static: %v", err)
	}
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	writeFile(t, filepath.Join(staticRoot, "staticfiles.json"), "{}")
	writeFile(t, filepath.Join(dir, ".env"), "GOGO_ENV=production\nGOGO_SECRET_KEY=8aUQh2zR7mN4pL6vCx9YtB3sWk5dF1gH\nGOGO_DEBUG=false\nGOGO_ALLOWED_HOSTS=example.com\nDATABASE_URL=sqlite://"+filepath.ToSlash(dbPath)+"\nGOGO_STATIC_ROOT="+filepath.ToSlash(staticRoot)+"\nGOGO_MEDIA_ROOT="+filepath.ToSlash(mediaRoot)+"\nGOGO_SESSION_COOKIE_SECURE=true\nGOGO_CSRF_COOKIE_SECURE=true\nGOGO_HTTPS_ENABLED=true\nGOGO_ADMIN_PATH=/admin\nGOGO_ADMIN_PATH_REVIEWED=true\nGOGO_DEPLOY_MIGRATIONS_APPLIED=true\nGOGO_DEPLOY_STATIC_COLLECTED=true\nGOGO_PASSWORD_RESET_ENABLED=false\n")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy_order (id integer PRIMARY KEY, number text NOT NULL, created_at timestamp)`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	t.Chdir(dir)

	project := management.Project{
		ModelMetadata: func() []models.Metadata {
			return []models.Metadata{legacyOrderMetadata(false)}
		},
		Migrations: func() []migrations.Migration {
			return []migrations.Migration{legacyInitialMigration()}
		},
	}

	var inspectOut bytes.Buffer
	if err := management.ExecuteProject(context.Background(), []string{"inspectdb", "--table", "legacy_order"}, &inspectOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("inspectdb error = %v", err)
	}
	if !strings.Contains(inspectOut.String(), `ModelName: "LegacyOrder"`) {
		t.Fatalf("inspectdb output = %q", inspectOut.String())
	}

	var diffOut bytes.Buffer
	if err := management.ExecuteProject(context.Background(), []string{"diffschema", "--app", "legacy"}, &diffOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("diffschema error = %v\n%s", err, diffOut.String())
	}
	if !strings.Contains(diffOut.String(), "schema matches model metadata") {
		t.Fatalf("diffschema output = %q", diffOut.String())
	}

	var sqlOut bytes.Buffer
	if err := management.ExecuteProject(context.Background(), []string{"sqlmigrate", "legacy", "0001_initial"}, &sqlOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	if !strings.Contains(sqlOut.String(), `CREATE TABLE "legacy_order"`) {
		t.Fatalf("sqlmigrate output = %q", sqlOut.String())
	}

	var migrateOut bytes.Buffer
	if err := management.ExecuteProject(context.Background(), []string{"migrate", "--app", "legacy", "--fake-initial"}, &migrateOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("migrate fake-initial error = %v\n%s", err, migrateOut.String())
	}
	if !strings.Contains(migrateOut.String(), "applied legacy.0001_initial") {
		t.Fatalf("migrate output = %q", migrateOut.String())
	}

	for _, args := range [][]string{{"check", "--tag", "models"}, {"check", "--tag", "migrations"}, {"check", "--deploy", "--tag", "deploy"}} {
		var stdout bytes.Buffer
		if err := management.ExecuteProject(context.Background(), args, &stdout, &bytes.Buffer{}, project); err != nil {
			t.Fatalf("%v error = %v\n%s", args, err, stdout.String())
		}
	}
}

func TestExistingProductCustomTableMakemigrationsCompatibility(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeFile(t, filepath.Join(dir, ".env"), "GOGO_SECRET_KEY=compat-secret\nDATABASE_URL=sqlite://"+filepath.ToSlash(dbPath)+"\n")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE orders (id integer PRIMARY KEY, number text NOT NULL, created_at timestamp)`); err != nil {
		_ = db.Close()
		t.Fatalf("create orders table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	t.Chdir(dir)

	project := management.Project{
		ModelMetadata: func() []models.Metadata {
			return []models.Metadata{salesOrderMetadata(true)}
		},
		Migrations: func() []migrations.Migration {
			return []migrations.Migration{salesInitialMigration()}
		},
	}

	var diffOut bytes.Buffer
	err = management.ExecuteProject(context.Background(), []string{"diffschema", "--app", "sales"}, &diffOut, &bytes.Buffer{}, project)
	if err == nil {
		t.Fatal("diffschema succeeded with missing orders.status")
	}
	if !strings.Contains(diffOut.String(), "MISSING column orders.status") {
		t.Fatalf("diffschema output = %q", diffOut.String())
	}

	var makeOut bytes.Buffer
	if err := management.ExecuteProject(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "add_status"}, &makeOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("makemigrations error = %v\n%s", err, makeOut.String())
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_add_status.go"))
	if err != nil {
		t.Fatalf("read generated migration: %v", err)
	}
	for _, want := range []string{`\"type\":\"AddField\"`, `\"table_name\":\"orders\"`, `\"type\":\"AddIndex\"`, `\"type\":\"AddConstraint\"`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("generated migration missing %q:\n%s", want, contents)
		}
	}

	var sqlOut bytes.Buffer
	if err := management.ExecuteProject(context.Background(), []string{"sqlmigrate", "sales", "0002_add_status"}, &sqlOut, &bytes.Buffer{}, project); err != nil {
		t.Fatalf("sqlmigrate generated migration error = %v", err)
	}
	for _, want := range []string{`ALTER TABLE "orders" ADD COLUMN "status" text`, `CREATE INDEX "idx_orders_status" ON "orders" ("status")`, `-- SQLite rebuild required to add constraint "uniq_orders_status" on "orders"`} {
		if !strings.Contains(sqlOut.String(), want) {
			t.Fatalf("sqlmigrate output missing %q:\n%s", want, sqlOut.String())
		}
	}
}

func legacyOrderMetadata(managed bool) models.Metadata {
	return models.Metadata{
		AppLabel:  "legacy",
		ModelName: "Order",
		TableName: "legacy_order",
		DBTable:   "legacy_order",
		Managed:   &managed,
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "number", Column: "number"},
			{Name: "created_at", Column: "created_at", Null: true},
		},
	}
}

func legacyInitialMigration() migrations.Migration {
	return migrations.Migration{
		AppLabel: "legacy",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "legacy",
				Name:      "Order",
				TableName: "legacy_order",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "number", Column: "number", Kind: "text"},
					{Name: "created_at", Column: "created_at", Kind: "timestamp", Null: true},
				},
			}},
		},
	}
}

func salesOrderMetadata(includeStatus bool) models.Metadata {
	fields := []models.FieldMeta{
		{Name: "id", Column: "id", Kind: "integer", PrimaryKey: true},
		{Name: "number", Column: "number", Kind: "text"},
		{Name: "created_at", Column: "created_at", Kind: "timestamp", Null: true},
	}
	indexes := []models.Index(nil)
	constraints := []models.Constraint(nil)
	if includeStatus {
		fields = append(fields, models.FieldMeta{Name: "status", Column: "status", Kind: "text", Null: true})
		indexes = append(indexes, models.Index{Name: "idx_orders_status", Fields: []models.IndexField{models.Asc("status")}})
		constraints = append(constraints, models.Constraint{Name: "uniq_orders_status", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("status")}})
	}
	return models.Metadata{
		AppLabel:    "sales",
		ModelName:   "Order",
		TableName:   "orders",
		DBTable:     "orders",
		Fields:      fields,
		Indexes:     indexes,
		Constraints: constraints,
	}
}

func salesInitialMigration() migrations.Migration {
	return migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "integer", PrimaryKey: true},
					{Name: "number", Column: "number", Kind: "text"},
					{Name: "created_at", Column: "created_at", Kind: "timestamp", Null: true},
				},
			}},
		},
	}
}

func legacyUserMetadata() models.Metadata {
	return models.Metadata{
		AppLabel:  "accounts",
		ModelName: "User",
		TableName: "accounts_user",
		DBTable:   "accounts_user",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true, DBDefault: models.DefaultSQL("gen_random_uuid()")},
		},
	}
}

func legacyProductMetadata() models.Metadata {
	return models.Metadata{
		AppLabel:  "legacy",
		ModelName: "Product",
		TableName: "legacy_product",
		DBTable:   "legacy_product",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "text", PrimaryKey: true, DBDefault: models.DefaultValue("legacy-product-id")},
			{Name: "owner", Column: "owner_id", RelationTarget: "accounts.User"},
			vector.FieldMeta("embedding", "embedding", 384),
			{Name: "name", Column: "name", Kind: "text"},
		},
		Indexes: []models.Index{
			vector.HNSWIndex("idx_legacy_product_embedding_hnsw", "embedding", vector.CosineOps),
		},
		Constraints: []models.Constraint{
			{Name: "uniq_legacy_product_owner", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("owner_id")}},
		},
	}
}

func schemaDiffOutput(diffs []migrations.SchemaDifference) string {
	lines := make([]string, len(diffs))
	for i, diff := range diffs {
		lines[i] = diff.String()
	}
	return strings.Join(lines, "\n")
}

func redisTestURL() string {
	value := strings.TrimSpace(os.Getenv("GOGO_TEST_REDIS_ADDR"))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	return "redis://" + value + "/0"
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
