package redirects

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/models"
)

const redirectSiteID = "00000000-0000-4000-8000-000000000001"
const redirectOtherID = "00000000-0000-4000-8000-000000000002"

func TestRedirectModelDefaultsValidationAndMigration(t *testing.T) {
	r, err := NewRedirect(strings.ToUpper(redirectSiteID), "/old?x=1", "/new")
	if err != nil || !r.Permanent || r.SiteID != redirectSiteID {
		t.Fatal(r, err)
	}
	if id, err := redirectID(r.ID); err != nil || id != r.ID || r.ID[14] != '4' {
		t.Fatal(r.ID, err)
	}
	record, err := models.Bind(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Permanent = false
	if err := models.ApplyDefaults(record); err != nil || r.Permanent {
		t.Fatal("explicit temporary flag lost", err)
	}
	r.NewPath = ""
	if err := Prepare(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	one, two := Migrations(), Migrations()
	a, err := one[0].Checksum()
	b, err2 := two[0].Checksum()
	if err != nil || err2 != nil || a != b || one[0].Dependencies[0] != sites.Migrations()[0].Key() {
		t.Fatal(a, b, err, err2)
	}
	schema := one[0].Operations[0].Schema
	if err := schema.Validate(); err != nil {
		t.Fatal(err)
	}
	fk, _ := schema.Field("site_id")
	if fk.Relation.Target != "gogo_sites.Site" || fk.Relation.OnDelete != models.Cascade || schema.Constraints[0].Name != SitePathConstraint {
		t.Fatal(schema)
	}
	one[0].Operations[0].Schema.Fields[1].Relation.Target = "changed"
	one[0].Operations[0].Schema.Constraints[0].Fields[0] = "changed"
	if two[0].Operations[0].Schema.Fields[1].Relation.Target != "gogo_sites.Site" || two[0].Operations[0].Schema.Constraints[0].Fields[0] != "site_id" {
		t.Fatal("migration descriptors shared")
	}
	for _, test := range []struct{ site, old, target string }{
		{"", "/", ""}, {"00000000-0000-0000-0000-000000000000", "/", ""},
		{redirectSiteID, "relative", "/"}, {redirectSiteID, "/#fragment", "/"},
		{redirectSiteID, "/" + strings.Repeat("x", MaxURIBytes), "/"},
		{redirectSiteID, "/", "javascript:alert(1)"}, {redirectSiteID, "/", "/bad\r\nLocation: x"},
	} {
		if _, err := NewRedirect(test.site, test.old, test.target); err == nil {
			t.Fatal("invalid model accepted", test)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Clean(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := (*Redirect)(nil).Clean(context.Background()); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	other, _ := models.Bind(&sites.Site{})
	if err := Prepare(context.Background(), other); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}
