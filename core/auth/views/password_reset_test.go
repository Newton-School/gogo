package views

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/sessions"
)

type resetRequestFunc func(context.Context, string) error

func (f resetRequestFunc) Request(ctx context.Context, identifier string) error {
	return f(ctx, identifier)
}

func resetRequestConfig() PasswordResetRequestConfig {
	l := loginConfig()
	return PasswordResetRequestConfig{Service: resetRequestFunc(func(context.Context, string) error { return nil }), NormalizeIdentifier: auth.NormalizeIdentifier, Limiter: l.Limiter, RateSecret: l.RateSecret}
}
func newResetRequestFixture(t *testing.T, config PasswordResetRequestConfig) *loginFixture {
	t.Helper()
	handler, err := PasswordResetRequest(config)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginFixture{t: t, handler: handler, session: sessions.New(sessions.Record{}), cookies: map[string]*http.Cookie{}}
	if w := f.call("GET", "/reset", nil, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return f
}

func TestResetRequestGenericAcknowledgementAndBoundaries(t *testing.T) {
	for _, mode := range []string{"eligible", "absent", "unavailable", "panic"} {
		t.Run(mode, func(t *testing.T) {
			config := resetRequestConfig()
			calls := 0
			config.Service = resetRequestFunc(func(_ context.Context, id string) error {
				calls++
				if id != "  submitted identity  " {
					t.Fatal("identity changed before backend")
				}
				if mode == "unavailable" {
					return errors.New("private account or provider information")
				}
				if mode == "panic" {
					panic("private account information")
				}
				return nil
			})
			config.OnFailure = func(context.Context, string) { panic("isolated observer") }
			f := newResetRequestFixture(t, config)
			if w := f.call("POST", "/reset", url.Values{"identifier": {"  submitted identity  "}}, ""); w.Code != 403 || calls != 0 {
				t.Fatal(w.Code, calls)
			}
			w := f.call("POST", "/reset", url.Values{"identifier": {"  submitted identity  "}, "user_id": {"ignored"}}, f.token)
			if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "If an eligible account exists") || strings.Contains(w.Body.String(), "submitted identity") || strings.Contains(w.Body.String(), "private") || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal(w.Code, calls, w.Body.String())
			}
			if f.session.Modified() {
				t.Fatal("anonymous reset request modified authenticated session")
			}
		})
	}
	f := newResetRequestFixture(t, resetRequestConfig())
	for _, values := range []url.Values{{"identifier": {"a", "b"}}, {"identifier": {strings.Repeat("x", 513)}}, {"not_identifier": {"a"}}} {
		if w := f.call("POST", "/reset", values, f.token); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := f.call("HEAD", "/reset", nil, ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code)
	}
	if w := f.call("DELETE", "/reset", nil, f.token); w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestResetRequestRateKeysMatchIdentityEquivalenceAndRemainPrivate(t *testing.T) {
	config := resetRequestConfig()
	var keys []string
	config.NormalizeIdentifier = func(id string) (string, error) { return strings.ToLower(strings.TrimSpace(id)), nil }
	config.Limiter = limiterFunc(func(_ context.Context, key string, _ ratelimit.Limit, _ int) (ratelimit.Decision, error) {
		keys = append(keys, key)
		return ratelimit.Decision{Allowed: true}, nil
	})
	f := newResetRequestFixture(t, config)
	for _, id := range []string{" User ", "user"} {
		if w := f.call("POST", "/reset", url.Values{"identifier": {id}}, f.token); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if len(keys) != 4 || !reflect.DeepEqual(keys[:2], keys[2:]) || strings.Contains(strings.Join(keys, ""), "user") || strings.Contains(strings.Join(keys, ""), "192.0.2.1") || !strings.HasPrefix(keys[1], "auth:password_reset:identity:") {
		t.Fatal("unsafe rate buckets", keys)
	}
	for _, unavailable := range []bool{true, false} {
		config = resetRequestConfig()
		config.Service = resetRequestFunc(func(context.Context, string) error { t.Fatal("rate denial reached service"); return nil })
		config.Limiter = limiterFunc(func(context.Context, string, ratelimit.Limit, int) (ratelimit.Decision, error) {
			if unavailable {
				return ratelimit.Decision{}, errors.New("private provider error")
			}
			return ratelimit.Decision{}, nil
		})
		f = newResetRequestFixture(t, config)
		w := f.call("POST", "/reset", url.Values{"identifier": {"user"}}, f.token)
		want := 429
		if unavailable {
			want = 503
		}
		if w.Code != want || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	config = resetRequestConfig()
	config.CSRF.Exempt = func(*http.Request) bool { return true }
	if _, err := PasswordResetRequest(config); err == nil {
		t.Fatal("reset CSRF exemption accepted")
	}
}

func TestResetRequestNormalizerPanicCannotEscapeOrExposeIdentity(t *testing.T) {
	config := resetRequestConfig()
	calls := 0
	var events []string
	config.NormalizeIdentifier = func(value string) (string, error) { panic("private normalization: " + value) }
	config.Service = resetRequestFunc(func(_ context.Context, id string) error {
		calls++
		if id != "sensitive-identity" {
			t.Fatal("identity transformed")
		}
		return nil
	})
	config.OnFailure = func(_ context.Context, code string) { events = append(events, code) }
	f := newResetRequestFixture(t, config)
	w := f.call("POST", "/reset", url.Values{"identifier": {"sensitive-identity"}}, f.token)
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "If an eligible account exists") || strings.Contains(w.Body.String(), "sensitive-identity") || len(events) != 1 || events[0] != "identity_normalization_failed" {
		t.Fatal(w.Code, calls, events)
	}
}
