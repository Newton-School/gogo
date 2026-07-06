package orm

import (
	"strings"
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestBuiltInLookupsCompile(t *testing.T) {
	registry := NewLookupRegistry()
	dialect := postgres.New()
	cases := []struct {
		name   Lookup
		value  any
		args   int
		column string
	}{
		{LookupExact, "published", 1, "status"},
		{LookupIExact, "published", 1, "status"},
		{LookupContains, "go", 1, "title"},
		{LookupIContains, "go", 1, "title"},
		{LookupIn, []any{"draft", "published"}, 2, "status"},
		{LookupGT, 10, 1, "views"},
		{LookupGTE, 10, 1, "views"},
		{LookupLT, 10, 1, "views"},
		{LookupLTE, 10, 1, "views"},
		{LookupRange, []any{1, 10}, 2, "views"},
		{LookupStarts, "go", 1, "title"},
		{LookupIStarts, "go", 1, "title"},
		{LookupEnds, "go", 1, "title"},
		{LookupIEnds, "go", 1, "title"},
		{LookupDate, "2026-06-27", 1, "created_at"},
		{LookupYear, 2026, 1, "created_at"},
		{LookupMonth, 6, 1, "created_at"},
		{LookupDay, 27, 1, "created_at"},
		{LookupWeek, 26, 1, "created_at"},
		{LookupWeekDay, 6, 1, "created_at"},
		{LookupQuarter, 2, 1, "created_at"},
		{LookupTime, "10:30:00", 1, "created_at"},
		{LookupHour, 10, 1, "created_at"},
		{LookupMinute, 30, 1, "created_at"},
		{LookupSecond, 15, 1, "created_at"},
		{LookupIsNull, true, 0, "deleted_at"},
		{LookupRegex, "^go", 1, "title"},
		{LookupIRegex, "^go", 1, "title"},
		{LookupJSONPath, JSONPathValue{Path: []string{"owner", "email"}, Value: "a@example.com"}, 1, "data"},
	}

	for _, tc := range cases {
		fragment, err := registry.Compile(LookupContext{
			Dialect: dialect,
			Column:  tc.column,
			Lookup:  tc.name,
			Value:   tc.value,
			Start:   4,
		})
		if err != nil {
			t.Fatalf("Compile(%s) error = %v", tc.name, err)
		}
		if fragment.SQL == "" {
			t.Fatalf("Compile(%s) returned empty SQL", tc.name)
		}
		if len(fragment.Args) != tc.args {
			t.Fatalf("Compile(%s) args = %#v, want %d args", tc.name, fragment.Args, tc.args)
		}
		if tc.args > 0 && !strings.Contains(fragment.SQL, "$4") {
			t.Fatalf("Compile(%s) SQL = %q, want placeholder offset $4", tc.name, fragment.SQL)
		}
	}
}

func TestLookupsParameterizeValues(t *testing.T) {
	value := "x' OR 1=1 --"
	fragment, err := NewLookupRegistry().Compile(LookupContext{
		Dialect: postgres.New(),
		Column:  "title",
		Lookup:  LookupExact,
		Value:   value,
		Start:   1,
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if strings.Contains(fragment.SQL, value) {
		t.Fatalf("SQL contains raw value: %q", fragment.SQL)
	}
	if len(fragment.Args) != 1 || fragment.Args[0] != value {
		t.Fatalf("Args = %#v", fragment.Args)
	}
}

func TestCustomLookupRegistration(t *testing.T) {
	registry := NewLookupRegistry()
	err := registry.Register("soundex", func(ctx LookupContext) (SQLFragment, error) {
		placeholder := ctx.Placeholder(0)
		return SQLFragment{
			SQL:  "SOUNDEX(" + ctx.QuotedColumn() + ") = SOUNDEX(" + placeholder + ")",
			Args: []any{ctx.Value},
		}, nil
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	fragment, err := registry.Compile(LookupContext{
		Dialect: postgres.New(),
		Column:  "name",
		Lookup:  "soundex",
		Value:   "Smith",
		Start:   2,
	})
	if err != nil {
		t.Fatalf("Compile(custom) error = %v", err)
	}
	if fragment.SQL != `SOUNDEX("name") = SOUNDEX($2)` || fragment.Args[0] != "Smith" {
		t.Fatalf("custom fragment = %#v", fragment)
	}
}
