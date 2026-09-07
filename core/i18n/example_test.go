package i18n_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func ExampleResolver_Middleware() {
	locales, err := i18n.New(i18n.Config{Languages: []string{"en", "fr"}})
	if err != nil {
		panic(err)
	}
	handler := locales.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locale, _ := i18n.FromContext(r.Context())
		fmt.Fprint(w, locale.Language())
	}))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept-Language", "fr-CA")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	fmt.Println(response.Code, response.Header().Get("Content-Language"), response.Body.String())
	// Output: 200 fr fr
}

func ExampleResolver_WithLocale() {
	locales, err := i18n.New(i18n.Config{
		Languages: []string{"en", "hi"},
		TimeZones: []string{"UTC", "Asia/Kolkata"},
	})
	if err != nil {
		panic(err)
	}
	ctx, err := locales.WithLocale(context.Background(), i18n.Preferences{Language: "hi", TimeZone: "Asia/Kolkata"})
	if err != nil {
		panic(err)
	}
	locale, _ := i18n.FromContext(ctx)
	instant := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	fmt.Println(locale.Language(), locale.LocalTime(instant).Format(time.RFC3339))
	fmt.Println(instant.Format(time.RFC3339))
	// Output:
	// hi 2026-01-01T05:30:00+05:30
	// 2026-01-01T00:00:00Z
}
