package humanize_test

import (
	"context"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/core/contrib/humanize"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
)

func Example_humanizeFormatter_Filters() {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	display, err := humanize.New(humanize.Config{Clock: func() time.Time { return now }})
	if err != nil {
		panic(err)
	}
	engine := templates.New(templates.Config{
		Filters: display.Filters(), Libraries: []string{"humanize"},
	})
	out, err := engine.RenderString(context.Background(),
		`{% load humanize %}{{ total|intcomma }}; {{ rank|ordinal }}; {{ published|naturalday }}`,
		templates.Context{"total": "1234567.8900", "rank": 21, "published": now})
	if err != nil {
		panic(err)
	}
	fmt.Println(out)
	// Output: 1,234,567.8900; 21st; today
}

func Example_humanizeFormatter_IntComma() {
	r, err := i18n.New(i18n.Config{Languages: []string{"en", "de"}})
	if err != nil {
		panic(err)
	}
	display, err := humanize.New(humanize.Config{Resolver: r})
	if err != nil {
		panic(err)
	}
	ctx, err := r.WithLocale(context.Background(), i18n.Preferences{Language: "de"})
	if err != nil {
		panic(err)
	}
	out, err := display.IntComma(ctx, "1234567.8900", true)
	if err != nil {
		panic(err)
	}
	fmt.Println(out)
	// Output: 1.234.567,8900
}
