package constraints

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestUniqueConstraintMetadata(t *testing.T) {
	nullsDistinct := false
	constraint := Unique("", "tenant_id", "slug").
		WithCondition("deleted_at IS NULL").
		WithNullsDistinct(nullsDistinct).
		WithInclude("id").
		WithViolation("unique_slug", "Slug must be unique per tenant.")

	if constraint.Type != TypeUnique {
		t.Fatalf("Type = %q, want %q", constraint.Type, TypeUnique)
	}
	if !reflect.DeepEqual(constraint.FieldNames(), []string{"tenant_id", "slug"}) {
		t.Fatalf("FieldNames() = %#v", constraint.FieldNames())
	}
	if constraint.Condition != "deleted_at IS NULL" {
		t.Fatalf("conditional metadata not preserved: %#v", constraint)
	}
	if constraint.NullsDistinct == nil || *constraint.NullsDistinct {
		t.Fatalf("NullsDistinct = %#v, want explicit false", constraint.NullsDistinct)
	}
	if constraint.ViolationCode != "unique_slug" || constraint.ViolationMessage != "Slug must be unique per tenant." {
		t.Fatalf("violation metadata = (%q, %q)", constraint.ViolationCode, constraint.ViolationMessage)
	}
	if err := constraint.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	name := constraint.NameFor("blog_post")
	if name == "" || len(name) > MaxNameLength {
		t.Fatalf("NameFor() = %q, want non-empty name within max length", name)
	}
	if again := constraint.NameFor("blog_post"); again != name {
		t.Fatalf("NameFor() = %q then %q, want deterministic", name, again)
	}
}

func TestUniqueConstraintRejectsDeferrablePartialCombination(t *testing.T) {
	constraint := Unique("uniq_tenant_slug", "tenant_id", "slug").
		WithCondition("deleted_at IS NULL").
		WithDeferrable(DeferrableDeferred)

	if err := constraint.Validate(); !errors.Is(err, ErrInvalidConstraint) {
		t.Fatalf("Validate() error = %v, want ErrInvalidConstraint", err)
	}
}

func TestCheckAndExclusionConstraintMetadata(t *testing.T) {
	check := Check("", "price >= 0")
	exclusion := Exclude("", Exclusion{
		Expression: "period",
		Operator:   "&&",
		OpClass:    "tsrange_ops",
	}).WithCondition("cancelled_at IS NULL")

	if check.Type != TypeCheck || check.Check != "price >= 0" {
		t.Fatalf("check metadata = %#v", check)
	}
	if exclusion.Type != TypeExclusion || len(exclusion.Exclusions) != 1 {
		t.Fatalf("exclusion metadata = %#v", exclusion)
	}
	if exclusion.Exclusions[0].OpClass != "tsrange_ops" || exclusion.Condition == "" {
		t.Fatalf("exclusion detail metadata = %#v", exclusion)
	}
	for _, constraint := range []Constraint{check, exclusion} {
		if err := constraint.Validate(); err != nil {
			t.Fatalf("Validate(%#v) error = %v", constraint, err)
		}
	}
}

func TestFunctionalConstraintAndClone(t *testing.T) {
	constraint := UniqueExpression("", "LOWER(email)").
		WithFields(Desc("created_at")).
		WithOperatorClasses("varchar_pattern_ops")

	cloned := constraint.Clone()
	cloned.Fields[0].Name = "changed"
	cloned.Expressions[0] = "UPPER(email)"
	cloned.OpClasses[0] = "changed_ops"

	if constraint.Fields[0].Name != "created_at" || constraint.Expressions[0] != "LOWER(email)" || constraint.OpClasses[0] != "varchar_pattern_ops" {
		t.Fatalf("Clone() shared backing storage with original: %#v", constraint)
	}
	if err := constraint.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConstraintValidationFailures(t *testing.T) {
	cases := []Constraint{
		Unique(""),
		Check("", ""),
		Exclude("", Exclusion{Expression: "period"}),
		Check("", "enabled").WithNullsDistinct(true),
		Unique("", "name").WithDeferrable("bad"),
	}

	for _, constraint := range cases {
		if err := constraint.Validate(); !errors.Is(err, ErrInvalidConstraint) {
			t.Fatalf("Validate(%#v) error = %v, want ErrInvalidConstraint", constraint, err)
		}
	}
}

func TestDeterministicConstraintNamesAreDistinctByParts(t *testing.T) {
	first := Unique("", "tenant_id", "slug").NameFor("blog_post")
	second := Unique("", "tenant_id", "title").NameFor("blog_post")
	if first == second {
		t.Fatalf("expected distinct deterministic names, got %q", first)
	}
	if !strings.HasPrefix(first, "blog_post_tenant_id_slug_") {
		t.Fatalf("NameFor() = %q, want readable prefix", first)
	}
}
