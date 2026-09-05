package security

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignPurposeExpiryTamperingAndRotation(t *testing.T) {
	key := SigningKey{ID: "one", Value: []byte(strings.Repeat("x", 32))}
	s, e := NewSigner(key, nil, "session")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	token, _ := s.Sign([]byte("data"))
	if got, e := s.Verify(token, time.Hour); e != nil || string(got) != "data" {
		t.Fatal(e)
	}
	if _, e = s.Verify(token+"x", time.Hour); e == nil {
		t.Fatal("tamper accepted")
	}
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, e = s.Verify(token, time.Hour); !errors.Is(e, ErrExpiredSignature) {
		t.Fatal(e)
	}
	other, _ := NewSigner(key, nil, "other")
	other.now = func() time.Time { return now }
	if _, e = other.Verify(token, time.Hour); e == nil {
		t.Fatal("purpose ignored")
	}
	if _, e = NewSigner(SigningKey{ID: "two", Value: key.Value}, []SigningKey{key}, "session"); e == nil {
		t.Fatal("unbounded fallback accepted")
	}
}
func TestCSRFRejectsForgeryAndAllowsMatchingToken(t *testing.T) {
	middleware, e := CSRF(CSRFConfig{})
	if e != nil {
		t.Fatal(e)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, CSRFToken(r)) }))
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "http://example.test/", nil))
	token := get.Body.String()
	cookie := get.Result().Cookies()[0]
	for _, tc := range []struct {
		origin, token string
		want          int
	}{{"http://example.test", token, 200}, {"http://evil.test", token, 403}, {"http://example.test", "fake", 403}} {
		r := httptest.NewRequest("POST", "http://example.test/", nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-CSRFToken", tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
}
func TestHeadersOnErrorAndProxyNotTrusted(t *testing.T) {
	m, e := Headers(HeadersConfig{AllowedHosts: []string{"example.test"}})
	if e != nil {
		t.Fatal(e)
	}
	h := m(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsSecure(r) {
			t.Error("untrusted proxy changed scheme")
		}
		w.WriteHeader(204)
	}))
	for _, host := range []string{"evil.test", "example.test"} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing error headers")
		}
		if host == "evil.test" && w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
func TestSafeNext(t *testing.T) {
	for _, bad := range []string{"//evil.test", "/\\evil.test", "https://evil.test", "/%2F%2Fevil.test", "/%0d%0ax:1"} {
		if SafeNext(bad, "/safe") != "/safe" {
			t.Fatal(bad)
		}
	}
	if SafeNext("/catalog/?page=2", "/") != "/catalog/?page=2" {
		t.Fatal("valid rejected")
	}
	_ = context.Background()
}
