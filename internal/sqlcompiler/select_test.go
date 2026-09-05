package sqlcompiler

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type numberedDialect struct{}

func (numberedDialect) Name() string                           { return "postgres" }
func (numberedDialect) Placeholder(index int) string           { return fmt.Sprintf("$%d", index) }
func (numberedDialect) FieldType(models.Field) (string, error) { return "text", nil }
func (numberedDialect) QuoteIdentifier(name string) (string, error) {
	if !models.ValidIdentifier(name) {
		return "", errors.New("invalid identifier")
	}
	return `"` + name + `"`, nil
}

func TestSelectProjectionNestedJoinAndRootArgumentsFollowSQLOrder(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Node", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("parent"), models.TextField("tenant")}}
	query := db.Select{Table: schema.DBTable(), Alias: "root", Fields: []string{"id", "parent__parent__id"}, Projections: []db.Projection{{Expression: db.Expression{Kind: "value", Value: "projection-value"}, Alias: "constant"}}, Joins: []db.Join{
		{Path: "parent", Alias: "first", Schema: schema, ParentField: "parent", TargetField: "id", Where: db.Predicate{Field: "tenant", Value: "first-scope"}},
		{Path: "parent__parent", ParentPath: "parent", Alias: "second", Schema: schema, ParentField: "parent", TargetField: "id", Where: db.Predicate{Field: "tenant", Value: "second-scope"}},
	}, Where: db.Predicate{Field: "tenant", Value: "root-scope"}}
	statement, args, err := Select(numberedDialect{}, schema, query)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []any{"projection-value", "first-scope", "second-scope", "root-scope"}) {
		t.Fatal("bound argument order", args)
	}
	for _, fragment := range []string{`$1 AS "constant"`, `"first"."tenant" = $2`, `"second"."tenant" = $3`, `"root"."tenant" = $4`} {
		if !strings.Contains(statement, fragment) {
			t.Fatal("placeholder bound to wrong alias", statement, fragment)
		}
	}
}

type advertisedDialect struct {
	numberedDialect
	filter bool
}

func (advertisedDialect) Name() string { return "third_party" }
func (d advertisedDialect) SupportsFeature(feature string) bool {
	return feature == "filtered_aggregates" && d.filter
}

func TestFilteredAggregateUsesPublicCapabilityNotProviderName(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Node", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	predicate := db.Predicate{Field: "tenant", Value: 7}
	expression := db.Expression{Kind: "function", Name: "COUNT", Args: []db.Expression{{Kind: "field", Name: "*"}}, Filter: &predicate}
	query := db.Select{Table: schema.DBTable(), Projections: []db.Projection{{Expression: expression, Alias: "count"}}}
	for _, dialect := range []db.Dialect{numberedDialect{}, advertisedDialect{filter: false}} {
		if _, _, err := Select(dialect, schema, query); !db.IsCode(err, db.UnsupportedFeature) {
			t.Fatal("unadvertised capability accepted", err)
		}
	}
	statement, args, err := Select(advertisedDialect{filter: true}, schema, query)
	if err != nil || !strings.Contains(statement, `COUNT(*) FILTER (WHERE "tenant" = $1)`) || !reflect.DeepEqual(args, []any{7}) {
		t.Fatal(statement, args, err)
	}
}
