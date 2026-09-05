package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCycleStopsHooks(t *testing.T) {
	called := false
	_, err := Bootstrap(context.Background(), []Config{{Name: "a", Label: "a", Requires: []string{"b"}, Ready: func(context.Context, *Registry) error { called = true; return nil }}, {Name: "b", Label: "b", Requires: []string{"a"}}}, nil, nil)
	if err == nil || called {
		t.Fatal("cycle not stopped")
	}
}
func TestCleanupAndFrozenRegistry(t *testing.T) {
	var got []string
	configs := []Config{{Name: "b", Label: "b", Requires: []string{"a"}, Shutdown: func(context.Context) error { got = append(got, "b"); return errors.New("failed") }}, {Name: "a", Label: "a", Shutdown: func(context.Context) error { got = append(got, "a"); return nil }}}
	a, err := Bootstrap(context.Background(), configs, []Resource{{Name: "db", Open: func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { got = append(got, "db"); return nil }, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Registry.Register("x", "y", 1); err == nil {
		t.Fatal("late registration")
	}
	if a.Close(context.Background()) == nil {
		t.Fatal("cleanup error lost")
	}
	_ = a.Close(context.Background())
	if !reflect.DeepEqual(got, []string{"b", "a", "db"}) || a.Ready() {
		t.Fatal(got)
	}
}
