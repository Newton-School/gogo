package docs_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/internal/codegen"
)

// The tutorial test uses local replacements only in its disposable client. It
// proves the documented scaffold/files compile, not a fresh network install or
// an applied PostgreSQL migration. Published-module installation has a separate
// repository gate and the showcase remains pinned without local replacements.
func TestFirstProjectTutorial(t *testing.T) {
	testFirstProjectTutorial(t, false)
}

// Opt in when network/module-proxy access is available. No local replacements
// are added, so a passing source-checkout test cannot hide release incompatibility.
func TestFirstProjectTutorialPublished(t *testing.T) {
	if os.Getenv("GOGO_TEST_DOCS_PUBLISHED") != "1" {
		t.Skip("set GOGO_TEST_DOCS_PUBLISHED=1 to verify the pinned public modules")
	}
	testFirstProjectTutorial(t, true)
}

func testFirstProjectTutorial(t *testing.T, published bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("temporary client compilation")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "connectors", "postgres", "go.mod")); os.IsNotExist(err) {
		t.Skip("tutorial integration requires a complete multi-module repository checkout, not a packaged Core module")
	} else if err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(t.TempDir(), "storefront")
	if err := codegen.StartProject(client, codegen.ProjectOptions{Module: "example.com/storefront"}); err != nil {
		t.Fatal(err)
	}
	if err := codegen.StartApp(client, "catalog"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"models.go", "urls.go"} {
		source, err := os.ReadFile(filepath.Join(root, "docs", "snippets", "catalog", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(client, "apps", "catalog", name), source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{}
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "GOGO_") || strings.HasPrefix(value, "GOWORK=") || strings.HasPrefix(value, "DOCS_DATABASE_URL=") {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "GOWORK=off")
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("go", args...)
		command.Dir, command.Env = client, environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, output)
		}
	}
	if !published {
		run("mod", "edit", "-replace=github.com/Newton-School/gogo="+root,
			"-replace=github.com/Newton-School/gogo/connectors/postgres="+filepath.Join(root, "connectors", "postgres"))
	}
	run("mod", "tidy")
	run("run", "manage.go", "generate")
	run("run", "manage.go", "makemigrations", "catalog")
	run("run", "manage.go", "generate", "--check")
	run("run", "manage.go", "makemigrations", "catalog", "--check")
	run("test", "./...")
	// Continue the same client through the database-backed API chapter. These
	// exact files are displayed in the guide, not parallel example implementations.
	snippets := filepath.Join(root, "docs", "snippets", "storefront")
	if err := filepath.WalkDir(snippets, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go.txt") {
			return nil
		}
		relative, err := filepath.Rel(snippets, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(client, strings.TrimSuffix(relative, ".txt")), data, 0600)
	}); err != nil {
		t.Fatal(err)
	}
	run("run", "manage.go", "generate", "--check")
	run("run", "manage.go", "makemigrations", "catalog", "--check")
	run("run", "manage.go", "seed", "--help")
	run("test", "./...")
	run("build", "-o", filepath.Join(t.TempDir(), "manage"), ".")
}
