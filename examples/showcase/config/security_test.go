package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/security"
)

func TestForwardedHeadersRequireTrustedPeer(t *testing.T) {
	settings, err := Settings().Load(map[string]string{"GOGO_TRUSTED_PROXIES": "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	headers, err := SecurityHeaders(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		peer, wantIP string
		secure       bool
	}{{"192.0.2.5:4321", "198.51.100.7", true}, {"203.0.113.8:4321", "203.0.113.8", false}} {
		request := httptest.NewRequest("GET", "http://localhost/", nil)
		request.RemoteAddr = test.peer
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-For", "198.51.100.7")
		headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if security.IsSecure(r) != test.secure || security.ClientIP(r) != test.wantIP {
				t.Errorf("unexpected proxy authority for %s", test.peer)
			}
		})).ServeHTTP(httptest.NewRecorder(), request)
	}
	invalid, err := Settings().Load(map[string]string{"GOGO_TRUSTED_PROXIES": "not-a-network"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SecurityHeaders(invalid); err == nil {
		t.Fatal("malformed proxy network accepted")
	}
}

func TestBoundedHandlerDeadlineCancelsWork(t *testing.T) {
	done := make(chan error, 1)
	handler := http.TimeoutHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); done <- r.Context().Err() }), 10*time.Millisecond, "Request timed out")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 503 || !strings.Contains(response.Body.String(), "Request timed out") {
		t.Fatal("timeout did not produce a safe terminal response")
	}
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request context was not canceled")
	}
}

func TestProxyTLSControlsCSRFOrigin(t *testing.T) {
	settings, err := Settings().Load(map[string]string{"GOGO_TRUSTED_PROXIES": "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	headers, err := SecurityHeaders(settings)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := security.CSRF(security.CSRFConfig{})
	if err != nil {
		t.Fatal(err)
	}
	handler := headers(csrf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(security.CSRFToken(r))) })))
	get := httptest.NewRequest("GET", "http://localhost/", nil)
	get.RemoteAddr = "192.0.2.5:4321"
	get.Header.Set("X-Forwarded-Proto", "https")
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, get)
	if initial.Code != 200 || initial.Body.Len() == 0 {
		t.Fatal("initial CSRF request failed")
	}
	for _, test := range []struct {
		peer   string
		status int
	}{{"192.0.2.5:4321", 200}, {"203.0.113.8:4321", 403}} {
		body := url.Values{"csrfmiddlewaretoken": {initial.Body.String()}}.Encode()
		post := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(body))
		post.RemoteAddr = test.peer
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Origin", "https://localhost")
		post.Header.Set("X-Forwarded-Proto", "https")
		for _, cookie := range initial.Result().Cookies() {
			post.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, post)
		if response.Code != test.status {
			t.Errorf("peer %s: CSRF status %d, want %d", test.peer, response.Code, test.status)
		}
	}
}
