package http

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the actual private request parser, builtin CSRF, ModelForm and
// transaction boundary. The backend is a deterministic transaction fixture;
// actual PostgreSQL creation and rollback have separate native coverage.
func FuzzGenericCreateURLFormBoundary(f *testing.F) {
	for _, body := range []string{
		"title=hello", "", "title=", "title=%3Cscript%3Ealert%281%29%3C%2Fscript%3E",
		"title=a&title=b", "title=hello&tenant=other", "title=hello&id=8",
		"title=hello&csrfmiddlewaretoken=forged", "title=%00", "title=%ff",
		"title=%xx", "title=hello;tenant=other", "title=" + strings.Repeat("a", 4096),
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		options, backend := genericCreateOptions(t)
		options.MaxBodyBytes = 4096
		handler, err := NewCreateView(options)
		if err != nil {
			t.Fatal(err)
		}
		cookie, token := genericCreateTokens(t, handler)
		request := genericCreateRequest("POST", body)
		request.AddCookie(cookie)
		request.Header.Set("X-CSRFToken", token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || backend.queries != 0 {
			t.Fatal("request escaped its private transaction/response boundary", response.Code)
		}
		switch response.Code {
		case 303:
			if backend.inserts != 1 || backend.commits != 1 || backend.rollbacks != 0 || backend.stored["tenant"] != "one" || backend.stored["id"] != int64(1) || response.Header().Get("Location") != "/articles/1/" {
				t.Fatal("creation did not retain server identity and scope")
			}
		case 400, 403, 413, 422:
			if backend.begins != 0 || backend.inserts != 0 || backend.commits != 0 || backend.stored != nil || response.Header().Get("Location") != "" {
				t.Fatal("invalid request reached persistence", response.Code)
			}
		default:
			t.Fatal("unexpected create response", response.Code)
		}
		if strings.Contains(strings.ToLower(response.Body.String()), "<script>") {
			t.Fatal("submitted HTML reached invalid-form markup")
		}
	})
}
