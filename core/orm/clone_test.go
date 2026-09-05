package orm

import (
	"github.com/Newton-School/gogo/core/db"
	"reflect"
	"testing"
)

func TestQueryPredicateSnapshotsMutableValues(t *testing.T) {
	values := []int64{1, 2}
	inner := map[string]any{"ids": values}
	predicate := db.Predicate{Value: inner, Children: []db.Predicate{{Value: values}}}
	copy := clonePredicate(predicate)
	values[0] = 9
	inner["extra"] = true
	predicate.Children[0].Value = "changed"
	if got := copy.Value.(map[string]any)["ids"].([]int64); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatal(got)
	}
	if _, exists := copy.Value.(map[string]any)["extra"]; exists {
		t.Fatal("query map aliased caller")
	}
	if got := copy.Children[0].Value.([]int64); got[0] != 1 {
		t.Fatal(got)
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	cloned := cloneQueryValue(cycle).(map[string]any)
	cloned["local"] = true
	if _, exists := cycle["local"]; exists {
		t.Fatal("cyclic map remained aliased")
	}
	if cloned["self"].(map[string]any)["local"] != true {
		t.Fatal("cycle identity was lost")
	}
}
