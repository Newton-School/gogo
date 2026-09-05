package messages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

func signer(t *testing.T) *security.Signer {
	t.Helper()
	s, err := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(strings.Repeat("x", 32))}, nil, "flash-test")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func middleware(t *testing.T, c Config) func(http.Handler) http.Handler {
	t.Helper()
	m, err := Middleware(c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestSessionConsumeAndKeepOnServerFailure(t *testing.T) {
	for _, status := range []int{200, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := sessions.New(sessions.Record{})
			if err := s.Set(sessionKey, []Message{{Level: Info, Text: "prior"}}); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/", nil).WithContext(sessions.WithSession(context.Background(), s))
			w := httptest.NewRecorder()
			middleware(t, Config{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				values, err := Consume(r)
				if err != nil || len(values) != 1 {
					t.Fatal(values, err)
				}
				if err := Add(r, Success, "new"); err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(status)
				if err := Add(r, Info, "late"); !errors.Is(err, sessions.ErrCommitted) {
					t.Fatal(err)
				}
			})).ServeHTTP(w, r)
			var kept []Message
			if _, err := s.Get(sessionKey, &kept); err != nil {
				t.Fatal(err)
			}
			want := "new"
			if status == 500 {
				want = "prior"
			}
			if len(kept) != 1 || kept[0].Text != want {
				t.Fatal(kept)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Header())
			}
		})
	}
}
func TestCookieRedirectConsumeTamperAndPrivateDenial(t *testing.T) {
	c := Config{Mode: Cookie, Signer: signer(t), Secure: true}
	w := httptest.NewRecorder()
	middleware(t, c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := Add(r, Info, "private"); !errors.Is(err, ErrPrivateCookie) {
			t.Fatal(err)
		}
		if err := AddPublic(r, Success, "Saved <script>", "record"); err != nil {
			t.Fatal(err)
		}
		http.Redirect(w, r, "/next", 303)
	})).ServeHTTP(w, httptest.NewRequest("POST", "https://example.com/", nil))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal(cookies)
	}
	r := httptest.NewRequest("GET", "https://example.com/next", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	middleware(t, c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, err := Consume(r)
		if err != nil || len(values) != 1 || values[0].Text != "Saved <script>" {
			t.Fatal(values, err)
		}
		if again, err := Consume(r); err != nil || len(again) != 0 {
			t.Fatal(again, err)
		}
	})).ServeHTTP(w, r)
	if cookies = w.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatal(cookies)
	}
	r = httptest.NewRequest("GET", "https://example.com/", nil)
	r.AddCookie(&http.Cookie{Name: "gogo_messages", Value: "tampered"})
	w = httptest.NewRecorder()
	middleware(t, c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, err := Consume(r)
		if err != nil || len(values) != 0 {
			t.Fatal(values, err)
		}
	})).ServeHTTP(w, r)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Code, w.Header())
	}
}
func TestFallbackSizePrivacyAndBounds(t *testing.T) {
	s := sessions.New(sessions.Record{})
	r := httptest.NewRequest("GET", "/", nil).WithContext(sessions.WithSession(context.Background(), s))
	w := httptest.NewRecorder()
	middleware(t, Config{Mode: Fallback, Signer: signer(t), CookieBytes: 256, MaxMessages: 2})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := AddPublic(r, Info, strings.Repeat("large", 100)); err != nil {
			t.Fatal(err)
		}
		if err := Add(r, Warning, "private user notice"); err != nil {
			t.Fatal(err)
		}
		if err := AddPublic(r, Info, "exceeds count"); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	})).ServeHTTP(w, r)
	var values []Message
	if _, err := s.Get(sessionKey, &values); err != nil || len(values) != 2 {
		t.Fatal(values, err)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Value != "" {
			t.Fatal("private text reached signed cookie")
		}
	}
}
func TestLevelsTagsAndDetachedMessages(t *testing.T) {
	s := sessions.New(sessions.Record{})
	r := httptest.NewRequest("GET", "/", nil).WithContext(sessions.WithSession(context.Background(), s))
	middleware(t, Config{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := Add(r, Debug, "hidden"); err != nil {
			t.Fatal(err)
		}
		if err := Add(r, Info, "bad tag", "\" onclick="); err == nil {
			t.Fatal("unsafe tag")
		}
		if err := SetLevel(r, Debug); err != nil {
			t.Fatal(err)
		}
		if err := Add(r, Debug, "shown", "safe"); err != nil {
			t.Fatal(err)
		}
		values, _ := Peek(r)
		values[0].Tags[0] = "changed"
		again, _ := Peek(r)
		if len(again) != 1 || again[0].Tags[0] != "safe" {
			t.Fatal(again)
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
}

type failingStore struct{}

func (failingStore) Load(context.Context, string) (sessions.Record, error) {
	return sessions.Record{}, sessions.ErrNotFound
}
func (failingStore) Create(context.Context, sessions.Record) error {
	return errors.New("store unavailable")
}
func (failingStore) Save(context.Context, sessions.Record, uint64) error {
	return errors.New("store unavailable")
}
func (failingStore) Delete(context.Context, string) error { return errors.New("store unavailable") }
func TestSessionPersistenceFailureDoesNotReturnRedirect(t *testing.T) {
	sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: failingStore{}, Signer: signer(t), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler := sessionMiddleware(middleware(t, Config{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := Add(r, Success, "Saved"); err != nil {
			t.Fatal(err)
		}
		http.Redirect(w, r, "/next", 303)
	})))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestCookieCapacityCheckedBeforeAcceptanceAndLatePeekDenied(t *testing.T) {
	w := httptest.NewRecorder()
	middleware(t, Config{Mode: Cookie, Signer: signer(t), CookieBytes: 500})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := AddPublic(r, Info, strings.Repeat("large", 100)); !errors.Is(err, ErrLimit) {
			t.Fatal("late-only size check", err)
		}
		if err := AddPublic(r, Info, "small"); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(200)
		if _, err := Peek(r); !errors.Is(err, sessions.ErrCommitted) {
			t.Fatal("late cookie access can bypass cache protection", err)
		}
	})).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
