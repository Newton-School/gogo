package admin

import (
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
)

func TestStockGrantOverridesFailExplicitlyInsteadOfDroppingValidators(t *testing.T) {
	for _, kind := range []AccountGrantKind{UserGroups, UserPermissions, GroupPermissions} {
		site, _ := newTestSite(t)
		options := ModelAdmin{Schema: (&auth.User{}).Schema(), userForms: true, Fields: []string{"identifier", string(kind)}}
		if kind == GroupPermissions {
			options.Schema, options.userForms, options.groupForms, options.Fields = (&auth.Group{}).Schema(), false, true, []string{"name", string(kind)}
		}
		options.FormOverrides = map[string]forms.Field{string(kind): {Kind: forms.MultipleChoice}}
		if err := site.Register(options); err == nil || !strings.Contains(err.Error(), "grant FormOverrides are unsupported") {
			t.Fatal("stock grant validator override silently ignored", kind, err)
		}
	}
}

func TestStockGrantNamesAreOnlyValidInStockFormConfiguration(t *testing.T) {
	for _, mode := range []string{"ordinary", "list", "filter", "ordering"} {
		site, _ := newTestSite(t)
		options := ModelAdmin{Schema: (&auth.User{}).Schema(), Fields: []string{"identifier", "groups"}, userForms: true}
		switch mode {
		case "ordinary":
			options.userForms = false
		case "list":
			options.ListDisplay = []string{"groups"}
		case "filter":
			options.ListFilter = []string{"groups"}
		case "ordering":
			options.Ordering = []string{"groups"}
		}
		if err := site.Register(options); err == nil {
			t.Fatal("unsupported stock grant surface accepted", mode)
		}
	}
}
