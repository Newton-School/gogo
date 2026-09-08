package tasks_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Newton-School/gogo/core/tasks"
)

type privateCause struct{ calls *int }

func (e *privateCause) Error() string { *e.calls++; panic("private error must not be formatted") }

func TestAcceptanceErrorPreservesCauseWithoutFormattingIt(t *testing.T) {
	calls := 0
	cause := &privateCause{calls: &calls}
	err := &tasks.AcceptanceError{ID: "identity", Confirmed: true, Cause: cause}
	for _, format := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
		if got := fmt.Sprintf(format, err); got != err.Error() {
			t.Fatal("unexpected safe error formatting", got)
		}
	}
	var retained *privateCause
	if !errors.As(err, &retained) || retained != cause || calls != 0 || !err.Confirmed || err.ID != "identity" {
		t.Fatal("acceptance identity or cause was not preserved")
	}
	var nilError *tasks.AcceptanceError
	if nilError.Unwrap() != nil || nilError.Error() == "" {
		t.Fatal("nil acceptance error is unsafe")
	}
}

func TestFailureIsPortableTerminalMetadata(t *testing.T) {
	err := tasks.Failure{Code: "REVOKED", Message: "Task did not succeed"}
	var failure tasks.Failure
	if !errors.As(err, &failure) || failure.Code != "REVOKED" || errors.Is(err, tasks.ErrResultExpired) {
		t.Fatal("task outcome conflated with payload expiry")
	}
}
