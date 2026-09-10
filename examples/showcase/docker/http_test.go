package docker_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This opt-in black-box test exercises the actual running Compose image, not
// a replacement app factory. It never sends bootstrap credentials off-host.
func TestLocalComposeHTTP(t *testing.T) {
	base := os.Getenv("SHOWCASE_TEST_HTTP_URL")
	if base == "" {
		t.Skip("set SHOWCASE_TEST_HTTP_URL for the running local Compose stack")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		t.Fatal("Compose test URL must be an HTTP origin at numeric 127.0.0.1")
	}
	base = strings.TrimRight(base, "/")
	password := os.Getenv("SHOWCASE_TEST_ADMIN_PASSWORD")
	if password == "" {
		t.Fatal("SHOWCASE_TEST_ADMIN_PASSWORD is required for the Compose login test")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	read := func(path string) (int, string) {
		t.Helper()
		response, err := client.Get(base + path)
		if err != nil {
			t.Fatal("local HTTP request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			t.Fatal("local HTTP response failed")
		}
		return response.StatusCode, string(body)
	}
	for _, path := range []string{"/", "/fields/", "/forms/", "/health/live/", "/health/ready/", "/api/schema/", "/cache-demo/"} {
		if status, _ := read(path); status != http.StatusOK {
			t.Fatalf("%s returned %d", path, status)
		}
	}
	if status, body := read("/api/v1/products/"); status != http.StatusOK || !strings.Contains(body, "Workspace notebook") || strings.Contains(body, "Unreleased desk lamp") {
		t.Fatal("public product visibility failed")
	}
	denied := func() {
		t.Helper()
		if status, _ := read("/admin/catalog/product/"); status != http.StatusFound && status != http.StatusSeeOther {
			t.Fatalf("anonymous Admin returned %d", status)
		}
	}
	denied()
	status, login := read("/admin/login/")
	if status != http.StatusOK {
		t.Fatalf("login page returned %d", status)
	}
	token := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(login)
	if len(token) != 2 {
		t.Fatal("login CSRF token missing")
	}
	post := func(values url.Values) int {
		t.Helper()
		response, err := client.PostForm(base+"/admin/login/", values)
		if err != nil {
			t.Fatal("local login request failed")
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if post(url.Values{"identifier": {"admin"}, "password": {password}}) != http.StatusForbidden {
		t.Fatal("login did not require CSRF")
	}
	if post(url.Values{"csrfmiddlewaretoken": {token[1]}, "identifier": {"admin"}, "password": {"intentionally-invalid"}}) != http.StatusUnauthorized {
		t.Fatal("invalid password was not rejected")
	}
	denied()
	status = post(url.Values{"csrfmiddlewaretoken": {token[1]}, "identifier": {"admin"}, "password": {password}, "next": {"/admin/"}})
	if status != http.StatusFound && status != http.StatusSeeOther {
		t.Fatalf("valid login returned %d", status)
	}
	if status, body := read("/admin/catalog/product/"); status != http.StatusOK || !strings.Contains(body, "Unreleased desk lamp") {
		t.Fatal("authenticated Admin view failed")
	}
	if status, _ := read("/admin/fieldlab/specimen/add/"); status != http.StatusForbidden {
		t.Fatal("read-only specimen creation became accessible")
	}
}
