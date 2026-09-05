package http_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
)

func TestInfrastructureFailureTakesPrecedenceOverIncompleteValidation(t *testing.T) {
	validation := &models.ValidationError{}
	validation.Add("name", "required", "Name is required.")
	for _, test := range []struct {
		err    error
		status int
	}{{&db.Error{Code: db.Unavailable, Cause: errors.New("private provider endpoint")}, 503}, {&db.Error{Code: db.UnknownCommit}, 503}, {&db.Error{Code: db.Canceled}, 503}, {context.Canceled, 503}, {context.DeadlineExceeded, 504}, {&db.Error{Code: db.UniqueViolation}, 409}, {&db.Error{Code: db.ForeignKeyViolation}, 409}, {&db.Error{Code: db.CheckViolation}, 409}, {&db.Error{Code: db.SerializationFailure}, 500}, {&db.Error{Code: db.Deadlock}, 500}, {&db.Error{Code: db.UnsupportedFeature}, 500}, {errors.New("private custom provider endpoint"), 500}} {
		for _, combined := range []error{errors.Join(validation, test.err), errors.Join(test.err, validation)} {
			public := ghttp.PublicError(combined)
			if public.Status != test.status || len(public.Fields) != 0 || strings.Contains(public.Error(), "private") {
				t.Fatal("infrastructure failure reported as field validation", public)
			}
		}
	}
	if public := ghttp.PublicError(validation); public.Status != 400 || len(public.Fields["name"]) != 1 {
		t.Fatal("ordinary validation changed", public)
	}
}
