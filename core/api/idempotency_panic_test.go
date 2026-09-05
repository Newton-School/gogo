package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
)

type inputPanicBackend struct {
	db.Backend
	begins int
}

func (*inputPanicBackend) Alias() string { return "default" }
func (*inputPanicBackend) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true, "savepoints": true, "row_locks": true}
}
func (b *inputPanicBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	return nil, errors.New("unexpected transaction")
}

type panickingOperationJSON struct{}

func (panickingOperationJSON) MarshalJSON() ([]byte, error) {
	panic("synthetic private serializer detail")
}

func TestOperationInputPanicFailsBeforeTransactionWithoutCommitUncertainty(t *testing.T) {
	backend := &inputPanicBackend{}
	service, err := NewIdempotency(IdempotencyConfig{
		Backend:   backend,
		Authorize: func(context.Context, string, string, Values) error { return nil },
		Redact:    func(_ context.Context, _, _ string, _ Values, body Values) (Values, error) { return body, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "review", Authenticated: true, Active: true})
	mutations := 0
	result, err := service.Execute(ctx, Operation{Scope: "one", Action: "create", Key: "one", Input: Values{"input": panickingOperationJSON{}}}, func(context.Context) (MutationResponse, error) {
		mutations++
		return MutationResponse{}, nil
	})
	if err == nil {
		t.Fatal("panicking serializer accepted")
	}
	public := ghttp.PublicError(err)
	if result.Outcome != MutationUnchanged || backend.begins != 0 || mutations != 0 || public.Status != 400 || public.Code != "INVALID_OPERATION" || strings.Contains(err.Error(), "private") {
		t.Fatal("known input failure reported commit uncertainty or leaked details", result.Outcome, backend.begins, mutations, public)
	}
	if result.Replayed || result.Response.Status != 0 || result.Response.Body != nil || result.Response.Headers != nil {
		t.Fatal("failed input returned a receipt")
	}
}
