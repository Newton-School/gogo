package testing

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	stdtesting "testing"
	"time"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/sessions"
)

func TestClientRequestHelpersCookiesSessionsAndAuth(t *stdtesting.T) {
	handler := http.NewServeMux()
	handler.HandleFunc("/set-cookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "theme", Value: "dark"})
		w.WriteHeader(http.StatusCreated)
	})
	handler.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		session, _ := sessions.SessionFromContext(r.Context())
		user, _ := auth.UserFromContext(r.Context())
		cookie, _ := r.Cookie("theme")
		fmt.Fprintf(w, "%s name=%s cookie=%s session=%s user=%s", r.Method, r.Form.Get("name"), cookie.Value, session.GetString("mode"), user.Username)
	})
	handler.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, r.Body)
	})
	handler.HandleFunc("/method", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Method", r.Method)
		_, _ = w.Write([]byte(r.Method))
	})

	session := sessions.NewSession(time.Hour)
	session.Set("mode", "test")
	client := NewClient(handler).WithSession(session).ForceLogin(auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 7, IsActive: true},
		Username:         "saksham",
	}})

	client.Get("/set-cookie").AssertStatus(t, http.StatusCreated)
	response := client.PostForm("/echo", url.Values{"name": {"gogo"}})
	response.AssertStatus(t, http.StatusOK)
	response.AssertBodyContains(t, "POST name=gogo cookie=dark session=test user=saksham")

	client.PostJSON("/json", map[string]any{"user": map[string]any{"name": "gogo"}}).AssertJSONPath(t, "user.name", "gogo")

	for _, method := range []struct {
		name string
		call func(string) *Response
	}{
		{name: http.MethodGet, call: client.Get},
		{name: http.MethodPut, call: func(path string) *Response { return client.Put(path, "body") }},
		{name: http.MethodPatch, call: func(path string) *Response { return client.Patch(path, "body") }},
		{name: http.MethodDelete, call: client.Delete},
		{name: http.MethodOptions, call: client.Options},
	} {
		method.call("/method").AssertHeader(t, "X-Method", method.name)
	}
}

func TestClientMultipartRedirectsTemplatesAndFormErrors(t *stdtesting.T) {
	handler := http.NewServeMux()
	handler.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("ParseMultipartForm() error = %v", err)
		}
		file, _, err := r.FormFile("avatar")
		if err != nil {
			t.Fatalf("FormFile() error = %v", err)
		}
		body, _ := io.ReadAll(file)
		fmt.Fprintf(w, "%s:%s", r.FormValue("name"), string(body))
	})
	handler.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	handler.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("new page"))
	})
	handler.HandleFunc("/template", func(w http.ResponseWriter, r *http.Request) {
		RecordTemplate(w, "blog/detail.html")
		_, _ = w.Write([]byte("rendered"))
	})
	handler.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		RecordFormErrors(w, map[string][]string{"title": {"required"}, "__all__": {"invalid"}})
		w.WriteHeader(http.StatusBadRequest)
	})

	client := NewClient(handler)
	client.PostMultipart("/upload", map[string]string{"name": "profile"}, map[string]MultipartFile{
		"avatar": {Filename: "avatar.txt", ContentType: "text/plain", Content: strings.NewReader("file")},
	}).AssertBodyContains(t, "profile:file")

	redirect := client.Get("/old")
	redirect.AssertRedirect(t, "/new")

	followed := client.FollowRedirects(true).Get("/old")
	followed.AssertStatus(t, http.StatusOK)
	followed.AssertBodyContains(t, "new page")
	if len(followed.RedirectChain) != 1 || followed.RedirectChain[0] != "/new" {
		t.Fatalf("RedirectChain = %#v", followed.RedirectChain)
	}

	client.Get("/template").AssertTemplateUsed(t, "blog/detail.html")
	form := client.Get("/form")
	form.AssertStatus(t, http.StatusBadRequest)
	form.AssertFormError(t, "title", "required")
	form.AssertNonFieldError(t, "invalid")
}
