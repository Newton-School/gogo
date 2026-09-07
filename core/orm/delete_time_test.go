package orm

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDeleteTimeLocationCannotMutateCanonicalValue(t *testing.T) {
	location := time.FixedZone("private-zone", 2*60*60)
	original := time.Date(2026, time.July, 1, 0, 0, 0, 0, location)
	record := deleteTestRecord(t, original)
	err := deleteCallback(context.Background(), record, func(_ context.Context, event DeleteEvent) error {
		value, err := event.Record.Get("payload")
		if err != nil {
			return err
		}
		timestamp := value.(time.Time)
		*timestamp.Location() = *time.FixedZone("changed-zone", -12*60*60)
		return nil
	})
	_, offset := original.Zone()
	if !errors.Is(err, ErrDeleteCallbackMutation) || offset != 2*60*60 {
		t.Fatalf("timestamp location alias escaped: error=%v canonical offset=%d", err, offset)
	}
}

func TestDeleteClonePreservesDefinedPointerType(t *testing.T) {
	type pointer *int
	number := 42
	original := pointer(&number)
	copy, err := (&deleteClone{}).value(original)
	if err != nil || reflect.TypeOf(copy) != reflect.TypeOf(original) {
		t.Fatalf("defined pointer type changed: got %T want %T error=%v", copy, original, err)
	}
}

func TestDeleteTimeSnapshotsPreserveZoneRulesWithoutSharingLocations(t *testing.T) {
	for _, timestamp := range []time.Time{time.Now(), time.Time{}, time.Date(2026, time.July, 1, 2, 3, 4, 5, time.FixedZone("zone", 2*60*60))} {
		copy, err := (&deleteClone{}).value(timestamp)
		if err != nil {
			t.Fatal(err)
		}
		cloned := copy.(time.Time)
		if !cloned.Equal(timestamp) || cloned.Format(time.RFC3339Nano) != timestamp.Format(time.RFC3339Nano) || cloned.Location() == timestamp.Location() || cloned != cloned.Round(0) || !reflect.DeepEqual(cloned.Location(), timestamp.Location()) {
			t.Fatal("timestamp snapshot changed wall/zone rules or retained mutable location", cloned, timestamp)
		}
		if err := deleteCallback(context.Background(), deleteTestRecord(t, timestamp), func(context.Context, DeleteEvent) error { return nil }); err != nil {
			t.Fatal("read-only timestamp callback rejected", err)
		}
	}
}

func TestDeleteOpaquePrivateTimeFailsSafelyBeforeCallback(t *testing.T) {
	payload := struct{ timestamp time.Time }{time.Now()}
	called := false
	err := deleteCallback(context.Background(), deleteTestRecord(t, payload), func(context.Context, DeleteEvent) error { called = true; return nil })
	if !errors.Is(err, ErrDeleteCallbackView) || called {
		t.Fatal("private timestamp exposed or panicked", err, called)
	}
}

func TestDeleteTimeMapKeyNormalizationCannotDiscardEntries(t *testing.T) {
	timestamp := time.Now()
	payload := map[time.Time]string{timestamp: "monotonic", timestamp.Round(0): "wall"}
	if len(payload) != 2 {
		t.Fatal("test requires a monotonic timestamp")
	}
	called := false
	err := deleteCallback(context.Background(), deleteTestRecord(t, payload), func(context.Context, DeleteEvent) error { called = true; return nil })
	if !errors.Is(err, ErrDeleteCallbackView) || called || len(payload) != 2 {
		t.Fatal("timestamp normalization lost map entries", err, called)
	}
}
