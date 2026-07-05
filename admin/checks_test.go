package admin

import (
	"context"
	"testing"

	"github.com/cybersaksham/gogo/checks"
	"github.com/cybersaksham/gogo/models"
)

func TestAdminChecksReportInvalidModelAdminOptions(t *testing.T) {
	site := DefaultSite()
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "status", Column: "status"},
		},
	}
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{
		ListDisplay:  []string{"title", "missing"},
		ListEditable: []string{"status"},
		SearchFields: []string{"^missing_search"},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	results := CheckSite(site)
	ids := checkResultIDs(results)
	for _, want := range []string{"admin.E001", "admin.E002"} {
		if !hasCheckID(ids, want) {
			t.Fatalf("admin check IDs = %#v, missing %s; results=%#v", ids, want, results)
		}
	}
	if !checks.HasFailures(results, checks.SeverityError) {
		t.Fatalf("admin checks should fail at error level: %#v", results)
	}
}

func TestRegisterChecksAddsAdminChecksToRegistry(t *testing.T) {
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
		},
	}, ModelAdmin{ListDisplay: []string{"missing"}}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	registry := checks.NewRegistry()
	RegisterChecks(registry, site)
	results := registry.Run(context.Background(), checks.Options{Tags: []string{"admin"}})
	if ids := checkResultIDs(results); !hasCheckID(ids, "admin.E002") {
		t.Fatalf("registered admin checks = %#v", results)
	}
}

func TestAdminChecksAcceptValidModelAdminOptions(t *testing.T) {
	site := DefaultSite()
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "status", Column: "status"},
		},
	}
	if err := site.ModelRegistry.RegisterMetadata(meta, ModelAdmin{
		ListDisplay:        []string{"title", "status"},
		ListDisplayLinks:   []string{"title"},
		ListEditable:       []string{"status"},
		SearchFields:       []string{"^title"},
		PrepopulatedFields: map[string][]string{"status": {"title"}},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	if results := CheckSite(site); len(results) != 0 {
		t.Fatalf("valid admin checks = %#v", results)
	}
}

func checkResultIDs(results []checks.Result) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	return ids
}

func hasCheckID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
