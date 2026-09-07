package api

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
)

func TestReceiptPruneRejectsInvalidAuthorityAndBoundsBeforeIO(t *testing.T) {
	active := auth.WithPrincipal(context.Background(), auth.Principal{ID: "maintenance", Authenticated: true, Active: true})
	canceled, cancel := context.WithCancel(active)
	cancel()
	for _, test := range []struct {
		name    string
		ctx     context.Context
		limit   int
		enabled bool
		want    error
	}{
		{"disabled", active, 1, false, auth.ErrPermissionDenied},
		{"anonymous", context.Background(), 1, true, auth.ErrUnauthenticated},
		{"inactive", auth.WithPrincipal(active, auth.Principal{ID: "inactive", Authenticated: true}), 1, true, auth.ErrPermissionDenied},
		{"missing_identity", auth.WithPrincipal(active, auth.Principal{Authenticated: true, Active: true}), 1, true, auth.ErrPermissionDenied},
		{"canceled", canceled, 1, true, context.Canceled},
		{"nil_context", nil, 1, true, nil},
		{"negative", active, -1, true, nil},
		{"oversize", active, 1001, true, nil},
		{"fallback_parameter_budget", active, 497, true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &inputPanicBackend{}
			config := IdempotencyConfig{Backend: backend,
				Authorize: func(context.Context, string, string, Values) error {
					t.Fatal("ordinary operation authority used for maintenance")
					return nil
				},
				Redact: func(context.Context, string, string, Values, Values) (Values, error) {
					t.Fatal("receipt loaded for maintenance")
					return nil, nil
				},
			}
			if test.enabled {
				config.AuthorizePrune = func(context.Context) error { t.Fatal("invalid input reached authorization callback"); return nil }
			}
			service, err := NewIdempotency(config)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Prune(test.ctx, test.limit)
			if err == nil || test.want != nil && !errors.Is(err, test.want) || result.Deleted != 0 || result.Outcome != MutationUnchanged || backend.begins != 0 {
				t.Fatal(result, err, backend.begins)
			}
		})
	}
	var absent *Idempotency
	if result, err := absent.Prune(active, 1); err == nil || result.Outcome != MutationUnchanged {
		t.Fatal(result, err)
	}
}
