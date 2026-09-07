package i18n

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

// Resolve applies explicit context override, current user preference, language
// cookie, Accept-Language, then the configured default. Profile/cookie values
// no longer in the allowlist are ignored. Timezone comes only from an explicit
// override/profile or the project default, never from Accept-Language.
func (r *Resolver) Resolve(request *http.Request) (locale Locale, err error) {
	if r == nil || request == nil {
		return Locale{}, ErrInvalidLocale
	}
	ctx := request.Context()
	if err := ctx.Err(); err != nil {
		return Locale{}, err
	}
	defer func() {
		if recover() != nil {
			locale, err = Locale{}, ErrUnavailable
		}
	}()
	if _, found := FromContext(ctx); found {
		validated, err := r.WithLocale(ctx, Preferences{})
		if err != nil {
			return Locale{}, err
		}
		locale, _ := FromContext(validated)
		return locale, nil
	}
	locale = r.defaultLocale
	preferred := ""
	if r.preferences != nil {
		selection, err := r.preferences(request.Clone(ctx))
		if ctx.Err() != nil {
			return Locale{}, ctx.Err()
		}
		if err != nil {
			return Locale{}, ErrUnavailable
		}
		if tag, err := parseLanguage(selection.Language); err == nil {
			if accepted, ok := r.allowed[tag.String()]; ok {
				preferred = accepted.String()
			}
		}
		if zone, ok := r.zones[selection.TimeZone]; ok {
			locale.zone = zone
		}
	}
	if preferred == "" && r.cookie != "" {
		if boundedHeaders(request.Header.Values("Cookie"), 16<<10, 32) {
			count, value := 0, ""
			for _, cookie := range request.Cookies() {
				if cookie.Name == r.cookie {
					count++
					value = cookie.Value
				}
			}
			if count == 1 {
				if tag, err := parseLanguage(value); err == nil {
					if accepted, ok := r.allowed[tag.String()]; ok {
						preferred = accepted.String()
					}
				}
			}
		}
	}
	if preferred != "" {
		locale.tag = r.allowed[preferred]
	} else {
		values := request.Header.Values("Accept-Language")
		if boundedHeaders(values, 4096, 16) {
			header := strings.Join(values, ",")
			if strings.Count(header, ",") < 32 {
				tags, _, err := language.ParseAcceptLanguage(header)
				if err == nil && len(tags) > 0 {
					_, index, confidence := r.matcher.Match(tags...)
					if confidence != language.No {
						locale.tag = r.languages[index]
					}
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Locale{}, err
	}
	return locale, nil
}

func boundedHeaders(values []string, maximum, lines int) bool {
	if len(values) > lines {
		return false
	}
	total := 0
	for _, value := range values {
		if len(value) > maximum-total {
			return false
		}
		total += len(value)
	}
	return true
}

func vary(header http.Header, name string) {
	for _, line := range header.Values("Vary") {
		for _, field := range strings.Split(line, ",") {
			if value := strings.TrimSpace(field); value == "*" || strings.EqualFold(value, name) {
				return
			}
		}
	}
	header.Add("Vary", name)
}

// Middleware does not rewrite routes, set cookies, or select translations yet.
// It sets Content-Language and cache Vary dimensions. A configured profile hook
// or explicit context selection additionally makes the response private/no-store;
// request-specific preferences must not enter a shared anonymous cache.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		vary(w.Header(), "Accept-Language")
		if r != nil && (r.cookie != "" || r.preferences != nil) {
			vary(w.Header(), "Cookie")
		}
		_, overridden := FromContext(request.Context())
		if r != nil && r.preferences != nil {
			vary(w.Header(), "Authorization")
		}
		if overridden || r != nil && r.preferences != nil {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		locale, err := r.Resolve(request)
		if err != nil || next == nil {
			w.Header().Set("Cache-Control", "private, no-store")
			status := http.StatusServiceUnavailable
			if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			http.Error(w, "Locale selection unavailable", status)
			return
		}
		w.Header().Set("Content-Language", locale.Language())
		next.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), localeKey{}, locale)))
	})
}
