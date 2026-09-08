package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestCatalogLimitsAndNilBackend(t *testing.T) {
	for _, backend := range []db.Backend{nil, (*Backend)(nil)} {
		if catalog, err := (Introspector{}).InspectCatalog(context.Background(), backend, db.CatalogOptions{}); err == nil || catalog.Schema != "" || catalog.Relations != nil {
			t.Fatalf("nil backend: %+v %v", catalog, err)
		}
	}
	var budget catalogBudget
	if err := budget.add(catalogTextLimit, strings.Repeat("x", catalogTextLimit+1)); err == nil || budget != 0 {
		t.Fatal("oversized text accepted")
	}
	text := strings.Repeat("x", catalogTextLimit)
	for i := 0; i < catalogByteLimit/catalogTextLimit; i++ {
		if err := budget.add(catalogTextLimit, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := budget.add(catalogTextLimit, "x"); err == nil || budget != catalogByteLimit {
		t.Fatal("aggregate metadata limit not enforced")
	}
}

func TestCatalogTypeMappingNeverGuessesUnknown(t *testing.T) {
	for _, c := range []struct {
		namespace, native, formatted string
		kind                         models.Kind
		digits, places, length       int
	}{
		{"pg_catalog", "numeric", "numeric(5,-2)", models.Decimal, 5, -2, 0},
		{"pg_catalog", "numeric", "numeric(3,5)", models.Decimal, 3, 5, 0},
		{"pg_catalog", "numeric", "numeric", models.Decimal, 0, 0, 0},
		{"pg_catalog", "varchar", "character varying(123)", models.Char, 0, 0, 123},
		{"pg_catalog", "bpchar", "character(7)", models.Char, 0, 0, 7},
		{"pg_catalog", "varchar", "character varying", models.Char, 0, 0, 0},
		{"pg_catalog", "int4range", "int4range", models.Custom, 0, 0, 0},
		{"pg_catalog", "timetz", "time with time zone", models.Custom, 0, 0, 0},
		{"custom", "int4", "custom.int4", models.Custom, 0, 0, 0},
	} {
		field := catalogField("value", c.namespace, c.native, c.formatted)
		if field.Kind != c.kind || field.MaxDigits != c.digits || field.DecimalPlaces != c.places || field.MaxLength != c.length {
			t.Errorf("%s => %+v", c.formatted, field)
		}
	}
}
