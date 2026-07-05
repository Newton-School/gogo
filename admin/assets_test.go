package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminEmbeddedAssetsExist(t *testing.T) {
	want := []string{
		"templates/404.html",
		"templates/500.html",
		"templates/actions.html",
		"templates/app_index.html",
		"templates/app_list.html",
		"templates/auth/user/add_form.html",
		"templates/auth/user/change_password.html",
		"templates/base.html",
		"templates/base_site.html",
		"templates/change_form_object_tools.html",
		"templates/index.html",
		"templates/login.html",
		"templates/logout.html",
		"templates/change_list_object_tools.html",
		"templates/change_list_results.html",
		"templates/change_list.html",
		"templates/change_form.html",
		"templates/color_theme_toggle.html",
		"templates/date_hierarchy.html",
		"templates/delete_confirmation.html",
		"templates/delete_selected_confirmation.html",
		"templates/edit_inline/stacked.html",
		"templates/edit_inline/tabular.html",
		"templates/filter.html",
		"templates/history.html",
		"templates/includes/fieldset.html",
		"templates/includes/object_delete_summary.html",
		"templates/invalid_setup.html",
		"templates/nav_sidebar.html",
		"templates/object_history.html",
		"templates/pagination.html",
		"templates/password_change.html",
		"templates/popup_response.html",
		"templates/prepopulated_fields_js.html",
		"templates/search_form.html",
		"templates/submit_line.html",
		"templates/widgets/clearable_file_input.html",
		"templates/widgets/date.html",
		"templates/widgets/foreign_key_raw_id.html",
		"templates/widgets/many_to_many_raw_id.html",
		"templates/widgets/radio.html",
		"templates/widgets/related_widget_wrapper.html",
		"templates/widgets/split_datetime.html",
		"templates/widgets/time.html",
		"templates/widgets/url.html",
		"static/admin.css",
		"static/admin.js",
		"static/admin/css/base.css",
		"static/admin/css/changelists.css",
		"static/admin/css/forms.css",
		"static/admin/css/login.css",
		"static/admin/css/widgets.css",
		"static/admin/img/icon-addlink.svg",
		"static/admin/img/icon-changelink.svg",
		"static/admin/img/icon-deletelink.svg",
		"static/admin/img/search.svg",
	}
	assets := AssetNames()
	for _, name := range want {
		if !containsString(assets, name) {
			t.Fatalf("asset %s missing from %#v", name, assets)
		}
		if body, ok := ReadAsset(name); !ok || len(body) == 0 {
			t.Fatalf("ReadAsset(%s) = %d, %v", name, len(body), ok)
		}
	}
}

func TestAdminPopupResponseTemplateIsStandalone(t *testing.T) {
	rendered, err := RenderTemplate("popup_response.html", map[string]any{"PopupResponseData": `{"value":"42"}`}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(popup_response) error = %v", err)
	}
	for _, want := range []string{
		`<!DOCTYPE html>`,
		`id="django-admin-popup-response-constants"`,
		`src="/admin/static/admin/js/popup_response.js"`,
		`data-popup-response="{&#34;value&#34;:&#34;42&#34;}"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("popup response missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `id="container"`) {
		t.Fatalf("popup response should not render inside admin base:\n%s", rendered)
	}
}

func TestAdminTemplatesRenderBlocksAndAllowOverrides(t *testing.T) {
	rendered, err := RenderTemplate("index.html", adminPageData{
		Header:         "Gogo administration",
		SiteURL:        "/admin/",
		ShowNavSidebar: true,
		ContentClass:   "colM",
		Apps: []IndexApp{{AppLabel: "blog", Models: []IndexModel{{
			AppLabel:  "blog",
			Name:      "Post",
			AddURL:    "/admin/blog/post/add/",
			ChangeURL: "/admin/blog/post/",
		}}}},
	}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(index) error = %v", err)
	}
	for _, want := range []string{
		`href="#content-start" class="skip-to-content-link"`,
		`<button class="theme-toggle">`,
		`<svg xmlns="http://www.w3.org/2000/svg" class="base-svgs">`,
		`id="toggle-nav-sidebar"`,
		`<nav class="sticky" id="nav-sidebar" aria-label="Sidebar">`,
		`<div class="main" id="main">`,
		`<main id="content-start" class="content" tabindex="-1">`,
		`<div id="content" class="colM">`,
		"Gogo administration",
		"blog",
		`id="recent-actions-module"`,
		`<thead class="visually-hidden">`,
		`aria-describedby="blog-post"`,
		`href="/admin/static/admin/css/base.css"`,
		`href="/admin/static/admin/css/dashboard.css"`,
		`href="/admin/static/admin/css/responsive.css"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered index missing %q:\n%s", want, rendered)
		}
	}
}

