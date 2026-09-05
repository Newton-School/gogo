package orm

import (
	"database/sql/driver"
	"encoding/json"
	"github.com/Newton-School/gogo/core/db"
	"reflect"
	"sync"
	"testing"
)

type queryValuer struct{ calls int }

func (v *queryValuer) Value() (driver.Value, error) {
	v.calls++
	return "value", nil
}

func TestQuerySnapshotDoesNotCopyProviderState(t *testing.T) {
	provider := &queryValuer{}
	if got := cloneQueryValue(provider); got != provider || provider.calls != 0 {
		t.Fatal("provider copied or evaluated during construction")
	}
	locked := &struct{ Lock sync.Mutex }{}
	locked.Lock.Lock()
	defer locked.Lock.Unlock()
	if got := cloneQueryValue(locked); got != locked {
		t.Fatal("opaque embedded lock state copied")
	}
	raw := json.RawMessage(`{"precise":9007199254740993}`)
	copy := cloneQueryValue(raw).(json.RawMessage)
	raw[0] = '['
	if string(copy) != `{"precise":9007199254740993}` {
		t.Fatal("plain raw JSON was not snapshotted exactly", string(copy))
	}
}

func TestPrefetchRejectsCyclicSpecificationBeforeCloning(t *testing.T) {
	children := []Prefetch{{Path: "parent"}}
	children[0].Children = children
	if _, err := clonePrefetches(children, 0); err == nil {
		t.Fatal("cyclic prefetch specification accepted")
	}
}

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
