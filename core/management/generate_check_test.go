package management

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/internal/codegen"
)

func TestGenerateCheckRunsWithoutRuntimeEffects(t *testing.T) {
	isolateCommandEnvironment(t)
	dir := filepath.Join(t.TempDir(), "project")
	if err := codegen.StartProject(dir, codegen.ProjectOptions{Module: "example.com/project"}); err != nil {
		t.Fatal(err)
	}
	if err := codegen.StartApp(dir, "catalog"); err != nil {
		t.Fatal(err)
	}
	project := Project{Root: dir, RuntimeResources: []string{"database"},
		ResourceFactory: func(conf.Values, []string) ([]app.Resource, error) {
			t.Fatal("generate check opened resources")
			return nil, nil
		},
		Apps: []app.Config{{Name: "catalog", Label: "catalog", Ready: func(context.Context, *app.Registry) error {
			t.Fatal("generate check ran Ready")
			return nil
		}}},
	}
	var output bytes.Buffer
	options := Options{Stdout: &output, Stderr: &output}
	if err := Call(context.Background(), project, []string{"generate", "--check"}, options); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "apps/catalog/zz_gogo.gen.go")
	if err := os.WriteFile(target, []byte("// owner edit must survive check\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Call(context.Background(), project, []string{"generate", "--check"}, options); err == nil {
		t.Fatal("invalid descriptors accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "// owner edit must survive check\n" {
		t.Fatal("read-only command overwrote owner data", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Call(ctx, project, []string{"generate", "--check"}, options); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}
