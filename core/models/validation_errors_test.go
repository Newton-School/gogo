package models_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type cyclicValidationError struct{}

func (*cyclicValidationError) Error() string   { return "cyclic error" }
func (e *cyclicValidationError) Unwrap() error { return e }

type joinedValidationError struct{ children []error }

func (*joinedValidationError) Error() string     { return "joined errors" }
func (e *joinedValidationError) Unwrap() []error { return e.children }

func TestIsValidationOnlyRejectsMixedMalformedAndUnboundedTrees(t *testing.T) {
	field := models.Invalid("invalid", "Safe field error.")
	validation := &models.ValidationError{}
	validation.Add("name", "invalid", "Safe name error.")
	for _, err := range []error{field, validation, fmt.Errorf("wrapper detail: %w", field), errors.Join(field, validation), &models.FieldError{Code: "invalid", Message: "Safe pointer field error."}} {
		if !models.IsValidationOnly(err) {
			t.Fatal("explicit validation tree rejected")
		}
	}
	deep := field
	for range 66 {
		deep = fmt.Errorf("wrapped: %w", deep)
	}
	wide := make([]error, 1025)
	for i := range wide {
		wide[i] = field
	}
	for _, err := range []error{
		nil, (*models.ValidationError)(nil), (*models.FieldError)(nil), errors.New("private provider detail"),
		errors.Join(validation, errors.New("private provider detail")), errors.Join(field, context.Canceled),
		&cyclicValidationError{}, deep, errors.Join(wide...), &joinedValidationError{}, &joinedValidationError{children: []error{field, nil}},
	} {
		if models.IsValidationOnly(err) {
			t.Fatal("mixed/invalid error tree classified as public validation")
		}
	}
}

type validationStageRow struct {
	models.Base
	ID, Target int64
	Name       string
	definition models.Schema
	clean      func(context.Context) error
}

func (r *validationStageRow) Schema() models.Schema { return r.definition }
func (r *validationStageRow) Clean(ctx context.Context) error {
	if r.clean != nil {
		return r.clean(ctx)
	}
	return nil
}

type validationStageChecker struct {
	call   func(context.Context, string) error
	stages []string
}

func (c *validationStageChecker) run(ctx context.Context, name string) error {
	c.stages = append(c.stages, name)
	return c.call(ctx, name)
}
func (c *validationStageChecker) ValidateRelation(ctx context.Context, _ models.Record, _ models.Field) error {
	return c.run(ctx, "relation")
}
func (c *validationStageChecker) ValidateUnique(ctx context.Context, _ models.Record, _ []string) error {
	return c.run(ctx, "unique")
}
func (c *validationStageChecker) ValidateConstraints(ctx context.Context, _ models.Record, _ []string) error {
	return c.run(ctx, "constraints")
}

func TestFullCleanPreservesOperationalFailuresAtEveryExtensionStage(t *testing.T) {
	for _, stage := range []string{"field", "relation", "model", "unique", "constraints"} {
		for _, mode := range []string{"provider", "mixed", "cancel", "cancel_nil"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("synthetic private provider detail")
				stopped := false
				invoke := func(_ context.Context, current string) error {
					if stopped {
						t.Fatal("extension ran after operational failure")
					}
					if current != stage {
						return nil
					}
					stopped = true
					switch mode {
					case "cancel":
						return context.Canceled
					case "cancel_nil":
						cancel()
						return nil
					case "mixed":
						return errors.Join(models.Invalid("safe", "Safe field error."), failure)
					default:
						return failure
					}
				}
				row := &validationStageRow{Target: 1, Name: "valid"}
				row.definition = models.Schema{AppLabel: "tests", Name: "ValidationStages", Fields: []models.Field{
					models.BigAutoField("ID"),
					models.TextField("Name", models.WithValidators(func(ctx context.Context, _ any) error { return invoke(ctx, "field") })),
					models.ForeignKeyField("Target", models.Relation{Target: "tests.Parent"}),
				}}
				row.clean = func(ctx context.Context) error { return invoke(ctx, "model") }
				record, err := models.Bind(row)
				if err != nil {
					t.Fatal(err)
				}
				err = models.FullClean(ctx, record, models.CleanOptions{}, &validationStageChecker{call: invoke})
				wanted := failure
				if strings.HasPrefix(mode, "cancel") {
					wanted = context.Canceled
				}
				if !errors.Is(err, wanted) || models.IsValidationOnly(err) {
					t.Fatal("provider failure became user input error", err)
				}
				var validation *models.ValidationError
				if errors.As(err, &validation) {
					for _, fields := range validation.Fields {
						for _, field := range fields {
							if strings.Contains(field.Message, "private") {
								t.Fatal("provider detail copied into public field error")
							}
						}
					}
				}
			})
		}
	}
}

func TestFullCleanCollectsPureValidationButPreservesMixedOutage(t *testing.T) {
	row := &validationStageRow{Target: 1, Name: "valid"}
	row.definition = models.Schema{AppLabel: "tests", Name: "ValidationStages", Fields: []models.Field{
		models.BigAutoField("ID"),
		models.TextField("Name", models.WithValidators(func(context.Context, any) error {
			return errors.Join(models.Invalid("first", "First safe error."), models.Invalid("second", "Second safe error."))
		})),
		models.ForeignKeyField("Target", models.Relation{Target: "tests.Parent"}),
	}}
	row.clean = func(context.Context) error { return models.Invalid("model", "Safe model error.") }
	record, _ := models.Bind(row)
	checker := &validationStageChecker{call: func(context.Context, string) error { return models.Invalid("check", "Safe checker error.") }}
	err := models.FullClean(context.Background(), record, models.CleanOptions{}, checker)
	var validation *models.ValidationError
	if !models.IsValidationOnly(err) || !errors.As(err, &validation) || len(validation.Fields["Name"]) != 2 || len(validation.Fields["Target"]) != 1 || len(validation.Fields[models.NonFieldErrors]) != 3 || len(checker.stages) != 3 {
		t.Fatal("pure validation stages or joined field errors lost", err)
	}
	failure := errors.New("synthetic private outage")
	checker.call = func(_ context.Context, stage string) error {
		if stage == "unique" {
			return failure
		}
		return nil
	}
	err = models.FullClean(context.Background(), record, models.CleanOptions{}, checker)
	if !errors.Is(err, failure) || models.IsValidationOnly(err) || !errors.As(err, &validation) || len(validation.Fields["Name"]) != 2 || len(validation.Fields[models.NonFieldErrors]) != 1 {
		t.Fatal("accumulated validation or operational cause lost", err)
	}
}

func TestFieldCleanStopsValidatorsWhenCallbackCancelsWithoutError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	field := models.TextField("name", models.WithValidators(func(context.Context, any) error { cancel(); return nil }, func(context.Context, any) error {
		t.Fatal("validator ran after prior callback canceled")
		return nil
	}))
	if _, err := field.Clean(ctx, "value"); !errors.Is(err, context.Canceled) {
		t.Fatal("nil-result callback cancellation was ignored", err)
	}
}
