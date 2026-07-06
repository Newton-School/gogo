package compatibility

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/conf"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/sessions"
)

func TestLegacyMigrationManifestLoads(t *testing.T) {
	loaded, err := migrations.NewLoader([]string{fixturePath("migrations")}).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded migrations = %d, want 1", len(loaded))
	}
	migration := loaded[0]
	if migration.AppLabel != "blog" || migration.Name != "0001_initial" {
		t.Fatalf("migration identity = %s.%s", migration.AppLabel, migration.Name)
	}
	if len(migration.Dependencies) != 1 || migration.Dependencies[0].AppLabel != "auth" || migration.Dependencies[0].Name != "0001_initial" {
		t.Fatalf("dependencies = %#v", migration.Dependencies)
	}
	if got := operationNames(migration.Operations); strings.Join(got, ",") != "CreateModel,AddField,AddIndex" {
		t.Fatalf("operations = %#v", got)
	}
}

func TestLegacySettingsFileLoads(t *testing.T) {
	values, err := conf.LoadEnvFile(fixturePath("settings", "legacy.env"))
	if err != nil {
		t.Fatalf("LoadEnvFile() error = %v", err)
	}
	settings := conf.SettingsFromMap(values)
	if err := settings.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if settings.Env != "production" || settings.Debug {
		t.Fatalf("settings environment = %#v", settings)
	}
	if len(settings.AllowedHosts) != 2 || settings.AllowedHosts[0] != "legacy.example.com" {
		t.Fatalf("AllowedHosts = %#v", settings.AllowedHosts)
	}
	if settings.SessionCookieName != "legacy_sessionid" || settings.CSRFCookieName != "legacy_csrftoken" {
		t.Fatalf("cookie names = %q / %q", settings.SessionCookieName, settings.CSRFCookieName)
	}
}

func TestLegacyGeneratedProjectFixtureMatchesContract(t *testing.T) {
	moduleFile := readFixture(t, "generated_project_v1", "go.mod")
	for _, want := range []string{
		"module example.com/legacy",
		"go 1.26.4",
		"require github.com/Newton-School/gogo v0.1.0",
	} {
		if !strings.Contains(moduleFile, want) {
			t.Fatalf("go.mod missing %q:\n%s", want, moduleFile)
		}
	}

	values, err := conf.LoadEnvFile(fixturePath("generated_project_v1", ".env.example"))
	if err != nil {
		t.Fatalf("LoadEnvFile(.env.example) error = %v", err)
	}
	for _, key := range []string{"GOGO_ENV", "GOGO_SECRET_KEY", "DATABASE_URL", "GOGO_ALLOWED_HOSTS", "GOGO_SESSION_COOKIE_NAME", "GOGO_CSRF_COOKIE_NAME"} {
		if _, ok := values[key]; !ok {
			t.Fatalf(".env.example missing %s", key)
		}
	}
	if values["GOGO_SECRET_KEY"] != "" || values["DATABASE_URL"] != "" {
		t.Fatalf(".env.example must keep required secrets empty: %#v", values)
	}
}

func TestLegacyQueueEnvelopeDecodes(t *testing.T) {
	body := []byte(readFixture(t, "queue", "envelope_v1.json"))
	registry := queue.NewSerializationRegistry(queue.SerializationOptions{})
	var envelope queue.Envelope
	err := registry.Decode(queue.Payload{Serializer: "json", ContentType: "application/json", Compression: queue.CompressionNone, Body: body}, &envelope)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if envelope.ID != "task-legacy-1" || envelope.Name != "blog.publish" || envelope.Queue != "default" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if envelope.Headers["tenant"] != "legacy" || envelope.Retries != 2 || envelope.Priority != 5 {
		t.Fatalf("envelope metadata = %#v", envelope)
	}
}

func TestLegacySignedCookieSessionLoads(t *testing.T) {
	key := strings.TrimSpace(readFixture(t, "sessions", "signed_cookie_v1.txt"))
	store := sessions.NewSignedCookieStore("legacy-secret")
	session, ok, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !ok || session == nil {
		t.Fatalf("Load() ok = %t session = %#v", ok, session)
	}
	if session.GetString("user_id") != "42" {
		t.Fatalf("session data = %#v", session.Data)
	}
}

func TestLegacyPasswordHashVerifiesAndRequestsUpgrade(t *testing.T) {
	hash := strings.TrimSpace(readFixture(t, "auth", "password_hash_pbkdf2_v1.txt"))
	ok, err := auth.CheckPassword("legacy-password", hash)
	if err != nil {
		t.Fatalf("CheckPassword() error = %v", err)
	}
	if !ok {
		t.Fatal("CheckPassword() ok = false")
	}
	if !auth.MustUpdatePasswordHash(hash) {
		t.Fatal("MustUpdatePasswordHash() = false, want true for old iterations")
	}
}

func TestLegacyAdminURLsReverse(t *testing.T) {
	var fixtures []struct {
		Name string            `json:"name"`
		Args map[string]string `json:"args"`
		Want string            `json:"want"`
	}
	if err := json.Unmarshal([]byte(readFixture(t, "admin", "urls_v1.json")), &fixtures); err != nil {
		t.Fatalf("decode admin URL fixtures: %v", err)
	}

	site := admin.DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}, admin.ModelAdmin{
		CustomURLs: []admin.URLPattern{{Name: "stats", Path: "stats/"}},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}
	for _, fixture := range fixtures {
		args := map[string]any{}
		for key, value := range fixture.Args {
			args[key] = value
		}
		if len(args) == 0 {
			args = nil
		}
		got, err := router.Reverse(fixture.Name, args)
		if err != nil {
			t.Fatalf("Reverse(%s) error = %v", fixture.Name, err)
		}
		if got != fixture.Want {
			t.Fatalf("Reverse(%s) = %q, want %q", fixture.Name, got, fixture.Want)
		}
	}
}

func operationNames(operations []migrations.Operation) []string {
	names := make([]string, len(operations))
	for i, operation := range operations {
		names[i] = operation.Name()
	}
	return names
}

func readFixture(t *testing.T, parts ...string) string {
	t.Helper()
	body, err := os.ReadFile(fixturePath(parts...))
	if err != nil {
		t.Fatalf("read fixture %v: %v", parts, err)
	}
	return string(body)
}

func fixturePath(parts ...string) string {
	elements := append([]string{"testdata"}, parts...)
	return filepath.Join(elements...)
}
