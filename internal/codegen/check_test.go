package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckGeneratedIsReadOnlyAndDetectsDrift(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := StartProject(project, ProjectOptions{Module: "example.com/project"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err != nil {
		t.Fatal("empty generated project", err)
	}
	if err := StartApp(project, "catalog"); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err != nil {
		t.Fatal("fresh app", err)
	}
	target := filepath.Join(project, "apps/catalog/zz_gogo.gen.go")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	source := `package catalog
import "github.com/Newton-School/gogo/core/models"
type Entry struct { Title string }
func (*Entry) Schema() models.Schema {
 panic("schema code must never run during source checks")
 return models.Schema{AppLabel:"catalog",Name:"Entry",Fields:[]models.Field{
  models.CharField("title",models.WithStructField("Title")),
 }}
}
`
	model := filepath.Join(project, "apps/catalog/models.go")
	if err := os.WriteFile(model, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatal("stale descriptors passed", err)
	}
	after, err := os.ReadFile(target)
	if err != nil || string(after) != string(original) {
		t.Fatal("check changed descriptor bytes", err)
	}
	afterInfo, err := os.Stat(target)
	if err != nil || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("check rewrote descriptor", err)
	}
	if err := Generate(project); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err != nil {
		t.Fatal("explicit generation did not reconcile", err)
	}
	if err := os.Rename(target, target+".fixture"); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err == nil {
		t.Fatal("missing descriptor passed")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("check created missing descriptor", err)
	}
	if err := os.Symlink(target+".fixture", target); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err == nil {
		t.Fatal("linked descriptor passed")
	}
}

func TestCheckGeneratedDoesNotRepairEarlierAppsOnLaterFailure(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := StartProject(project, ProjectOptions{Module: "example.com/project"}); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"alpha", "zeta"} {
		if err := StartApp(project, label); err != nil {
			t.Fatal(err)
		}
	}
	first := filepath.Join(project, "apps/alpha/zz_gogo.gen.go")
	info, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	last := filepath.Join(project, "apps/zeta/models.go")
	if err := os.WriteFile(last, []byte("not Go source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CheckGenerated(project); err == nil {
		t.Fatal("invalid source passed")
	}
	after, err := os.Stat(first)
	if err != nil || !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("earlier app rewritten during read-only check", err)
	}
}
