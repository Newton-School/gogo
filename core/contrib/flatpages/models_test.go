package flatpages

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/models"
)

const flatID = "00000000-0000-4000-8000-000000000010"
const flatSiteA = "00000000-0000-4000-8000-000000000001"
const flatSiteB = "00000000-0000-4000-8000-000000000002"
const flatSiteC = "00000000-0000-4000-8000-000000000003"

func TestFlatPageModelsCanonicalFieldsAndExplicitThrough(t *testing.T) {
	p := &FlatPage{ID: flatID, URL: "/caf%C3%a9/%7e", Title: "Page"}
	if err := p.Clean(context.Background()); err != nil || p.URL != "/caf%C3%A9/~" {
		t.Fatal(p, err)
	}
	if _, err := models.Bind(p); err != nil {
		t.Fatal(err)
	}
	mapping := &FlatPageSite{ID: flatID, PageID: flatID, SiteID: flatSiteA, URL: p.URL}
	if err := mapping.Clean(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{p.Schema(), mapping.Schema(), (&sites.Site{}).Schema()} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	field, _ := p.Schema().Field("sites")
	if field.Relation.Through != mapping.Schema().Key() || field.IsStored() || len(registry.All()) != 3 {
		t.Fatal(field, registry.All())
	}
	a, b := Migrations(), Migrations()
	first, err := a[0].Checksum()
	second, other := b[0].Checksum()
	if err != nil || other != nil || first != second || a[0].Dependencies[0] != sites.Migrations()[0].Key() {
		t.Fatal(first, second, err, other)
	}
	a[0].Operations[1].Schema.Constraints[0].Fields[0] = "changed"
	if b[0].Operations[1].Schema.Constraints[0].Fields[0] != "site_id" {
		t.Fatal("shared migration metadata")
	}
	if id, err := newPageID(); err != nil || id[14] != '4' {
		t.Fatal(id, err)
	}
}
