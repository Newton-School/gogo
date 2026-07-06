package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	gogohttp "github.com/Newton-School/gogo/http"
)

func TestAPIRouterRegistersViewSetRoutesCustomActionsAndReverse(t *testing.T) {
	viewset := &ModelViewSet{Store: newMemoryViewSetStore()}
	viewset.RegisterAction("publish", ViewSetAction{
		Handler: func(context.Context, *Request) Response {
			return JSON(http.StatusOK, map[string]any{"published": true})
		},
		Detail:  true,
		Methods: []string{http.MethodPost},
	})

	router := NewRouter()
	if err := router.Register("posts", "post", viewset); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	routes := router.Routes()
	names := make([]string, 0, len(routes))
	patterns := map[string]string{}
	methods := map[string][]string{}
	for _, route := range routes {
		names = append(names, route.Name)
		patterns[route.Name] = route.Pattern
		methods[route.Name] = route.Methods
	}
	wantNames := []string{"post-list", "post-create", "post-detail", "post-update", "post-partial-update", "post-destroy", "post-publish"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("route names = %#v, want %#v", names, wantNames)
	}
	if patterns["post-detail"] != "/posts/<str:id>/" || patterns["post-publish"] != "/posts/<str:id>/publish/" {
		t.Fatalf("patterns = %#v", patterns)
	}
	if !reflect.DeepEqual(methods["post-create"], []string{http.MethodPost}) || !reflect.DeepEqual(methods["post-destroy"], []string{http.MethodDelete}) {
		t.Fatalf("methods = %#v", methods)
	}

	detailURL, err := router.Reverse("post-detail", map[string]any{"id": "42"})
	if err != nil {
		t.Fatalf("Reverse(detail) error = %v", err)
	}
	if detailURL != "/posts/42/" {
		t.Fatalf("detailURL = %q", detailURL)
	}
	customURL, err := router.Reverse("post-publish", map[string]any{"id": "42"})
	if err != nil {
		t.Fatalf("Reverse(custom) error = %v", err)
	}
	if customURL != "/posts/42/publish/" {
		t.Fatalf("customURL = %q", customURL)
	}

	request := NewRequest(httptest.NewRequest(http.MethodPost, "/posts/1/publish/", nil))
	response := router.Resolve(context.Background(), request)
	if response.status != http.StatusOK || response.body.(map[string]any)["published"] != true || request.PathParam("id") != "1" {
		t.Fatalf("custom action response=%#v id=%q", response, request.PathParam("id"))
	}
}

func TestAPIRouterSupportsNestedPrefixesTrailingSlashConfigAndInclude(t *testing.T) {
	router := NewRouter(WithAPIPrefix("api/v1"), WithTrailingSlash(false))
	if err := router.Register("orgs/<str:org_id>/posts", "org-post", &ModelViewSet{Store: newMemoryViewSetStore()}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	url, err := router.Reverse("org-post-detail", map[string]any{"org_id": "acme", "id": "9"})
	if err != nil {
		t.Fatalf("Reverse() error = %v", err)
	}
	if url != "/api/v1/orgs/acme/posts/9" {
		t.Fatalf("url = %q", url)
	}

	child := NewRouter(WithTrailingSlash(false))
	if err := child.Register("comments", "comment", &ModelViewSet{Store: newMemoryViewSetStore()}); err != nil {
		t.Fatalf("child Register() error = %v", err)
	}
	root := NewRouter(WithTrailingSlash(false))
	if err := root.Include("api/v1", child); err != nil {
		t.Fatalf("Include() error = %v", err)
	}
	includedURL, err := root.Reverse("comment-detail", map[string]any{"id": "7"})
	if err != nil {
		t.Fatalf("Reverse(included) error = %v", err)
	}
	if includedURL != "/api/v1/comments/7" {
		t.Fatalf("includedURL = %q", includedURL)
	}
}

func TestAPIRouterMountHTTPRoutesThroughFrameworkRouter(t *testing.T) {
	apiRouter := NewRouter(WithAPIPrefix("api"))
	if err := apiRouter.Handle("blog-item-list", "blog/items", func(context.Context, *Request) Response {
		return JSON(http.StatusOK, map[string]any{"count": 0, "results": []map[string]any{}})
	}, http.MethodGet); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	router := gogohttp.NewRouter()
	if err := apiRouter.MountHTTP(router); err != nil {
		t.Fatalf("MountHTTP() error = %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/blog/items/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("mounted API status = %d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	if body := response.Body.String(); body != "{\"count\":0,\"results\":[]}\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestAPIRouterExceptionHandlerOverridesDefaultErrors(t *testing.T) {
	router := NewRouter(WithExceptionHandler(func(_ context.Context, _ *Request, err error) Response {
		return JSON(http.StatusConflict, map[string]any{"legacy_error": err.Error()})
	}))
	if err := router.Handle("post-only", "/post-only/", func(context.Context, *Request) Response {
		return NoContent()
	}, http.MethodPost); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	notFound := httptest.NewRecorder()
	router.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/missing/", nil))
	if notFound.Code != http.StatusConflict || notFound.Body.String() != "{\"legacy_error\":\"not found\"}\n" {
		t.Fatalf("not found response = %d %q", notFound.Code, notFound.Body.String())
	}

	methodNotAllowed := httptest.NewRecorder()
	router.ServeHTTP(methodNotAllowed, httptest.NewRequest(http.MethodGet, "/post-only/", nil))
	if methodNotAllowed.Code != http.StatusConflict || methodNotAllowed.Body.String() != "{\"legacy_error\":\"method not allowed\"}\n" {
		t.Fatalf("method response = %d %q", methodNotAllowed.Code, methodNotAllowed.Body.String())
	}
}

func TestAPIRouterHandleHTTPServesRawHandlersAndPathValues(t *testing.T) {
	apiRouter := NewRouter(WithAPIPrefix("api"))
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "id="+request.PathValue("id"))
	})
	if err := apiRouter.HandleHTTP("legacy-detail", "legacy/<str:id>", handler, OperationMetadata{Summary: "Legacy detail"}, http.MethodGet); err != nil {
		t.Fatalf("HandleHTTP() error = %v", err)
	}

	direct := httptest.NewRecorder()
	apiRouter.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/api/legacy/42/", nil))
	if direct.Code != http.StatusOK || direct.Body.String() != "id=42" {
		t.Fatalf("direct raw response = %d %q", direct.Code, direct.Body.String())
	}

	mountedRouter := gogohttp.NewRouter()
	if err := apiRouter.MountHTTP(mountedRouter); err != nil {
		t.Fatalf("MountHTTP() error = %v", err)
	}
	mounted := httptest.NewRecorder()
	mountedRouter.ServeHTTP(mounted, httptest.NewRequest(http.MethodGet, "/api/legacy/84/", nil))
	if mounted.Code != http.StatusOK || mounted.Body.String() != "id=84" {
		t.Fatalf("mounted raw response = %d %q", mounted.Code, mounted.Body.String())
	}

	url, err := apiRouter.Reverse("legacy-detail", map[string]any{"id": "7"})
	if err != nil {
		t.Fatalf("Reverse() error = %v", err)
	}
	if url != "/api/legacy/7/" {
		t.Fatalf("url = %q", url)
	}
}