func TestAdminChangeListTemplateMatchesDjangoStructure(t *testing.T) {
	rendered, err := RenderTemplate("change_list.html", adminPageData{
		CSRFToken:              "token",
		AddURL:                 "/admin/blog/post/add/",
		ModelVerboseName:       "post",
		ModelVerboseNamePlural: "posts",
		ChangeList:             ChangeList{Total: 2, Columns: []ChangeListColumn{{Name: "title"}}},
	}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(change_list) error = %v", err)
	}
	for _, want := range []string{
		`<div class="module" id="changelist">`,
		`<div class="changelist-form-container">`,
		`<h2 id="changelist-search-form" class="visually-hidden">Search posts</h2>`,
		`<form id="changelist-search" method="get" role="search" aria-labelledby="changelist-search-form">`,
		`<label for="searchbar"><img src="/admin/static/admin/img/search.svg" alt="Search"></label>`,
		`<div class="changelist-footer">`,
		`<nav class="paginator" aria-labelledby="pagination">`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("change_list missing %q:\n%s", want, rendered)
		}
	}
}

func TestAdminChangeFormTemplateMatchesDjangoFieldsetStructure(t *testing.T) {
	rendered, err := RenderTemplate("change_form.html", adminPageData{
		CSRFToken: "token",
		Form: adminFormData{
			ID: "post_form",
			Fieldsets: []adminFieldsetData{{
				Name: "Main",
				Fields: []adminFormFieldData{{
					Name:       "title",
					Label:      "Title",
					FieldID:    "id_title",
					FieldCSS:   "form-row field-title",
					WidgetHTML: `<input type="text" name="title" id="id_title">`,
				}},
			}},
			SaveButtons: []adminSubmitButton{{Name: "_save", Value: "Save", Class: "default"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(change_form) error = %v", err)
	}
	for _, want := range []string{
		`id="fieldset-0-0-heading" class="fieldset-heading"`,
		`class="flex-container"`,
		`<div class="submit-row">`,
		`<script id="django-admin-form-add-constants"`,
		`src="/admin/static/admin/js/change_form.js"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("change_form missing %q:\n%s", want, rendered)
		}
	}
}

func TestAdminStaticAssetsServeDjangoCSSAndImages(t *testing.T) {
	site := DefaultSite()
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	tests := []struct {
		path        string
		contentType string
		want        string
	}{
		{"/admin/static/admin/css/base.css", "text/css; charset=utf-8", "--primary"},
		{"/admin/static/admin/css/changelists.css", "text/css; charset=utf-8", "#changelist"},
		{"/admin/static/admin/img/icon-addlink.svg", "image/svg+xml", "<svg"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", test.path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != test.contentType {
			t.Fatalf("%s content type = %q", test.path, got)
		}
		if !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("%s body missing %q", test.path, test.want)
		}
	}
}

func TestAdminStaticAssetRejectsTraversal(t *testing.T) {
	site := DefaultSite()
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/static/../templates/base.html", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("traversal status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminTemplatesAllowOverrides(t *testing.T) {
	rendered, err := RenderTemplate("index.html", map[string]any{"Header": "Gogo administration", "Apps": []IndexApp{{AppLabel: "blog"}}}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(index) error = %v", err)
	}
	if !strings.Contains(rendered, "<main") || !strings.Contains(rendered, "Gogo administration") || !strings.Contains(rendered, "blog") {
		t.Fatalf("rendered index = %s", rendered)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "admin", "templates"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin", "templates", "index.html"), []byte("override {{.Header}}"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	override, err := RenderTemplate("index.html", map[string]any{"Header": "Custom"}, []string{dir})
	if err != nil {
		t.Fatalf("RenderTemplate(override) error = %v", err)
	}
	if override != "override Custom" {
		t.Fatalf("override = %q", override)
	}
}

func TestAdminTemplatesAllowBaseSiteOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "admin"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	override := `{{define "base-site"}}<!doctype html><main id="custom-base-site">{{template "content" .}}</main>{{end}}`
	if err := os.WriteFile(filepath.Join(dir, "admin", "base_site.html"), []byte(override), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	rendered, err := RenderTemplate("index.html", adminPageData{
		Header:         "Gogo administration",
		SiteURL:        "/admin/",
		ShowNavSidebar: true,
		ContentClass:   "colM",
		Apps:           []IndexApp{{AppLabel: "blog"}},
	}, []string{dir})
	if err != nil {
		t.Fatalf("RenderTemplate(index with base_site override) error = %v", err)
	}
	for _, want := range []string{
		`id="custom-base-site"`,
		`id="content-main" class="app-list"`,
		`id="recent-actions-module"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("base_site override rendered output missing %q:\n%s", want, rendered)
		}
	}
}

func TestAdminTemplatesIgnoreNonAdminPartialOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "base.html"), []byte(`{{define "project-base"}}project layout{{end}}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	rendered, err := RenderTemplate("login.html", adminPageData{
		Header:           "Gogo administration",
		SiteURL:          "/admin/",
		ContentClass:     "colM",
		StaticCSSURLs:    []string{"/admin/static/admin/css/base.css"},
		StaticHeadJSURLs: []string{"/admin/static/admin/js/theme.js"},
	}, []string{dir})
	if err != nil {
		t.Fatalf("RenderTemplate(login with project base template) error = %v", err)
	}
	if !strings.Contains(rendered, `id="login-form"`) || strings.Contains(rendered, "project layout") {
		t.Fatalf("admin template used non-admin project base override:\n%s", rendered)
	}
}

func TestAdminTemplatesPreferModelSpecificOverride(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "admin", "blog", "post")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	override := `{{define "content"}}<div id="model-specific-change-form">{{.AppLabel}}.{{.ModelName}}</div>{{end}}`
	if err := os.WriteFile(filepath.Join(modelDir, "change_form.html"), []byte(override), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	rendered, err := RenderTemplate("change_form.html", adminPageData{
		AppLabel:  "blog",
		ModelName: "Post",
		Title:     "Change post",
		Header:    "Gogo administration",
		SiteURL:   "/admin/",
		Form:      adminFormData{ID: "post_form"},
	}, []string{dir})
	if err != nil {
		t.Fatalf("RenderTemplate(change_form with model override) error = %v", err)
	}
	for _, want := range []string{
		`<div id="site-name"><a href="/admin/">Gogo administration</a></div>`,
		`id="model-specific-change-form"`,
		`blog.Post`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("model-specific override rendered output missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `id="post_form"`) {
		t.Fatalf("model-specific override did not replace embedded change form:\n%s", rendered)
	}
}

func TestAdminPasswordChangeTemplateIncludesHiddenUsername(t *testing.T) {
	rendered, err := RenderTemplate("password_change.html", map[string]any{
		"CSRFToken": "token",
		"UserName":  "admin",
	}, nil)
	if err != nil {
		t.Fatalf("RenderTemplate(password_change) error = %v", err)
	}
	want := `name="username" value="admin" autocomplete="username" hidden`
	if !strings.Contains(rendered, want) {
		t.Fatalf("password change template missing %q:\n%s", want, rendered)
	}
}

func TestAdminFormTemplatesUseDjangoFieldFlexContainers(t *testing.T) {
	for _, name := range []string{"templates/change_form.html", "templates/password_change.html"} {
		body, ok := ReadAsset(name)
		if !ok {
			t.Fatalf("ReadAsset(%s) missing", name)
		}
		if !strings.Contains(string(body), `class="flex-container`) {
			t.Fatalf("%s missing Django flex-container field layout", name)
		}
	}
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
