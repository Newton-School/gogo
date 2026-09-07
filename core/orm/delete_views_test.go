package orm

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type deletePrivateScalar struct {
	number int
	label  string
}
type deletePrivateBytes struct{ data []byte }
type deletePrivatePointer struct{ data *int }
type deleteMethodValue struct{ ValueBytes []byte }

func (deleteMethodValue) MarshalJSON() ([]byte, error) { panic("must not marshal callback values") }
func (deleteMethodValue) Value() (driver.Value, error) { panic("must not evaluate callback values") }

type deleteUnusedCodec struct{}

func (deleteUnusedCodec) Encode(any) (any, error) { panic("must not encode callback views") }
func (deleteUnusedCodec) Decode(any) (any, error) { panic("must not decode callback views") }

func deleteTestRecord(t *testing.T, payload any) *models.MapRecord {
	t.Helper()
	schema := models.Schema{AppLabel: "test", Name: "DeleteView", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload")}}
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Set("id", int64(1)); err != nil {
		t.Fatal(err)
	}
	if err := record.Set("payload", payload); err != nil {
		t.Fatal(err)
	}
	record.State().Persisted = true
	record.State().Database = "default"
	return record
}

func TestDeleteViewClonesTypesAliasesCyclesAndDoesNotInvokeMethods(t *testing.T) {
	type node struct {
		Number int
		Next   *node
		Bytes  []byte
	}
	original := &node{Number: 1, Bytes: []byte{1, 2}}
	original.Next = original
	payload := map[string]any{"first": original, "second": original, "methods": deleteMethodValue{[]byte{3}}, "private": deletePrivateScalar{4, "safe"}, "time": time.Now()}
	payload["self"] = payload
	record := deleteTestRecord(t, payload)
	if err := deleteCallback(context.Background(), record, func(_ context.Context, event DeleteEvent) error {
		copy, err := event.Record.Get("payload")
		if err != nil {
			return err
		}
		values := copy.(map[string]any)
		first := values["first"].(*node)
		if first == original || first != values["second"].(*node) || first.Next != first || reflect.ValueOf(values["self"]).Pointer() != reflect.ValueOf(values).Pointer() {
			t.Fatal("clone lost types, aliases or cycles")
		}
		if _, ok := models.Underlying(event.Record); ok {
			t.Fatal("callback exposes an execution model")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := deleteCallback(context.Background(), record, func(_ context.Context, event DeleteEvent) error {
		value, _ := event.Record.Get("payload")
		value.(map[string]any)["first"].(*node).Bytes[0] = 99
		return nil
	}); !errors.Is(err, ErrDeleteCallbackMutation) || original.Bytes[0] != 1 {
		t.Fatal("nested mutation escaped", err, original.Bytes)
	}
}

func TestDeleteViewGuardHandlesNaNsAndPointerMapKeys(t *testing.T) {
	key := 7
	nan := math.Float64frombits(0x7ff8000000000001)
	nanCycle := map[float64]any{}
	nanCycle[nan] = nanCycle
	for name, payload := range map[string]any{"pointer_keys": map[*int][]byte{&key: {1}}, "nan_values": []any{nan, complex(nan, nan)}, "nan_keys": map[float64]string{nan: "one", nan: "two"}, "nan_cycle": nanCycle} {
		t.Run(name, func(t *testing.T) {
			record := deleteTestRecord(t, payload)
			if err := deleteCallback(context.Background(), record, func(context.Context, DeleteEvent) error { return nil }); err != nil {
				t.Fatal("read-only callback rejected", err)
			}
		})
	}
	record := deleteTestRecord(t, map[*int]string{&key: "value"})
	err := deleteCallback(context.Background(), record, func(_ context.Context, event DeleteEvent) error {
		value, _ := event.Record.Get("payload")
		for pointer := range value.(map[*int]string) {
			*pointer = 8
		}
		return nil
	})
	if !errors.Is(err, ErrDeleteCallbackMutation) || key != 7 {
		t.Fatal("map key mutation escaped", err, key)
	}
}

func TestDeleteViewOverlappingSlicesRemainIsolated(t *testing.T) {
	original := []byte{1, 2, 3}
	copy, err := (&deleteClone{}).value([][]byte{original[:2], original[1:]})
	if err != nil || !reflect.DeepEqual(copy, [][]byte{{1, 2}, {2, 3}}) {
		t.Fatal("overlapping slices changed values", copy, err)
	}
	copy.([][]byte)[1][0] = 9
	if original[1] != 2 {
		t.Fatal("overlapping slice shares canonical storage")
	}
}

func TestDeleteViewRejectsOpaqueMutableValuesAndBounds(t *testing.T) {
	number := 1
	for _, payload := range []any{deletePrivateBytes{[]byte{1}}, deletePrivatePointer{&number}, make(chan int), func() {}} {
		called := false
		err := deleteCallback(context.Background(), deleteTestRecord(t, payload), func(context.Context, DeleteEvent) error { called = true; return nil })
		if !errors.Is(err, ErrDeleteCallbackView) || called {
			t.Fatal("opaque mutable value exposed", err, called)
		}
	}
	var deep any = "leaf"
	for range 130 {
		deep = []any{deep}
	}
	if _, err := (&deleteClone{}).value(deep); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal("depth not bounded", err)
	}
	if _, err := (&deleteClone{nodes: 1 << 20}).value("leaf"); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal("nodes not bounded", err)
	}
	if _, err := (&deleteClone{bytes: 64 << 20}).value([]byte{1}); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal("bytes not bounded", err)
	}
	private := reflect.ValueOf(struct{ nested [2]int }{})
	if (&deleteClone{nodes: (1 << 20) - 1}).immutableField(private, 0) {
		t.Fatal("private scalar traversal bypassed bound")
	}
}

func TestDeleteViewSchemaMetadataDetachedAndRuntimeServicesPreserved(t *testing.T) {
	record := deleteTestRecord(t, map[string]any{"safe": []byte{1}})
	schema := record.Schema()
	schema.Fields[1].Default = map[string]any{"bytes": []byte{2}}
	schema.Fields[1].DefaultFunc = func() any { panic("must not invoke defaults") }
	schema.Fields[1].DefaultID = "test.delete-default"
	schema.Fields[1].Validators = []models.Validator{func(context.Context, any) error { panic("must not invoke validators") }}
	schema.Fields[1].Codec = deleteUnusedCodec{}
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	_ = record.Set("id", int64(1))
	_ = record.Set("payload", map[string]any{"safe": []byte{1}})
	err = deleteCallback(context.Background(), record, func(_ context.Context, event DeleteEvent) error {
		metadata := event.Record.Schema()
		metadata.Fields[1].Default.(map[string]any)["bytes"].([]byte)[0] = 9
		metadata.Fields[0].Column = "another_id"
		metadata.Table = "another_table"
		if event.Record.Schema().DBTable() != schema.DBTable() || event.Record.Schema().Fields[1].Default.(map[string]any)["bytes"].([]byte)[0] != 2 {
			t.Fatal("metadata copies alias")
		}
		return nil
	})
	if err != nil || schema.Fields[1].Default.(map[string]any)["bytes"].([]byte)[0] != 2 {
		t.Fatal("metadata services caused rejection or data escaped", err)
	}
}

func TestDeleteAuthorizationGuardsAllDescriptorsAndState(t *testing.T) {
	for _, mutation := range []string{"update_field", "update_value", "update_record", "join_field", "join_record", "join_endpoint", "database", "provided", "payload"} {
		t.Run(mutation, func(t *testing.T) {
			root, child := deleteTestRecord(t, []byte{1}), deleteTestRecord(t, []byte{2})
			plan := DeletionPlan{Objects: []models.Record{root}, Updates: []FieldUpdate{{child, "payload", []byte{3}}}, JoinRemovals: []JoinRemoval{{child, root, "payload"}}}
			err := authorizeDelete(context.Background(), plan, func(_ context.Context, view DeletionPlan) error {
				switch mutation {
				case "update_field":
					view.Updates[0].Field = "id"
				case "update_value":
					view.Updates[0].Value.([]byte)[0] = 9
				case "update_record":
					view.Updates[0].Record = view.Objects[0]
				case "join_field":
					view.JoinRemovals[0].Field = "id"
				case "join_record":
					view.JoinRemovals[0].Record = view.Objects[0]
				case "join_endpoint":
					view.JoinRemovals[0].Endpoint = view.Updates[0].Record
				case "database":
					view.Objects[0].State().Database = "other"
				case "provided":
					view.Objects[0].State().Provided["id"] = false
				case "payload":
					value, _ := view.Objects[0].Get("payload")
					value.([]byte)[0] = 9
				}
				return nil
			})
			if !errors.Is(err, ErrDeleteCallbackMutation) {
				t.Fatal("mutation accepted", err)
			}
			value, _ := root.Get("payload")
			if value.([]byte)[0] != 1 || plan.Updates[0].Value.([]byte)[0] != 3 || root.State().Database != "default" || !root.State().Provided["id"] {
				t.Fatal("canonical plan changed")
			}
		})
	}
}

func TestDeleteAuthorizationUnchangedComplexUpdateValues(t *testing.T) {
	key := 1
	root := deleteTestRecord(t, nil)
	value := map[*int]any{&key: math.NaN()}
	plan := DeletionPlan{Objects: []models.Record{root}, Updates: []FieldUpdate{{root, "payload", value}}}
	if err := authorizeDelete(context.Background(), plan, func(context.Context, DeletionPlan) error { return nil }); err != nil {
		t.Fatal("unchanged update rejected", err)
	}
}

func TestDeleteScopeMetadataDefaultsAndChoicesAreDetached(t *testing.T) {
	schema := deleteTestRecord(t, nil).Schema()
	schema.Fields[1].Default = map[string]any{"bytes": []byte{1}}
	schema.Fields[1].Choices = []models.Choice{{Value: []byte{2}, Label: "choice"}}
	collector := DeleteCollector{Scope: func(_ context.Context, view models.Schema) (db.Predicate, error) {
		view.Fields[1].Default.(map[string]any)["bytes"].([]byte)[0] = 9
		view.Fields[1].Choices[0].Value.([]byte)[0] = 9
		view.Fields[0].Column = "another_id"
		return Q("id", int64(1)), nil
	}}
	if _, err := collector.deletionScope(context.Background(), schema); err != nil {
		t.Fatal(err)
	}
	if schema.Fields[0].Column != "" || schema.Fields[1].Default.(map[string]any)["bytes"].([]byte)[0] != 1 || schema.Fields[1].Choices[0].Value.([]byte)[0] != 2 {
		t.Fatal("scope changed canonical metadata")
	}
	schema.Fields[1].Default = deletePrivateBytes{[]byte{1}}
	if _, err := collector.deletionScope(context.Background(), schema); !errors.Is(err, ErrDeleteCallbackView) {
		t.Fatal("unsafe scope metadata accepted", err)
	}
}
