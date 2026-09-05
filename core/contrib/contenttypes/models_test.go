package contenttypes

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestContentTypeMetadataAndNaturalKeys(t *testing.T) {
	typeRecord := &ContentType{AppLabel: "catalog", ModelName: "product"}
	if _, err := models.Bind(typeRecord); err != nil {
		t.Fatal(err)
	}
	if typeRecord.NaturalKey() != [2]string{"catalog", "product"} {
		t.Fatal("unstable natural key")
	}
	first, second := Migrations(), Migrations()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("migration metadata is nondeterministic")
	}
	one, err := first[0].Checksum()
	if err != nil {
		t.Fatal(err)
	}
	two, _ := second[0].Checksum()
	if one != two {
		t.Fatal("migration checksum changed")
	}
	first[0].Operations[0].Schema.Fields[0].Name = "changed"
	if second[0].Operations[0].Schema.Fields[0].Name != "id" {
		t.Fatal("caller mutated shared migration metadata")
	}
}
