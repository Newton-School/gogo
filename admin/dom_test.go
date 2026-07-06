package admin

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/admin/parity"
)

func TestAdminDOMRegressionCoverage(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want []string
	}{
		{
			name: "index",
			body: renderAdminParityPage(t, parityPageIndex),
			want: []string{`<body class="dashboard"`, `id="content-main" class="app-list"`, `id="recent-actions-module"`},
		},
		{
			name: "app index",
			body: renderAdminParityPage(t, parityPageAppIndex),
			want: []string{`<body class="dashboard app-blog"`, `Models in the blog application`, `class="model-post"`},
		},
		{
			name: "changelist",
			body: renderAdminParityPage(t, parityPageChangeList),
			want: []string{`id="changelist-form"`, `name="action"`, `name="_selected_action"`, `id="result_list"`},
		},
		{
			name: "add",
			body: renderAdminParityPage(t, parityPageAdd),
			want: []string{`<body class="app-blog model-post change-form"`, `id="post_form"`, `name="_addanother"`},
		},
		{
			name: "change",
			body: renderAdminParityPage(t, parityPageChange),
			want: []string{`class="historylink"`, `class="deletelink"`, `value="First parity post"`},
		},
		{
			name: "delete",
			body: renderAdminParityPage(t, parityPageDelete),
			want: []string{`id="deleted-objects"`, `name="post" value="yes"`, `class="button cancel-link"`},
		},
		{
			name: "delete selected",
			body: renderAdminParityPage(t, parityPageDeleteSelected),
			want: []string{`name="_selected_action" value="1"`, `name="action" value="delete_selected"`, `name="select_across"`},
		},
		{
			name: "history",
			body: renderAdminParityPage(t, parityPageHistory),
			want: []string{`id="change-history"`, `<th scope="col">Date/time</th>`, `Changed title`},
		},
		{
			name: "login",
			body: renderAdminParityPage(t, parityPageLogin),
			want: []string{`<body class="login"`, `id="login-form"`, `autocomplete="current-password"`},
		},
		{
			name: "logout",
			body: renderAdminAuthPage(t, "logout"),
			want: []string{`<body class="logout"`, `Logged out`, `Log in again`},
		},
		{
			name: "password change",
			body: renderAdminAuthPage(t, "password_change"),
			want: []string{`<body class="dashboard password-change"`, `id="password-change-form"`, `name="old_password"`, `name="new_password"`},
		},
		{
			name: "popup response",
			body: renderAdminParityPage(t, parityPagePopup),
			want: []string{`id="django-admin-popup-response-constants"`, `data-popup-response=`, `popup_response.js`},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			html := parity.NormalizeHTML(test.body)
			for _, want := range test.want {
				if !strings.Contains(html, want) {
					t.Fatalf("%s DOM missing %q:\n%s", test.name, want, html)
				}
			}
		})
	}
}

