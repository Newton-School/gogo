package forms

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type modelFailureChecker struct {
	failure error
	cancel  context.CancelFunc
}

func (c modelFailureChecker) ValidateUnique(context.Context, models.Record, []string) error {
	if c.cancel != nil {
		c.cancel()
	}
	return c.failure
}
func (modelFailureChecker) ValidateConstraints(context.Context, models.Record, []string) error {
	return nil
}

func TestModelFormPreservesOperationalValidationWithoutDisclosingItsMessages(t *testing.T) {
	provider := errors.New("synthetic private provider details")
	validation := &models.ValidationError{}
	validation.Add("Name", "invalid", "Synthetic field-only rejection")
	for _, mode := range []string{"success", "validation", "provider", "joined", "cancel", "cancel_nil"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checker := modelFailureChecker{}
			switch mode {
			case "validation":
				checker.failure = validation
			case "provider":
				checker.failure = provider
			case "joined":
				checker.failure = errors.Join(validation, provider)
			case "cancel":
				checker.failure = errors.Join(validation, context.Canceled)
			case "cancel_nil":
				checker.cancel = cancel
			}
			record, _ := models.Bind(&modelSample{Name: "Before", Role: "member"})
			store := &formStore{}
			form, err := NewModelForm(ctx, record, ModelFormOptions{Fields: []string{"Name"}, Checker: checker, Persistence: store}, WithData(url.Values{"Name": {"After"}}))
			if err != nil {
				t.Fatal(err)
			}
			valid := form.IsValid()
			if mode == "success" {
				if !valid || form.Err() != nil {
					t.Fatal("success failed", form.Err())
				}
				return
			}
			if valid {
				t.Fatal("invalid model accepted")
			}
			if mode == "validation" {
				if form.Err() != nil || !form.HasError("Name", "invalid") {
					t.Fatal("field validation misclassified", form.Err())
				}
				return
			}
			cause := provider
			if strings.HasPrefix(mode, "cancel") {
				cause = context.Canceled
			}
			if !errors.Is(form.Err(), cause) || strings.Contains(form.Err().Error(), provider.Error()) {
				t.Fatal("operational cause lost or message disclosed", form.Err())
			}
			body, err := form.Render("div")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), provider.Error()) || strings.Contains(string(body), "Synthetic field-only rejection") {
				t.Fatal("operational validation disclosed partial field/provider messages")
			}
			for _, operation := range []func() error{func() error { _, err := form.Save(false); return err }, func() error { _, err := form.Save(true); return err }, form.SaveM2M, func() error { return form.CheckRelations(ctx) }, func() error { _, err := form.CleanedRelations(); return err }} {
				if !errors.Is(operation(), cause) {
					t.Fatal("public model form operation erased provider failure")
				}
			}
			if len(store.events) != 0 {
				t.Fatal("operationally failed form started persistence")
			}
		})
	}
}
