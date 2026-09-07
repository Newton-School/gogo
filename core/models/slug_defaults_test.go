package models

import (
	"context"
	"strings"
	"testing"
)

func TestSlugDefaultsAndExplicitOverrides(t *testing.T) {
	field := SlugField("slug")
	if field.MaxLength != 50 || !field.DBIndex || field.AllowUnicode {
		t.Fatal("slug constructor defaults", field)
	}
	if err := field.Validate(context.Background(), strings.Repeat("a", 50)); err != nil {
		t.Fatal(err)
	}
	if err := field.Validate(context.Background(), strings.Repeat("a", 51)); err == nil {
		t.Fatal("default slug length not validated")
	}
	field = SlugField("slug", WithMaxLength(75), WithDBIndex(false), WithAllowUnicode(true))
	if field.MaxLength != 75 || field.DBIndex || !field.AllowUnicode {
		t.Fatal("constructor overwrote explicit options", field)
	}
	if err := field.Validate(context.Background(), strings.Repeat("é", 75)); err != nil {
		t.Fatal("explicit rune limit", err)
	}
	field = SlugField("slug", WithMaxLength(0))
	if err := field.Validate(context.Background(), strings.Repeat("a", 100)); err != nil {
		t.Fatal("explicit unlimited override not preserved", err)
	}
}
