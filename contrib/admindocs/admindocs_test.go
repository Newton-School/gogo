package admindocs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

func TestAdminDocsGenerationAndPermission(t *testing.T) {
	registry := admin.NewRegistry()
	_ = registry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}, admin.ModelAdmin{ListDisplay: []string{"title"}})
	docs := Generate(DocConfig{
		Registry:        registry,
		Routes:          []RouteDoc{{Name: "admin:index", Path: "/admin/"}},
		TemplateTags:    []string{"url"},
		TemplateFilters: []string{"intcomma"},
		Settings:        []string{"INSTALLED_APPS"},
		Commands:        []string{"check"},
	})
	if !contains(docs.Models, "blog.Post") || !contains(docs.Routes, "/admin/") || !contains(docs.Filters, "intcomma") || !contains(docs.Commands, "check") {
		t.Fatalf("docs = %#v", docs)
	}
	handler := Handler(DocConfig{Registry: registry}, func(*http.Request) auth.User {
		return auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{IsActive: true}, IsStaff: true}}
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/doc/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "blog.Post") {
		t.Fatalf("staff response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	denied := Handler(DocConfig{}, func(*http.Request) auth.User { return auth.User{} })
	recorder = httptest.NewRecorder()
	denied.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/doc/", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d", recorder.Code)
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
