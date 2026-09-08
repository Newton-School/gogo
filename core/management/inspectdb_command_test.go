package management

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/db"
)

func inspectionCommandProject(t *testing.T, provider *inspectionProvider) (Project, *[]string) {
	t.Helper()
	var events []string
	command := InspectDBCommand(func(alias string) (db.Backend, db.CatalogIntrospector, error) {
		events = append(events, "resolve:"+alias)
		return inspectionBackend{}, provider, nil
	})
	project := Project{Root: t.TempDir(), Commands: []Command{command}, Environment: map[string]string{"GOGO_DATABASE_URL": "unused-fixture-setting"}, ResourceFactory: func(_ conf.Values, names []string) ([]app.Resource, error) {
		if !reflect.DeepEqual(names, []string{"database"}) {
			t.Fatalf("unrelated resources requested: %v", names)
		}
		return []app.Resource{{Name: "database", Open: func(context.Context) (func(context.Context) error, error) {
			events = append(events, "open")
			return func(context.Context) error { events = append(events, "close"); return nil }, nil
		}}}, nil
	}}
	return project, &events
}

func TestInspectDBCommandValidatesBeforeOpeningResources(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"--app", "bad/name"}, {"--database", "postgres://wrong"}, {"--package", "type"}, {"--primary-key", "account.id", "--primary-key", "account.id"}, {"account", "account"}, {"--schema", strings.Repeat("x", 64)}} {
		provider := &inspectionProvider{catalog: inspectionFixture()}
		project, events := inspectionCommandProject(t, provider)
		var output bytes.Buffer
		code := Run(context.Background(), project, append([]string{"manage", "inspectdb"}, args...), Options{Stdout: &output, Stderr: &output})
		if code != 2 || len(*events) != 0 || provider.calls != 0 {
			t.Fatalf("invalid invocation opened resources: %v %d %v", args, code, *events)
		}
	}
	provider := &inspectionProvider{catalog: inspectionFixture()}
	project, events := inspectionCommandProject(t, provider)
	var output bytes.Buffer
	if err := Call(context.Background(), project, []string{"inspectdb", "--help"}, Options{Stdout: &output, Stderr: &output}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 || provider.calls != 0 || !strings.Contains(output.String(), "include-views") {
		t.Fatal("help opened resources", *events, output.String())
	}
}

func TestInspectDBCommandUsesLazyAliasAndCompleteWriter(t *testing.T) {
	provider := &inspectionProvider{catalog: inspectionFixture()}
	project, events := inspectionCommandProject(t, provider)
	var output bytes.Buffer
	if err := Call(context.Background(), project, []string{"inspectdb", "account", "--database", "reporting", "--app", "legacy"}, Options{Stdout: &output}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"open", "resolve:reporting", "close"}) || !strings.Contains(output.String(), "type Account struct") {
		t.Fatal(*events, output.String())
	}
	provider = &inspectionProvider{catalog: inspectionFixture()}
	project, events = inspectionCommandProject(t, provider)
	if err := Call(context.Background(), project, []string{"inspectdb"}, Options{Stdout: inspectionShortWriter{}}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short writer reported success", err)
	}
	if (*events)[len(*events)-1] != "close" {
		t.Fatal("failed output did not close resources")
	}
}

type inspectionShortWriter struct{}

func (inspectionShortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestInspectDBCommandKeepsProviderErrorsPrivateAndMappingErrorsActionable(t *testing.T) {
	for _, mapping := range []bool{false, true} {
		provider := &inspectionProvider{catalog: inspectionFixture()}
		if mapping {
			provider.catalog.Relations[0].Columns[1].MappingIssue = "native type requires an explicit codec"
		} else {
			provider.err = errors.New("private-provider-value")
		}
		project, events := inspectionCommandProject(t, provider)
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), project, []string{"manage", "inspectdb"}, Options{Stdout: &stdout, Stderr: &stderr})
		if code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "private-provider-value") || (*events)[len(*events)-1] != "close" {
			t.Fatal(code, stdout.String(), stderr.String(), *events)
		}
		if mapping && (!strings.Contains(stderr.String(), `relation "account"`) || !strings.Contains(stderr.String(), `column "display_name"`)) {
			t.Fatal("mapping error not actionable", stderr.String())
		}
	}
}

func TestInspectDBCommandKeywordAppRequiresPackageFirst(t *testing.T) {
	for _, pkgFirst := range []bool{false, true} {
		provider := &inspectionProvider{catalog: inspectionFixture()}
		project, events := inspectionCommandProject(t, provider)
		var stdout, stderr bytes.Buffer
		args := []string{"manage", "inspectdb", "--app", "type", "--package", "legacy"}
		if pkgFirst {
			args = []string{"manage", "inspectdb", "--package", "legacy", "--app", "type"}
		}
		code := Run(context.Background(), project, args, Options{Stdout: &stdout, Stderr: &stderr})
		if pkgFirst {
			if code != 0 || len(*events) != 3 || !strings.Contains(stdout.String(), "package legacy") {
				t.Fatal(code, *events, stderr.String())
			}
		} else if code != 2 || len(*events) != 0 || stdout.Len() != 0 {
			t.Fatal("invalid interim package opened resources", code, *events)
		}
	}
}
