package api

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
)

func TestCreateIncompleteValidationCannotHideProviderFailure(t *testing.T) {
	validation := &models.ValidationError{}
	validation.Add("name", "required", "Name is required.")
	for _, test := range []struct {
		err    error
		status int
	}{{&db.Error{Code: db.Unavailable}, 503}, {&db.Error{Code: db.UnknownCommit}, 503}, {context.DeadlineExceeded, 504}, {context.Canceled, 503}, {&db.Error{Code: db.UniqueViolation}, 409}, {&db.Error{Code: db.SerializationFailure}, 500}, {&db.Error{Code: db.Deadlock}, 500}, {&db.Error{Code: db.UnsupportedFeature}, 500}} {
		public := ghttp.PublicError(publicMutationError(errors.Join(validation, test.err)))
		if public.Status != test.status || len(public.Fields) != 0 {
			t.Fatal("failed provider check reported as input error", public)
		}
	}
	if public := ghttp.PublicError(publicMutationError(validation)); public.Status != 422 {
		t.Fatal("completed model validation status changed", public)
	}
	for _, invalid := range []error{validation, &ValidationError{Fields: map[string][]models.FieldError{"name": {{Code: "required", Message: "Name is required."}}}}} {
		public := ghttp.PublicError(publicMutationError(errors.Join(invalid, errors.New("private custom provider endpoint"))))
		if public.Status != 500 || len(public.Fields) != 0 {
			t.Fatal("custom provider failure disclosed incomplete validation", public)
		}
	}
}
