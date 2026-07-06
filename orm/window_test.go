package orm

import (
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestWindowHelpersRenderSQL(t *testing.T) {
	dialect := postgres.New()
	cases := []struct {
		name string
		expr Expression
		want string
		args int
	}{
		{"row_number", RowNumber(), `ROW_NUMBER()`, 0},
		{"rank", Rank(), `RANK()`, 0},
		{"dense_rank", DenseRank(), `DENSE_RANK()`, 0},
		{"percent_rank", PercentRank(), `PERCENT_RANK()`, 0},
		{"cume_dist", CumeDist(), `CUME_DIST()`, 0},
		{"ntile", NTile(Value(4)), `NTILE($1)`, 1},
		{"lag", Lag(F("score"), Value(1)), `LAG("score", $1)`, 1},
		{"lead", Lead(F("score"), Value(1)), `LEAD("score", $1)`, 1},
		{"first_value", FirstValue(F("score")), `FIRST_VALUE("score")`, 0},
		{"last_value", LastValue(F("score")), `LAST_VALUE("score")`, 0},
		{"nth_value", NthValue(F("score"), Value(2)), `NTH_VALUE("score", $1)`, 1},
	}
	for _, tc := range cases {
		fragment, err := CompileExpression(dialect, tc.expr, 1)
		if err != nil {
			t.Fatalf("%s CompileExpression() error = %v", tc.name, err)
		}
		if fragment.SQL != tc.want || len(fragment.Args) != tc.args {
			t.Fatalf("%s fragment = %#v", tc.name, fragment)
		}
	}
}
