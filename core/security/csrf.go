package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type CSRFConfig struct {
	CookieName     string
	Secure         bool
	TrustedOrigins []string
	MaxBodyBytes   int64
	Exempt         func(*http.Request) bool
}
type csrfKey struct{}

func CSRFToken(r *http.Request) string { v, _ := r.Context().Value(csrfKey{}).(string); return v }
func masked(secret []byte) (string, error) {
	mask := make([]byte, 32)
	if _, err := rand.Read(mask); err != nil {
		return "", err
	}
	out := make([]byte, 64)
	copy(out, mask)
	for i := range mask {
		out[32+i] = mask[i] ^ secret[i]
	}
	return base64.RawURLEncoding.EncodeToString(out), nil
}
func unmask(token string) ([]byte, error) {
	if len(token) > 128 {
		return nil, errors.New("invalid CSRF token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 64 {
		return nil, errors.New("invalid CSRF token")
	}
	out := make([]byte, 32)
	for i := range out {
		out[i] = raw[i] ^ raw[32+i]
	}
	return out, nil
}
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || method == http.MethodTrace
}
func sameOrigin(r *http.Request, origin string) bool {
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return false
	}
	scheme := "http"
	if IsSecure(r) {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}
func validOrigin(origin string) bool {
	u, e := url.Parse(origin)
	return e == nil && u.User == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}
func CSRF(config CSRFConfig) (func(http.Handler) http.Handler, error) {
	if config.CookieName == "" {
		config.CookieName = "gogo_csrf"
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = 1 << 20
	}
	if config.MaxBodyBytes < 1 {
		return nil, errors.New("invalid CSRF body limit")
	}
	for _, origin := range config.TrustedOrigins {
		if !validOrigin(origin) {
			return nil, errors.New("invalid trusted CSRF origin")
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if config.Exempt != nil && config.Exempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			var secret []byte
			if cookie, err := r.Cookie(config.CookieName); err == nil && len(cookie.Value) < 128 {
				secret, _ = base64.RawURLEncoding.DecodeString(cookie.Value)
			}
			validCookie := len(secret) == 32
			if !validCookie {
				secret = make([]byte, 32)
				if _, err := rand.Read(secret); err != nil {
					http.Error(w, "security service unavailable", 503)
					return
				}
			}
			if !safeMethod(r.Method) {
				origin := r.Header.Get("Origin")
				trusted := origin != "" && sameOrigin(r, origin)
				if origin != "" {
					for _, allowed := range config.TrustedOrigins {
						trusted = trusted || origin == allowed
					}
				} else if IsSecure(r) {
					referer, e := url.Parse(r.Referer())
					if e == nil && referer.Scheme == "https" {
						trusted = sameOrigin(r, referer.Scheme+"://"+referer.Host)
					}
				} else {
					trusted = true
				}
				if !trusted || !validCookie {
					http.Error(w, "CSRF verification failed", 403)
					return
				}
				token := r.Header.Get("X-CSRFToken")
				if token == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
					r.Body = http.MaxBytesReader(w, r.Body, config.MaxBodyBytes)
					if r.ParseForm() != nil {
						http.Error(w, "invalid form", 400)
						return
					}
					token = r.PostForm.Get("csrfmiddlewaretoken")
				}
				submitted, err := unmask(token)
				if err != nil || subtle.ConstantTimeCompare(secret, submitted) != 1 {
					http.Error(w, "CSRF verification failed", 403)
					return
				}
			}
			token, err := masked(secret)
			if err != nil {
				http.Error(w, "security service unavailable", 503)
				return
			}
			if !validCookie {
				http.SetCookie(w, &http.Cookie{Name: config.CookieName, Value: base64.RawURLEncoding.EncodeToString(secret), Path: "/", Secure: config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			}
			w.Header().Add("Vary", "Cookie")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfKey{}, token)))
		})
	}, nil
}

// RotateCSRF invalidates the browser's CSRF secret on login/credential changes.
func RotateCSRF(w http.ResponseWriter, config CSRFConfig) error {
	if config.CookieName == "" {
		config.CookieName = "gogo_csrf"
	}
	token, err := RandomToken(32)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: config.CookieName, Value: token, Path: "/", Secure: config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	return nil
}
