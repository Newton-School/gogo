package forms

import (
	"context"
	"testing"
)

func TestCharWhitespacePreservationAndRawLengthValidation(t *testing.T) {
	preserve := false
	field := NewField("password", Char)
	field.Strip = &preserve
	for _, value := range []string{"   ", "  significant password \t", "\n"} {
		cleaned, err := field.clean(context.Background(), value)
		if err != nil || cleaned != value {
			t.Fatal("password whitespace changed", cleaned, err)
		}
	}
	field.MinLength = 3
	if value, err := field.clean(context.Background(), " x "); err != nil || value != " x " {
		t.Fatal("minimum length was not measured on preserved input", value, err)
	}
	field.MaxLength = 4
	if _, err := field.clean(context.Background(), "  x  "); err == nil {
		t.Fatal("maximum length ignored preserved whitespace")
	}
	for _, strip := range []*bool{nil, func() *bool { value := true; return &value }()} {
		field.Strip, field.MinLength = strip, 0
		if value, err := field.clean(context.Background(), " x "); err != nil || value != "x" {
			t.Fatal("default trimming changed", value, err)
		}
		if _, err := field.clean(context.Background(), "   "); err == nil {
			t.Fatal("default required field accepted blank whitespace")
		}
	}
}

func TestCharWhitespaceDeclarationCloneIsIndependent(t *testing.T) {
	strip := false
	field := Field{Name: "password", Kind: Char, Strip: &strip}
	cloned := field.Clone()
	strip = true
	if cloned.Strip == field.Strip || cloned.Strip == nil || *cloned.Strip {
		t.Fatal("whitespace option pointer shared with declaration")
	}
}
