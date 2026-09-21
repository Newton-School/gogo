package auth

import (
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestAccountIdentityDefaultsAndOptions(t *testing.T) {
	for _, kind := range []models.Kind{"", models.Auto, models.BigAuto, models.UUID} {
		t.Run(string(kind), func(t *testing.T) {
			config, err := NewAccountModels(kind)
			if err != nil {
				t.Fatal(err)
			}
			want := kind
			if want == "" {
				want = models.Auto
			}
			for _, schema := range []models.Schema{config.User().Schema(), config.Group().Schema()} {
				pk := schema.PKFields()[0]
				if pk.Kind != want || pk.IsEditable() || pk.Null {
					t.Fatal("invalid identity descriptor", pk)
				}
				if err := schema.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			id, err := config.newID()
			if err != nil {
				t.Fatal(err)
			}
			if kind == models.UUID {
				if !validUUID(id) || config.validID("1") {
					t.Fatal("UUID option lost")
				}
			} else if id != "" || !config.validID("1") || !config.validID("2147483647") {
				t.Fatal("identity must be allocated by INSERT, not in Go")
			}
			if config.validID("2147483648") != (kind == models.BigAuto) {
				t.Fatal("incorrect width")
			}
			for _, invalid := range []string{"", "0", "-1", "+1", "01", " 1", "1.0", "1e0", "9223372036854775808"} {
				if config.validID(invalid) {
					t.Fatalf("accepted noncanonical ID %q", invalid)
				}
			}
		})
	}
	for _, kind := range []models.Kind{models.Char, models.Integer, models.SmallAuto, "arbitrary"} {
		if _, err := NewAccountModels(kind); err == nil {
			t.Fatal("unsupported kind accepted", kind)
		}
	}
}

func TestLegacyUUIDAccountMigrationChecksumIsPreserved(t *testing.T) {
	legacy, err := NewAccountModels(models.UUID)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := legacy.Migrations()[0].Checksum()
	// Captured from the unchanged UUID migration before introducing ID options.
	if err != nil || sum != "b39bd1073b4e94529aef283bed7d1100cd527c30a308a2694cbdfd90d2f5f61c" {
		t.Fatal("existing UUID migration changed", sum, err)
	}
	current, err := Migrations()[0].Checksum()
	if err != nil || current == sum {
		t.Fatal("different schemas must not share a checksum", err)
	}
}
