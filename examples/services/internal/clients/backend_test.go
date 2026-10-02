package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountValidatesRemoteResponses(t *testing.T) {
	for _, test := range []struct {
		body   string
		status int
		valid  bool
	}{
		{`{"count":2}`, 200, true}, {`{}`, 200, false}, {`{"count":-1}`, 200, false},
		{`{"count":2} {}`, 200, false}, {strings.Repeat("x", 4097), 200, false},
		{`{"count":2}`, 500, false}, {`{"count":2}`, 302, false},
	} {
		t.Run(test.body[:min(len(test.body), 30)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/products/count/" {
					t.Error(r.URL.Path)
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			count, err := Count(context.Background(), server.URL)
			if (err == nil) != test.valid || test.valid && count != 2 {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

func TestCountRejectsUnsafeOriginsAndCancellation(t *testing.T) {
	for _, address := range []string{"", "file:///etc/passwd", "http://user:password@localhost", "http://localhost/?token=private", "http://localhost/#fragment"} {
		if _, err := Count(context.Background(), address); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Count(ctx, "http://127.0.0.1:1"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
