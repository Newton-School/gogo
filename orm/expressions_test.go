package orm

import (
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestExpressionValuesAreParameterized(t *testing.T) {
	value := "x' OR 1=1 --"
	fragment, err := CompileExpression(postgres.New(), Compare(F("title"), "=", Value(value)), 1)
	if err != nil {
		t.Fatalf("CompileExpression() error = %v", err)
	}
	if strings.Contains(fragment.SQL, value) {
		t.Fatalf("SQL contains raw value: %q", fragment.SQL)
	}
	if fragment.SQL != `"title" = $1` || len(fragment.Args) != 1 || fragment.Args[0] != value {
		t.Fatalf("fragment = %#v", fragment)
	}
}

func TestNestedExpressionsCompile(t *testing.T) {
	expression := Coalesce(
		Cast(F("published_at"), "text"),
		Func("LOWER", Value("UNKNOWN")),
	)
	fragment, err := CompileExpression(postgres.New(), expression, 3)
	if err != nil {
		t.Fatalf("CompileExpression() error = %v", err)
	}
	if fragment.SQL != `COALESCE(CAST("published_at" AS text), LOWER($3))` {
		t.Fatalf("SQL = %q", fragment.SQL)
	}
	if len(fragment.Args) != 1 || fragment.Args[0] != "UNKNOWN" {
		t.Fatalf("Args = %#v", fragment.Args)
	}
}

func TestQObjectCaseSubqueryExistsAndArithmetic(t *testing.T) {
	condition := Q(Filter("status", LookupExact, "published")).
		And(Filter("views", LookupGT, 10)).
		Or(Not(Filter("archived", LookupExact, true)))
	caseExpr := Case(
		When(condition, Value("visible")),
	).Default(Value("hidden"))
	expression := Add(caseExpr, Value("-post"))

	fragment, err := CompileExpression(postgres.New(), expression, 1)
	if err != nil {
		t.Fatalf("CompileExpression() error = %v", err)
	}
	for _, want := range []string{`CASE WHEN`, `"status" = $1`, `"views" > $2`, `NOT ("archived" = $3)`, `THEN $4`, `ELSE $5`, `+ $6`} {
		if !strings.Contains(fragment.SQL, want) {
			t.Fatalf("SQL = %q, missing %q", fragment.SQL, want)
		}
	}
	if len(fragment.Args) != 6 {
		t.Fatalf("Args = %#v", fragment.Args)
	}

	exists, err := CompileExpression(postgres.New(), Exists(Subquery(`SELECT 1 FROM comments WHERE comments.post_id = `+OuterRef("id").SQL())), 1)
	if err != nil {
		t.Fatalf("CompileExpression(exists) error = %v", err)
	}
	if exists.SQL != `EXISTS (SELECT 1 FROM comments WHERE comments.post_id = OUTER."id")` {
		t.Fatalf("exists SQL = %q", exists.SQL)
	}
}

func TestWindowExpressionAndFramesCompile(t *testing.T) {
	fragment, err := CompileExpression(postgres.New(), Over(
		Func("RANK"),
		Window{
			PartitionBy: []Expression{F("author_id")},
			OrderBy:     []string{"-created_at"},
			Frame:       RowsBetween(FrameUnboundedPreceding, FrameCurrentRow),
		},
	), 1)
	if err != nil {
		t.Fatalf("CompileExpression() error = %v", err)
	}
	want := `RANK() OVER (PARTITION BY "author_id" ORDER BY "created_at" DESC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)`
	if fragment.SQL != want {
		t.Fatalf("SQL = %q, want %q", fragment.SQL, want)
	}

	rangeFrame := RangeBetween(FramePreceding(5), FrameFollowing(1))
	if got := rangeFrame.SQL(); got != "RANGE BETWEEN 5 PRECEDING AND 1 FOLLOWING" {
		t.Fatalf("range frame = %q", got)
	}
}

func TestRawSQLRequiresUnsafeMarker(t *testing.T) {
	_, err := CompileExpression(postgres.New(), RawSQL("NOW()"), 1)
	if !errors.Is(err, ErrUnsafeRawSQL) {
		t.Fatalf("CompileExpression(raw) error = %v, want ErrUnsafeRawSQL", err)
	}

	fragment, err := CompileExpression(postgres.New(), UnsafeRawSQL("NOW()"), 1)
	if err != nil {
		t.Fatalf("CompileExpression(unsafe raw) error = %v", err)
	}
	if fragment.SQL != "NOW()" {
		t.Fatalf("SQL = %q", fragment.SQL)
	}
}
