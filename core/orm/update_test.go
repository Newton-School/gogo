package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type updateDialect struct{}

func (updateDialect) Name() string                           { return "third-party" }
func (updateDialect) Placeholder(i int) string               { return fmt.Sprintf("$%d", i) }
func (updateDialect) FieldType(models.Field) (string, error) { return "integer", nil }
func (updateDialect) QuoteIdentifier(name string) (string, error) {
	if !models.ValidIdentifier(name) {
		return "", errors.New("invalid identifier")
	}
	return `"` + name + `"`, nil
}

type updateBackend struct {
	db.Backend
	tx              updateTransaction
	caps            db.Capabilities
	maximum, begins int
}

func (b *updateBackend) Alias() string                 { return "test" }
func (b *updateBackend) Dialect() db.Dialect           { return updateDialect{} }
func (b *updateBackend) Capabilities() db.Capabilities { return b.caps }
func (b *updateBackend) MaxParameters() int            { return b.maximum }
func (b *updateBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	return &b.tx, nil
}

type updateTransaction struct {
	db.Transaction
	statement                    string
	args                         []any
	execErr, countErr, commitErr error
	count                        int64
	writes, commits, rollbacks   int
}

func (t *updateTransaction) Exec(_ context.Context, statement string, args ...any) (db.Result, error) {
	t.writes++
	t.statement, t.args = statement, append([]any(nil), args...)
	return t, t.execErr
}
func (t *updateTransaction) RowsAffected() (int64, error) { return t.count, t.countErr }
func (t *updateTransaction) Commit() error                { t.commits++; return t.commitErr }
func (t *updateTransaction) Rollback() error              { t.rollbacks++; return nil }

func updateFixture() (*updateBackend, Query[*models.MapRecord]) {
	b := &updateBackend{caps: db.Capabilities{"transactions": true, "update_matched_rows": true}, maximum: 500, tx: updateTransaction{count: 2}}
	schema := models.Schema{AppLabel: "tests", Name: "Counter", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("value"), models.JSONField("payload", models.Nullable)}}
	return b, For(New(b, nil), func() *models.MapRecord { record, _ := models.NewRecord(schema); return record })
}

func TestUpdateSnapshotsAssignmentsBeforeScopeAndRoutesToTransaction(t *testing.T) {
	b, query := updateFixture()
	expression := Add(F("value"), Value(1))
	data := map[string]any{"ids": []int{1, 2}}
	values := map[string]any{"value": expression, "payload": data}
	query = query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
		expression.Args[1].Value = 100
		data["ids"].([]int)[0] = 100
		values["value"] = 99
		return Q("id", 7), nil
	})
	n, err := query.Update(context.Background(), values)
	if err != nil || n != 2 || b.begins != 1 || b.tx.writes != 1 || b.tx.commits != 1 || b.tx.rollbacks != 0 {
		t.Fatal("update transaction result", n, err)
	}
	if !reflect.DeepEqual(b.tx.args, []any{`{"ids":[1,2]}`, 1, 7}) || !strings.Contains(b.tx.statement, `"value" = ("value" + $2) WHERE`) {
		t.Fatal("scope callback mutated assignments", b.tx.statement, b.tx.args)
	}
}

func TestUpdateRejectsUnsupportedOrOversizedWritesWithoutStartingTransaction(t *testing.T) {
	for _, mode := range []string{"capability", "parameters", "canceled", "cyclic"} {
		b, query := updateFixture()
		ctx := context.Background()
		values := map[string]any{"value": Add(F("value"), Value(1))}
		switch mode {
		case "capability":
			delete(b.caps, "update_matched_rows")
		case "parameters":
			b.maximum = 0
		case "canceled":
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		case "cyclic":
			args := []db.Expression{{Kind: "function", Name: "ABS"}}
			args[0].Args = args
			values["value"] = args[0]
		}
		if n, err := query.Update(ctx, values); err == nil || n != 0 || b.begins != 0 {
			t.Fatal("invalid write started transaction", mode, n, err)
		}
	}
}

func TestUpdateDoesNotReplayErrorsOrReportUncertainCount(t *testing.T) {
	for _, mode := range []string{"execute", "count", "negative", "commit"} {
		b, query := updateFixture()
		failure := errors.New("synthetic backend failure")
		switch mode {
		case "execute":
			b.tx.execErr = failure
		case "count":
			b.tx.countErr = failure
		case "negative":
			b.tx.count = -1
		case "commit":
			b.tx.commitErr = &db.Error{Code: db.UnknownCommit}
		}
		if n, err := query.Update(context.Background(), map[string]any{"value": 1}); err == nil || n != 0 || b.begins != 1 || b.tx.writes != 1 || b.tx.rollbacks != 1 {
			t.Fatal("failed update count/replay/rollback wrong", mode, n, err)
		}
	}
}
