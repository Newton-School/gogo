package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/auth"
	gogohttp "github.com/Newton-School/gogo/http"
	"github.com/Newton-School/gogo/sessions"
)

func TestAdminLoginViewAllowsStaffAndSetsSession(t *testing.T) {
	users := adminUserStore(t,
		auth.User{AbstractUser: auth.AbstractUser{
			AbstractBaseUser: auth.AbstractBaseUser{ID: 1, Password: fastHash(t, "secret"), IsActive: true},
			Username:         "staff",
			IsStaff:          true,
		}},
	)
	sessionStore := sessions.NewDatabaseStore("secret")
	handler := LoginView(AuthViewConfig{Site: DefaultSite(), UserStore: users, SessionStore: sessionStore, Cookie: sessions.CookieOptions{Name: "sid", Path: "/"}})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loginRequest("staff", "secret", "/admin/"))

	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/admin/" {
		t.Fatalf("login response = %d location %q", recorder.Code, recorder.Header().Get("Location"))
	}
	cookie := recorder.Result().Cookies()[0]
	loaded, ok, err := sessionStore.Load(context.Background(), cookie.Value)
	if err != nil || !ok || loaded.GetString("user_id") != "1" {
		t.Fatalf("session after login = %#v, %v, %v", loaded, ok, err)
	}
}

func TestAdminLoginViewRejectsMissingCSRF(t *testing.T) {
	users := adminUserStore(t,
		auth.User{AbstractUser: auth.AbstractUser{
			AbstractBaseUser: auth.AbstractBaseUser{ID: 1, Password: fastHash(t, "secret"), IsActive: true},
			Username:         "staff",
			IsStaff:          true,
		}},
	)
	handler := LoginView(AuthViewConfig{Site: DefaultSite(), UserStore: users, SessionStore: sessions.NewDatabaseStore("secret"), Cookie: sessions.CookieOptions{Name: "sid"}})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loginRequestWithoutCSRF("staff", "secret", "/admin/"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing csrf login response = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminLoginViewDeniesNonStaffInactiveAndUnsafeNext(t *testing.T) {
	users := adminUserStore(t,
		auth.User{AbstractUser: auth.AbstractUser{
			AbstractBaseUser: auth.AbstractBaseUser{ID: 1, Password: fastHash(t, "secret"), IsActive: true},
			Username:         "plain",
		}},
		auth.User{AbstractUser: auth.AbstractUser{
			AbstractBaseUser: auth.AbstractBaseUser{ID: 2, Password: fastHash(t, "secret"), IsActive: false},
			Username:         "inactive",
			IsStaff:          true,
		}},
		auth.User{AbstractUser: auth.AbstractUser{
			AbstractBaseUser: auth.AbstractBaseUser{ID: 3, Password: fastHash(t, "secret"), IsActive: true},
			Username:         "staff",
			IsStaff:          true,
		}},
	)
	handler := LoginView(AuthViewConfig{Site: DefaultSite(), UserStore: users, SessionStore: sessions.NewDatabaseStore("secret"), Cookie: sessions.CookieOptions{Name: "sid"}})

	for _, username := range []string{"plain", "inactive"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, loginRequest(username, "secret", "/admin/"))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", username, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loginRequest("staff", "secret", "https://evil.example/admin"))
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/admin/" {
		t.Fatalf("unsafe next response = %d location %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestAdminLogoutViewFlushesSession(t *testing.T) {
	sessionStore := sessions.NewDatabaseStore("secret")
	session := sessions.NewSession(time.Hour)
	session.Set("user_id", "1")
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := LogoutView(AuthViewConfig{Site: DefaultSite(), SessionStore: sessionStore, Cookie: sessions.CookieOptions{Name: "sid"}})

	request := httptest.NewRequest("POST", "/admin/logout/", nil)
	request.AddCookie(&http.Cookie{Name: "sid", Value: session.Key})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/admin/login/" {
		t.Fatalf("logout response = %d location %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if _, ok, err := sessionStore.Load(context.Background(), session.Key); err != nil || ok {
		t.Fatalf("old session after logout = %v, %v", ok, err)
	}
}

func adminUserStore(t *testing.T, users ...auth.User) *auth.MemoryUserStore {
	t.Helper()
	store, err := auth.NewMemoryUserStore(users...)
	if err != nil {
		t.Fatalf("NewMemoryUserStore() error = %v", err)
	}
	return store
}

func fastHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := auth.EncodePBKDF2PasswordWithIterations(password, "salt", 1)
	if err != nil {
		t.Fatalf("EncodePBKDF2PasswordWithIterations() error = %v", err)
	}
	return hash
}

func loginRequest(username, password, next string) *http.Request {
	token := "test-csrf-token"
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	form.Set("next", next)
	form.Set(gogohttp.CSRFFormFieldName, token)
	request := httptest.NewRequest("POST", "/admin/login/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: gogohttp.CSRFCookieName, Value: token})
	return request
}

func loginRequestWithoutCSRF(username, password, next string) *http.Request {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	form.Set("next", next)
	request := httptest.NewRequest("POST", "/admin/login/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}
