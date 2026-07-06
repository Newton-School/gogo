package admin

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/files"
	"github.com/Newton-School/gogo/models"
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

func TestAdminChecksReportUnsupportedMetadataWidgets(t *testing.T) {
	site := DefaultSite()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "title", Column: "title"},
			{Name: "author", Column: "author_id", RelationTarget: "auth.User", RelationType: "foreign_key"},
			{Name: "status", Column: "status"},
		},
	}, ModelAdmin{
		AutocompleteFields: []string{"title"},
		FilterHorizontal:   []string{"author"},
		RadioFields:        map[string]string{"status": "horizontal"},
	}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	results := CheckSite(site)
	ids := checkResultIDs(results)
	if !hasCheckID(ids, "admin.E003") {
		t.Fatalf("admin check IDs = %#v, want admin.E003; results=%#v", ids, results)
	}
}

func TestAdminChecksReportMissingStoreCapabilities(t *testing.T) {
	site := DefaultSite()
	site.ModelStore = checkModelStore{}
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "attachment", Column: "attachment", Kind: "file", UploadTo: "uploads"},
			{Name: "tags", RelationTarget: "blog.Tag", RelationType: "many_to_many"},
		},
	}, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	results := CheckSite(site)
	ids := checkResultIDs(results)
	if !hasCheckID(ids, "admin.E004") || len(results) != 2 {
		t.Fatalf("admin store capability checks = %#v", results)
	}
}

func TestAdminChecksAcceptConfiguredStoreCapabilities(t *testing.T) {
	site := DefaultSite()
	site.ModelStore = &processorStore{}
	site.FileStorage = files.NewLocalStorage(t.TempDir(), files.LocalOptions{})
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "attachment", Column: "attachment", Kind: "file", UploadTo: "uploads"},
			{Name: "tags", RelationTarget: "blog.Tag", RelationType: "many_to_many"},
		},
	}, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}

	if results := CheckSite(site); len(results) != 0 {
		t.Fatalf("admin store capability checks = %#v", results)
	}
}

func checkResultIDs(results []checks.Result) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	return ids
}

type checkModelStore struct{}

func (checkModelStore) List(context.Context, models.Metadata) ([]map[string]any, error) {
	return nil, nil
}

func (checkModelStore) Get(context.Context, models.Metadata, string) (map[string]any, bool, error) {
	return nil, false, nil
}

func (checkModelStore) Create(context.Context, models.Metadata, map[string]any) (map[string]any, error) {
	return nil, nil
}

func (checkModelStore) Update(context.Context, models.Metadata, string, map[string]any, bool) (map[string]any, error) {
	return nil, nil
}

func (checkModelStore) Delete(context.Context, models.Metadata, string) error {
	return nil
}

func hasCheckID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
