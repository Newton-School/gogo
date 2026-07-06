package orm

import (
	"errors"
	"testing"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
	"github.com/Newton-School/gogo/orm/dialects/sqlite"
)

func TestDatabaseFunctionsRenderSQL(t *testing.T) {
	dialect := postgres.New()
	cases := []struct {
		name string
		expr Expression
		want string
		args int
	}{
		{"collate", Collate(F("name"), "C"), `"name" COLLATE "C"`, 0},
		{"greatest", Greatest(F("score"), Value(10)), `GREATEST("score", $1)`, 1},
		{"least", Least(F("score"), Value(10)), `LEAST("score", $1)`, 1},
		{"nullif", NullIf(F("nickname"), Value("")), `NULLIF("nickname", $1)`, 1},
		{"extract", Extract("year", F("created_at")), `EXTRACT(YEAR FROM "created_at")`, 0},
		{"trunc", Trunc("day", F("created_at")), `DATE_TRUNC($1, "created_at")`, 1},
		{"now", Now(), `NOW()`, 0},
		{"lower", Lower(F("title")), `LOWER("title")`, 0},
		{"upper", Upper(F("title")), `UPPER("title")`, 0},
		{"length", Length(F("title")), `LENGTH("title")`, 0},
		{"substr", Substr(F("title"), Value(1), Value(5)), `SUBSTR("title", $1, $2)`, 2},
		{"replace", Replace(F("title"), Value("a"), Value("b")), `REPLACE("title", $1, $2)`, 2},
		{"concat", Concat(F("first"), Value(" "), F("last")), `CONCAT("first", $1, "last")`, 1},
		{"md5", MD5(Value("x")), `MD5($1)`, 1},
		{"sha256", SHA256(Value("x")), `ENCODE(DIGEST($1, $2), $3)`, 3},
		{"round", Round(F("price"), Value(2)), `ROUND("price", $1)`, 1},
		{"ceil", Ceil(F("price")), `CEIL("price")`, 0},
		{"floor", Floor(F("price")), `FLOOR("price")`, 0},
		{"abs", Abs(F("delta")), `ABS("delta")`, 0},
		{"mod", Mod(F("a"), Value(2)), `MOD("a", $1)`, 1},
		{"power", Power(F("a"), Value(2)), `POWER("a", $1)`, 1},
		{"random", Random(), `RANDOM()`, 0},
		{"json_object", JSONObject("title", F("title"), "views", Value(10)), `jsonb_build_object($1, "title", $2, $3)`, 3},
		{"json_array", JSONArray(F("title"), Value(10)), `jsonb_build_array("title", $1)`, 1},
	}

	for _, tc := range cases {
		fragment, err := CompileExpression(dialect, tc.expr, 1)
		if err != nil {
			t.Fatalf("%s CompileExpression() error = %v", tc.name, err)
		}
		if fragment.SQL != tc.want {
			t.Fatalf("%s SQL = %q, want %q", tc.name, fragment.SQL, tc.want)
		}
		if len(fragment.Args) != tc.args {
			t.Fatalf("%s args = %#v, want %d args", tc.name, fragment.Args, tc.args)
		}
	}
}

func TestUnsupportedDialectFunction(t *testing.T) {
	_, err := CompileExpression(sqlite.New(), SHA256(Value("x")), 1)
	if !errors.Is(err, ErrUnsupportedFunction) {
		t.Fatalf("CompileExpression() error = %v, want ErrUnsupportedFunction", err)
	}
}

func TestFunctionAnnotationIntegration(t *testing.T) {
	query, err := NewQuery(models.Metadata{AppLabel: "blog", ModelName: "Post"}).
		AnnotateExpression("lower_title", Lower(F("title")), postgres.New())
	if err != nil {
		t.Fatalf("AnnotateExpression() error = %v", err)
	}
	annotation := query.Annotations["lower_title"]
	if annotation.SQL != `LOWER("title")` || len(annotation.Args) != 0 {
		t.Fatalf("annotation = %#v", annotation)
	}
}
