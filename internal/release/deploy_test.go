package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/conf"
)

func TestRunDeployChecksPassesValidConfig(t *testing.T) {
	settings := validDeploySettings(t)
	results := RunDeployChecks(DeployConfig{
		Settings:               settings,
		DatabaseReachable:      true,
		StaticFilesCollected:   true,
		MediaStorageWritable:   true,
		QueueBrokerReachable:   true,
		ResultBackendReachable: true,
		ScheduleStoreReachable: true,
	})
	if len(results) != 1 || results[0].ID != "deploy.I001" || results[0].Severity != checks.SeverityInfo {
		t.Fatalf("results = %#v", results)
	}
}

func TestRunDeployChecksCatchesEveryFailure(t *testing.T) {
	settings := conf.Settings{
		Env:                  "production",
		Debug:                true,
		SecretKey:            "short",
		AllowedHosts:         []string{"*"},
		DatabaseURL:          "sqlite://:memory:",
		HTTPAddr:             ":8000",
		CSRFTrustedOrigins:   []string{"http://bad.example.com/path"},
		AdminPath:            "admin",
		BrokerURL:            "redis://localhost:1/0",
		ResultBackend:        "redis://localhost:1/1",
		ScheduleStore:        "redis://localhost:1/2",
		PasswordResetEnabled: true,
	}
	results := RunDeployChecks(DeployConfig{
		Settings:             settings,
		DatabaseError:        errors.New("database down"),
		MediaStorageError:    errors.New("media down"),
		QueueBrokerError:     errors.New("broker down"),
		ResultBackendError:   errors.New("result backend down"),
		ScheduleStoreError:   errors.New("schedule store down"),
		StaticFilesCollected: false,
	})
	got := resultIDs(results)
	want := []string{
		"deploy.E001",
		"deploy.E002",
		"deploy.E003",
		"deploy.E004",
		"deploy.E005",
		"deploy.E006",
		"deploy.E007",
		"deploy.E008",
		"deploy.E009",
		"deploy.E010",
		"deploy.E011",
		"deploy.E012",
		"deploy.E013",
		"deploy.E014",
		"deploy.E015",
		"deploy.E019",
		"deploy.E016",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result IDs = %#v, want %#v", got, want)
	}
}

func TestRunDeployChecksRejectsProductionMemoryQueueURLs(t *testing.T) {
	settings := validDeploySettings(t)
	settings.BrokerURL = "memory://"
	settings.ResultBackend = "memory"
	settings.ScheduleStore = "memory://"
	results := RunDeployChecks(DeployConfig{
		Settings:               settings,
		DatabaseReachable:      true,
		StaticFilesCollected:   true,
		MediaStorageWritable:   true,
		QueueBrokerReachable:   true,
		ResultBackendReachable: true,
		ScheduleStoreReachable: true,
	})
	got := resultIDs(results)
	want := []string{"deploy.E017", "deploy.E018", "deploy.E020"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result IDs = %#v, want %#v", got, want)
	}
}

func TestBuildDeployConfigChecksSQLiteStaticMediaAndMemoryBackends(t *testing.T) {
	root := t.TempDir()
	staticRoot := filepath.Join(root, "staticfiles")
	mediaRoot := filepath.Join(root, "media")
	if err := os.MkdirAll(staticRoot, 0o755); err != nil {
		t.Fatalf("mkdir static: %v", err)
	}
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticRoot, "staticfiles.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write static manifest: %v", err)
	}

	settings := validDeploySettings(t)
	settings.DatabaseURL = "sqlite://" + filepath.Join(root, "db.sqlite3")
	settings.StaticRoot = staticRoot
	settings.MediaRoot = mediaRoot
	settings.BrokerURL = "memory://"
	settings.ResultBackend = "memory"
	settings.ScheduleStore = "memory://"

	config := BuildDeployConfig(context.Background(), settings)
	if !config.DatabaseReachable || config.DatabaseError != nil {
		t.Fatalf("database reachable = %t error = %v", config.DatabaseReachable, config.DatabaseError)
	}
	if !config.StaticFilesCollected {
		t.Fatal("StaticFilesCollected = false")
	}
	if !config.MediaStorageWritable || config.MediaStorageError != nil {
		t.Fatalf("media writable = %t error = %v", config.MediaStorageWritable, config.MediaStorageError)
	}
	if !config.QueueBrokerReachable || config.QueueBrokerError != nil {
		t.Fatalf("broker reachable = %t error = %v", config.QueueBrokerReachable, config.QueueBrokerError)
	}
	if !config.ResultBackendReachable || config.ResultBackendError != nil {
		t.Fatalf("result backend reachable = %t error = %v", config.ResultBackendReachable, config.ResultBackendError)
	}
	if !config.ScheduleStoreReachable || config.ScheduleStoreError != nil {
		t.Fatalf("schedule store reachable = %t error = %v", config.ScheduleStoreReachable, config.ScheduleStoreError)
	}
}

func validDeploySettings(t *testing.T) conf.Settings {
	t.Helper()
	return conf.Settings{
		Env:                  "production",
		SecretKey:            "8aUQh2zR7mN4pL6vCx9YtB3sWk5dF1gH",
		Debug:                false,
		AllowedHosts:         []string{"example.com", "admin.example.com"},
		HTTPAddr:             ":8000",
		DatabaseURL:          "sqlite://:memory:",
		StaticRoot:           t.TempDir(),
		MediaRoot:            t.TempDir(),
		SessionCookieName:    "gogo_sessionid",
		SessionCookieSecure:  true,
		CSRFCookieName:       "gogo_csrftoken",
		CSRFCookieSecure:     true,
		HTTPSEnabled:         true,
		CSRFTrustedOrigins:   []string{"https://admin.example.com"},
		AdminPath:            "/admin",
		AdminPathReviewed:    true,
		MigrationsApplied:    true,
		StaticFilesCollected: true,
		PasswordResetEnabled: true,
		EmailURL:             "smtp://mail:1025",
		BrokerURL:            "",
		ResultBackend:        "",
		DefaultAutoField:     "BigAutoField",
		TimeZone:             "UTC",
		LanguageCode:         "en-us",
	}
}

func resultIDs(results []checks.Result) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	return ids
}
