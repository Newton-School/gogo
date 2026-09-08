package humanize

import (
	"context"
	"testing"
	"time"
)

// The ordinary cancellable context remains authoritative. Canceling while the
// locale is read makes the final null-result boundary deterministic, without
// changing the formatter or returning a malformed Context error.
type humanizeCancelOnValueContext struct {
	context.Context
	cancel context.CancelFunc
}

func (c humanizeCancelOnValueContext) Value(key any) any {
	c.cancel()
	return c.Context.Value(key)
}

func TestTemporalNullFiltersObserveCancellationDuringLocaleValue(t *testing.T) {
	for _, name := range []string{"naturalday", "naturaltime"} {
		for _, input := range []struct {
			name  string
			value any
		}{
			{name: "nil"},
			{name: "typed nil", value: (*time.Time)(nil)},
		} {
			for _, canceled := range []bool{false, true} {
				state := "uncanceled"
				if canceled {
					state = "canceled during locale"
				}
				t.Run(name+"/"+input.name+"/"+state, func(t *testing.T) {
					clockCalls := 0
					formatter, err := New(Config{Clock: func() time.Time {
						clockCalls++
						return time.Time{}
					}})
					if err != nil {
						t.Fatal(err)
					}
					base, cancel := context.WithCancel(context.Background())
					defer cancel()
					var ctx context.Context = base
					var wantErr error
					if canceled {
						ctx = humanizeCancelOnValueContext{Context: base, cancel: cancel}
						wantErr = context.Canceled
					}
					if err := ctx.Err(); err != nil {
						t.Fatalf("context canceled before filter entry: %v", err)
					}
					got, err := formatter.Filters()[name](ctx, input.value, nil)
					if got != "" || err != wantErr {
						t.Errorf("filter returned (%#v, %v), want empty text and %v", got, err, wantErr)
					}
					if base.Err() != wantErr {
						t.Errorf("underlying context error = %v, want %v", base.Err(), wantErr)
					}
					if clockCalls != 0 {
						t.Errorf("null input invoked clock %d times", clockCalls)
					}
				})
			}
		}
	}
}