func TestAdminAutocompleteDOMRegressionJSON(t *testing.T) {
	body := renderAdminParityPage(t, parityPageAutocomplete)
	for _, want := range []string{`"results"`, `"id":"1"`, `"text":"First parity post"`, `"pagination"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("autocomplete JSON missing %q:\n%s", want, body)
		}
	}
}

func TestAdminInlineDOMRegressionCoverage(t *testing.T) {
	for _, test := range []struct {
		name string
		kind InlineKind
		want []string
	}{
		{
			name: "stacked",
			kind: InlineStacked,
			want: []string{`data-inline-type="stacked"`, `class="inline-related"`, `id="comment_set-0"`, `name="comment_set-0-body"`},
		},
		{
			name: "tabular",
			kind: InlineTabular,
			want: []string{`data-inline-type="tabular"`, `<table>`, `class="original"`, `name="comment_set-0-body"`},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := renderInlineAdminPage(t, test.kind)
			html := parity.NormalizeHTML(body)
			for _, want := range append([]string{
				`id="comment_set-group"`,
				`name="comment_set-TOTAL_FORMS" value="2"`,
				`name="comment_set-INITIAL_FORMS" value="1"`,
				`name="comment_set-0-id" value="10"`,
				`name="comment_set-0-DELETE"`,
			}, test.want...) {
				if !strings.Contains(html, want) {
					t.Fatalf("%s inline DOM missing %q:\n%s", test.name, want, html)
				}
			}
		})
	}
}

func TestAdminReferencedStaticAssetsAreEmbeddedAndSafelyServed(t *testing.T) {
	site := DefaultSite()
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}

	seen := map[string]struct{}{}
	for _, page := range []parityPage{
		parityPageIndex,
		parityPageChangeList,
		parityPageAdd,
		parityPageDelete,
		parityPageDeleteSelected,
		parityPageHistory,
		parityPageLogin,
		parityPagePopup,
	} {
		for _, path := range adminStaticRefs(renderAdminParityPage(t, page)) {
			seen[path] = struct{}{}
		}
	}
	for _, body := range []string{
		renderAdminAuthPage(t, "logout"),
		renderAdminAuthPage(t, "password_change"),
		renderInlineAdminPage(t, InlineStacked),
		renderInlineAdminPage(t, InlineTabular),
	} {
		for _, path := range adminStaticRefs(body) {
			seen[path] = struct{}{}
		}
	}
	if len(seen) == 0 {
		t.Fatalf("no admin static references found")
	}

	for path := range seen {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, response.Code, response.Body.String())
		}
		contentType := response.Header().Get("Content-Type")
		switch {
		case strings.HasSuffix(path, ".css"):
			if !strings.HasPrefix(contentType, "text/css") {
				t.Fatalf("%s content type = %q", path, contentType)
			}
		case strings.HasSuffix(path, ".js"):
			if !strings.Contains(contentType, "javascript") {
				t.Fatalf("%s content type = %q", path, contentType)
			}
		case strings.HasSuffix(path, ".svg"):
			if !strings.HasPrefix(contentType, "image/svg+xml") {
				t.Fatalf("%s content type = %q", path, contentType)
			}
		default:
			if strings.HasPrefix(contentType, "text/html") {
				t.Fatalf("%s unsafe content type = %q", path, contentType)
			}
		}
	}
}

func renderAdminAuthPage(t *testing.T, name string) string {
	t.Helper()
	site := DefaultSite()
	config := AuthViewConfig{Site: site}
	var handler http.Handler
	var request *http.Request
	switch name {
	case "logout":
		handler = LogoutView(config)
		request = httptest.NewRequest(http.MethodGet, "/admin/logout/", nil)
	case "password_change":
		handler = PasswordChangeView(config)
		request = staffAdminRequest(http.MethodGet, "/admin/password_change/")
	default:
		t.Fatalf("unsupported auth page %q", name)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status = %d body=%s", name, response.Code, response.Body.String())
	}
	return response.Body.String()
}

func renderInlineAdminPage(t *testing.T, kind InlineKind) string {
	t.Helper()
	parentMeta, childMeta := inlineTestMetas()
	store := &inlineModelStore{rows: map[string][]map[string]any{
		parentMeta.Label(): {{"id": "1", "title": "Parent"}},
		childMeta.Label():  {{"id": "10", "post_id": "1", "body": "Existing"}},
	}}
	site := DefaultSite()
	site.ModelStore = store
	if err := site.ModelRegistry.RegisterMetadata(parentMeta, ModelAdmin{
		Fields:  []string{"title"},
		Inlines: []Inline{{Model: childMeta.Label(), Kind: kind, Extra: 1, CanDelete: true}},
	}); err != nil {
		t.Fatalf("RegisterMetadata(parent) error = %v", err)
	}
	if err := site.ModelRegistry.RegisterMetadata(childMeta, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata(child) error = %v", err)
	}
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, staffAdminRequest(http.MethodGet, "/admin/blog/post/1/change/"))
	if response.Code != http.StatusOK {
		t.Fatalf("inline change form status = %d body=%s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

var adminStaticRefPattern = regexp.MustCompile(`(?:href|src)="(/admin/static/[^"#?]+)`)

func adminStaticRefs(body string) []string {
	matches := adminStaticRefPattern.FindAllStringSubmatch(body, -1)
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		refs = append(refs, match[1])
	}
	return refs
}
