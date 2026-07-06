package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/conf"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
)

func TestCheckCommandPassesValidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeTextFile(t, filepath.Join(dir, ".env"), `
GOGO_SECRET_KEY=check-secret
DATABASE_URL=postgres://check
`)

	root := NewRoot()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := root.Execute(context.Background(), []string{"check"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute(check) error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"OK config settings valid",
		"INFO apps app registry checks registered",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("check output = %q, want it to contain %q", output, want)
		}
	}
}

func TestCheckCommandFailsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	root := NewRoot()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := root.Execute(context.Background(), []string{"check"}, &stdout, &stderr)
	if !errors.Is(err, conf.ErrInvalidSettings) {
		t.Fatalf("Execute(check) error = %v, want ErrInvalidSettings", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"ERROR config",
		"GOGO_SECRET_KEY",
		"DATABASE_URL",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("check output = %q, want it to contain %q", output, want)
		}
	}
}

func TestCheckCommandSupportsTagFiltering(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeTextFile(t, filepath.Join(dir, ".env"), `
GOGO_SECRET_KEY=check-secret
DATABASE_URL=postgres://check
`)
	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"check", "--tag", "queue"}, &stdout, io.Discard); err != nil {
		t.Fatalf("Execute(check --tag queue) error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "INFO queue queue checks registered") || strings.Contains(output, "WARN apps") {
		t.Fatalf("filtered check output = %q", output)
	}
}

func TestCheckCommandSupportsDeployChecks(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	staticRoot := filepath.Join(dir, "staticfiles")
	mediaRoot := filepath.Join(dir, "media")
	if err := os.MkdirAll(staticRoot, 0o755); err != nil {
		t.Fatalf("mkdir static root: %v", err)
	}
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		t.Fatalf("mkdir media root: %v", err)
	}
	writeTextFile(t, filepath.Join(staticRoot, "staticfiles.json"), `{}`)
	writeTextFile(t, filepath.Join(dir, ".env"), `
GOGO_ENV=production
GOGO_SECRET_KEY=8aUQh2zR7mN4pL6vCx9YtB3sWk5dF1gH
GOGO_DEBUG=false
GOGO_ALLOWED_HOSTS=example.com,admin.example.com
GOGO_HTTP_ADDR=:8000
DATABASE_URL=sqlite://:memory:
GOGO_STATIC_ROOT=`+staticRoot+`
GOGO_MEDIA_ROOT=`+mediaRoot+`
GOGO_SESSION_COOKIE_SECURE=true
GOGO_CSRF_COOKIE_SECURE=true
GOGO_CSRF_TRUSTED_ORIGINS=https://admin.example.com
GOGO_HTTPS_ENABLED=true
GOGO_ADMIN_PATH=/admin
GOGO_ADMIN_PATH_REVIEWED=true
GOGO_DEPLOY_MIGRATIONS_APPLIED=true
GOGO_DEPLOY_STATIC_COLLECTED=true
GOGO_BROKER_URL=
GOGO_RESULT_BACKEND=
GOGO_PASSWORD_RESET_ENABLED=true
GOGO_EMAIL_URL=smtp://mail:1025
`)

	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"check", "--deploy", "--tag", "deploy"}, &stdout, io.Discard); err != nil {
		t.Fatalf("Execute(check --deploy) error = %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "INFO deploy production deploy checks passed") {
		t.Fatalf("deploy check output = %q", stdout.String())
	}
}

func TestCheckCommandValidatesProjectModelsAndMigrations(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeTextFile(t, filepath.Join(dir, ".env"), `
GOGO_SECRET_KEY=check-secret
DATABASE_URL=sqlite://:memory:
`)

	root := NewRootWithOptions(RootOptions{
		ProjectModels: []models.Metadata{{
			AppLabel:  "blog",
			ModelName: "Item",
			TableName: "blog_item",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", PrimaryKey: true},
				{Name: "legacy_id", Column: "id"},
			},
		}},
		ProjectMigrations: []migrations.Migration{{
			AppLabel: "blog",
			Name:     "0001_initial",
			Operations: []migrations.Operation{
				migrations.ManifestOperation{NameValue: "NoopMigration"},
			},
		}},
	})
	var stdout bytes.Buffer
	err := root.Execute(context.Background(), []string{"check", "--tag", "models"}, &stdout, io.Discard)
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("check models error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(stdout.String(), "duplicate column id") {
		t.Fatalf("check models output = %q", stdout.String())
	}

	validModel := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Item",
		TableName: "blog_item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
		},
	}
	root = NewRootWithOptions(RootOptions{
		ProjectModels: []models.Metadata{validModel},
		ProjectMigrations: []migrations.Migration{
			{
				AppLabel:   "blog",
				Name:       "0001_initial",
				Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "NoopMigration"}},
			},
			{
				AppLabel:     "blog",
				Name:         "0002_missing",
				Dependencies: []migrations.Dependency{{AppLabel: "blog", Name: "0009_missing"}},
				Operations:   []migrations.Operation{migrations.ManifestOperation{NameValue: "NoopMigration"}},
			},
		},
	})
	stdout.Reset()
	err = root.Execute(context.Background(), []string{"check", "--tag", "migrations"}, &stdout, io.Discard)
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("check migrations error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(stdout.String(), "missing dependency") && !strings.Contains(stdout.String(), "0009_missing") {
		t.Fatalf("check migrations output = %q", stdout.String())
	}
}

func writeTextFile(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}
