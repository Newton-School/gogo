package signals

import (
	"context"
	"errors"
	"testing"
)

func TestSnapshotAndRobust(t *testing.T) {
	var s Signal[int]
	calls := []string{}
	_ = s.Connect("a", 0, func(context.Context, int) error {
		calls = append(calls, "a")
		s.Disconnect("b")
		return errors.New("bad")
	})
	_ = s.Connect("b", 1, func(context.Context, int) error { calls = append(calls, "b"); return nil })
	out, e := s.SendRobust(context.Background(), 1)
	if e == nil || len(out) != 2 || len(calls) != 2 {
		t.Fatal(out, e)
	}
	calls = nil
	out, _ = s.Send(context.Background(), 2)
	if len(out) != 1 {
		t.Fatal(out)
	}
}
func TestPanicBecomesSafeError(t *testing.T) {
	var s Signal[int]
	_ = s.Connect("bad", 0, func(context.Context, int) error { panic("private") })
	_, e := s.Send(context.Background(), 0)
	if e == nil || e.Error() != "receiver bad: signal receiver panicked" {
		t.Fatal(e)
	}
}
