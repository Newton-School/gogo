package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFTrustedOriginsFrozenAtConstruction(t *testing.T) {
	origins := []string{"https://trusted.example"}
	protect, err := CSRF(CSRFConfig{TrustedOrigins: origins})
	if err != nil {
		t.Fatal(err)
	}
	var token string
	handler := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { token = CSRFToken(r); w.WriteHeader(204) }))
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "http://example.test", nil))
	cookie := get.Result().Cookies()[0]
	origins[0] = "https://untrusted.example"
	for _, scenario := range []struct {
		origin string
		status int
	}{{"https://trusted.example", 204}, {"https://untrusted.example", 403}} {
		r := httptest.NewRequest("POST", "http://example.test", nil)
		r.AddCookie(cookie)
		r.Header.Set("X-CSRFToken", token)
		r.Header.Set("Origin", scenario.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != scenario.status {
			t.Fatal("constructor slice mutation changed origin trust", scenario.origin, w.Code)
		}
	}
}
