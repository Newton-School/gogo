package docs_test

import (
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
	if testing.Short() {
		t.Skip("temporary client compilation")
	}
	root, err := filepath.Abs("..")
	if err != nil {
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
		if strings.HasPrefix(value, "GOGO_") || strings.HasPrefix(value, "GOWORK=") {
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
	run("mod", "edit", "-replace=github.com/Newton-School/gogo="+root,
		"-replace=github.com/Newton-School/gogo/connectors/postgres="+filepath.Join(root, "connectors", "postgres"))
	run("mod", "tidy")
	run("run", "manage.go", "generate")
	run("run", "manage.go", "makemigrations", "catalog")
	run("run", "manage.go", "generate", "--check")
	run("run", "manage.go", "makemigrations", "catalog", "--check")
	run("test", "./...")
}
