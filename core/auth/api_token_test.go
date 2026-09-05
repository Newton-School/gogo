package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type tokenBackendFunc func(context.Context, string) (Principal, error)

func (f tokenBackendFunc) AuthenticateToken(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}

func TestAPITokenSecretPurposeParsingAndRedaction(t *testing.T) {
	id, secret, digest, err := newAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	parsed, hashed, err := parseAPIToken(secret.Reveal())
	if err != nil || parsed != id || hashed != digest || len(secret.Reveal()) != 85 {
		t.Fatal("issued token cannot authenticate", err)
	}
	for _, input := range []string{"", secret.Reveal() + " ", strings.ToUpper(secret.Reveal()), secret.Reveal()[:84], secret.Reveal() + "A", "reset_" + secret.Reveal()[5:]} {
		if _, _, err := parseAPIToken(input); !errors.Is(err, ErrToken) {
			t.Fatal("malformed token accepted")
		}
	}
	if apiTokenDigest(id, []byte("secret")) == resetDigest(id, []byte("secret")) || apiTokenDigest(id, []byte("secret")) == apiTokenDigest("another-id", []byte("secret")) {
		t.Fatal("digest purpose/identity not bound")
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%f"} {
		for _, value := range []any{secret, &secret, TokenIssue{State: TokenChanged, ID: id, Secret: secret}} {
			if strings.Contains(fmt.Sprintf(verb, value), secret.Reveal()) {
				t.Fatal("credential formatting leak", verb)
			}
		}
	}
	if _, err := json.Marshal(secret); err == nil {
		t.Fatal("credential entered ordinary JSON")
	}
}

func TestBearerMiddlewareDoesNotDowngradeOrPromoteIdentity(t *testing.T) {
	for _, mode := range []string{"good", "invalid", "outage", "panic", "unconstrained", "inactive", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, nextCalls := 0, 0
			middleware, err := BearerMiddleware(tokenBackendFunc(func(_ context.Context, bearer string) (Principal, error) {
				calls++
				if bearer != "private-fixture-token" {
					t.Fatal("credential changed")
				}
				p := Principal{ID: "token-user", Authenticated: true, Active: true, AuthVersion: 1}
				switch mode {
				case "invalid":
					return p, ErrToken
				case "outage":
					return p, errors.New("private provider credential detail")
				case "panic":
					panic("private provider credential detail")
				case "unconstrained":
					return p, nil
				case "inactive":
					p.Active = false
				case "canceled":
					cancel()
				}
				return ConstrainPrincipal(p, []string{"catalog.view_product"})
			}))
			if err != nil {
				t.Fatal(err)
			}
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				if p := FromContext(r.Context()); p.ID != "token-user" || p.tokenScopes == nil {
					t.Fatal("cookie identity retained or scope lost")
				}
				w.WriteHeader(204)
			}))
			r := httptest.NewRequest("GET", "/private?token=ignored", nil).WithContext(WithPrincipal(ctx, Principal{ID: "cookie-user", Authenticated: true, Active: true, Superuser: true}))
			r.Header.Set("Authorization", "bEaReR   private-fixture-token")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 503
			if mode == "good" {
				want = 204
			} else if mode == "invalid" || mode == "inactive" {
				want = 401
			}
			if w.Code != want || calls != 1 || (nextCalls == 1) != (mode == "good") || strings.Contains(w.Body.String(), "private") || len(w.Result().Cookies()) != 0 {
				t.Fatal("unsafe bearer outcome", w.Code, calls, nextCalls)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Vary") != "Authorization" || (want == 401 && w.Header().Get("WWW-Authenticate") == "") {
				t.Fatal("authentication cache/challenge headers missing")
			}
		})
	}
}

func TestBearerMiddlewareRejectsAmbiguousCredentialsBeforeBackend(t *testing.T) {
	middleware, _ := BearerMiddleware(tokenBackendFunc(func(context.Context, string) (Principal, error) {
		t.Fatal("ambiguous credential reached backend")
		return Principal{}, nil
	}))
	handler := middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("credential failure fell back") }))
	for _, values := range [][]string{nil, {""}, {"Basic abc"}, {"Bearer"}, {"Bearer "}, {"Bearer a b"}, {"Bearer a,b"}, {"Bearer a", "Bearer b"}, {"Bearer\tabc"}, {"Bearer " + strings.Repeat("a", 8192)}} {
		r := httptest.NewRequest("GET", "/?token=not-a-header", nil)
		for _, value := range values {
			r.Header.Add("Authorization", value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}
