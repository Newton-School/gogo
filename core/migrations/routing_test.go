package migrations

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// These probes use the real sealed facade, not a wrapper that happens to reject
// SQL. In particular, plan/checksum and historical-editor callbacks must not
// run before an unsupported routing request is refused.
func TestRoutingFacadeRefusesMigrationEntrypointsBeforeCallbacks(t *testing.T) {
	for _, method := range []string{"apply", "reverse", "sql", "sql_reverse", "history"} {
		t.Run(method, func(t *testing.T) {
			provider := &migrationRoutingBackend{alias: "primary"}
			router, err := db.NewRouter(db.RouterConfig{
				Default:   "primary",
				Databases: []db.RoutedDatabase{{Alias: "primary", Backend: provider}},
			})
			if err != nil {
				t.Fatal(err)
			}
			marshalCalls := 0
			migration := migrationRoutingDefinition(provider)
			migration.Operations[1].Args = []any{migrationRoutingJSON{calls: &marshalCalls}}
			executor := Executor{
				Backend: router.RoutingBackend(), Editor: &migrationRoutingEditor{backend: provider},
				Migrations: []Migration{migration},
				AfterMigrate: func(context.Context, Migration) error {
					provider.events = append(provider.events, "after_migrate")
					return nil
				},
			}
			provider.events = nil // NewRouter legitimately checks the configured alias.
			switch method {
			case "apply":
				err = executor.Apply(context.Background(), migration.Key())
			case "reverse":
				err = executor.Reverse(context.Background(), "routing.zero")
			case "sql", "sql_reverse":
				var statements []Statement
				statements, err = executor.SQL(context.Background(), migration.Key(), method == "sql_reverse")
				if len(statements) != 0 {
					t.Fatal("refused route returned executable statements")
				}
			case "history":
				var history []Applied
				history, err = executor.History(context.Background())
				if len(history) != 0 {
					t.Fatal("refused route returned migration history")
				}
			}
			if !errors.Is(err, db.ErrRouting) {
				t.Fatalf("expected routing refusal, got %v", err)
			}
			if marshalCalls != 0 || len(provider.events) != 0 {
				t.Fatalf("refused route invoked callbacks: marshal=%d events=%v", marshalCalls, provider.events)
			}
		})
	}
}

// Legacy Atomic accepts an empty alias. Before the explicit History guard,
// ExecutorFor could match that ambient transaction to the facade's empty alias
// and bypass the facade's own Query refusal. The ordinary transaction remains
// valid; only the unsupported routed migration is refused.
func TestRoutingFacadeHistoryCannotBorrowAnAmbientTransaction(t *testing.T) {
	provider := &migrationRoutingBackend{}
	var router db.Router
	executor := Executor{Backend: router.RoutingBackend()}
	err := db.Atomic(context.Background(), provider, db.AtomicOptions{}, func(ctx context.Context) error {
		provider.events = nil
		history, err := executor.History(ctx)
		if !errors.Is(err, db.ErrRouting) || len(history) != 0 {
			t.Fatalf("unbound history used an ambient executor: rows=%d err=%v", len(history), err)
		}
		if len(provider.events) != 0 {
			t.Fatalf("unbound history touched the transaction: %v", provider.events)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRoutingGuardPreservesOrdinaryBackendLifecycle(t *testing.T) {
	provider := &migrationRoutingBackend{alias: "ordinary"}
	migration := migrationRoutingDefinition(provider)
	executor := Executor{
		Backend: provider, Editor: &migrationRoutingEditor{backend: provider}, Migrations: []Migration{migration},
		AfterMigrate: func(context.Context, Migration) error {
			provider.events = append(provider.events, "after_migrate")
			return nil
		},
	}
	if err := executor.Apply(context.Background(), migration.Key()); err != nil {
		t.Fatal(err)
	}
	checksum, err := migration.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	history, err := executor.History(context.Background())
	if err != nil || !reflect.DeepEqual(history, []Applied{{Key: migration.Key(), Checksum: checksum}}) {
		t.Fatalf("ordinary history changed: %v %v", history, err)
	}
	for _, event := range []string{"editor.transition", "lock", "ledger.initialize", "ledger.query", "begin", "editor.create", "operation.forward", "ledger.insert", "commit", "after_migrate", "unlock"} {
		if !migrationRoutingHasEvent(provider.events, event) {
			t.Fatalf("ordinary apply did not reach %s: %v", event, provider.events)
		}
	}
	if err := executor.Reverse(context.Background(), "routing.zero"); err != nil {
		t.Fatal(err)
	}
	history, err = executor.History(context.Background())
	if err != nil || len(history) != 0 {
		t.Fatalf("ordinary reversal did not remove its history: %v %v", history, err)
	}
	for _, event := range []string{"operation.backward", "editor.delete", "ledger.delete"} {
		if !migrationRoutingHasEvent(provider.events, event) {
			t.Fatalf("ordinary reversal did not reach %s: %v", event, provider.events)
		}
	}
}

func TestMigrationRoutingGuardPreservesBackendFreeSQLPreview(t *testing.T) {
	provider := &migrationRoutingBackend{}
	migration := migrationRoutingDefinition(provider)
	// A concrete backend has never been required for editor-only SQL previews.
	executor := Executor{Editor: &migrationRoutingEditor{backend: provider}, Migrations: []Migration{migration}}
	for _, reverse := range []bool{false, true} {
		provider.events = nil
		statements, err := executor.SQL(context.Background(), migration.Key(), reverse)
		if err != nil || len(statements) != 3 {
			t.Fatalf("ordinary preview changed: statements=%d err=%v", len(statements), err)
		}
		for _, event := range provider.events {
			if event != "editor.transition" && event != "editor.create" && event != "editor.delete" {
				t.Fatalf("preview invoked storage or a data callback: %v", provider.events)
			}
		}
		if !migrationRoutingHasEvent(provider.events, "editor.transition") {
			t.Fatal("preview skipped the ordinary historical editor")
		}
	}
}

func migrationRoutingDefinition(provider *migrationRoutingBackend) Migration {
	schema := models.Schema{AppLabel: "routing", Name: "Item", Fields: []models.Field{models.BigAutoField("id")}}
	return Migration{App: "routing", Name: "0001", Operations: []Operation{
		CreateModel(schema),
		RunSQL("SELECT $1", []any{1}, "SELECT $1", []any{2}),
		RunData("routing_roundtrip", func(context.Context, db.Executor) error {
			provider.events = append(provider.events, "operation.forward")
			return nil
		}, func(context.Context, db.Executor) error {
			provider.events = append(provider.events, "operation.backward")
			return nil
		}),
	}}
}

func migrationRoutingHasEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

type migrationRoutingJSON struct{ calls *int }

func (v migrationRoutingJSON) MarshalJSON() ([]byte, error) {
	*v.calls += 1
	return []byte("0"), nil
}

type migrationRoutingBackend struct {
	db.Backend
	alias   string
	events  []string
	history []Applied
}

func (b *migrationRoutingBackend) Alias() string {
	b.events = append(b.events, "alias")
	return b.alias
}
func (b *migrationRoutingBackend) Dialect() db.Dialect { return migrationRoutingDialect{} }
func (b *migrationRoutingBackend) LockMigrations(context.Context) (func() error, error) {
	b.events = append(b.events, "lock")
	return func() error { b.events = append(b.events, "unlock"); return nil }, nil
}
func (b *migrationRoutingBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.events = append(b.events, "begin")
	return &migrationRoutingTransaction{backend: b}, nil
}
func (b *migrationRoutingBackend) Exec(_ context.Context, query string, args ...any) (db.Result, error) {
	switch {
	case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS gogo_migrations"):
		b.events = append(b.events, "ledger.initialize")
	case strings.HasPrefix(query, "INSERT INTO gogo_migrations"):
		b.events = append(b.events, "ledger.insert")
		b.history = append(b.history, Applied{Key: args[0].(string) + "." + args[1].(string), Checksum: args[2].(string)})
	case strings.HasPrefix(query, "DELETE FROM gogo_migrations"):
		b.events = append(b.events, "ledger.delete")
		b.history = nil
	default:
		b.events = append(b.events, "statement")
	}
	return recordedResult{}, nil
}
func (b *migrationRoutingBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	b.events = append(b.events, "ledger.query")
	return &migrationRoutingRows{history: append([]Applied(nil), b.history...)}, nil
}

type migrationRoutingTransaction struct{ backend *migrationRoutingBackend }

func (tx *migrationRoutingTransaction) Exec(ctx context.Context, query string, args ...any) (db.Result, error) {
	return tx.backend.Exec(ctx, query, args...)
}
func (tx *migrationRoutingTransaction) Query(ctx context.Context, query string, args ...any) (db.Rows, error) {
	return tx.backend.Query(ctx, query, args...)
}
func (tx *migrationRoutingTransaction) Commit() error {
	tx.backend.events = append(tx.backend.events, "commit")
	return nil
}
func (tx *migrationRoutingTransaction) Rollback() error {
	tx.backend.events = append(tx.backend.events, "rollback")
	return nil
}

type migrationRoutingRows struct {
	history []Applied
	next    int
}

func (*migrationRoutingRows) Columns() ([]string, error) {
	return []string{"app", "name", "checksum"}, nil
}
func (r *migrationRoutingRows) Next() bool {
	if r.next == len(r.history) {
		return false
	}
	r.next++
	return true
}
func (r *migrationRoutingRows) Scan(dest ...any) error {
	row := r.history[r.next-1]
	app, name, _ := strings.Cut(row.Key, ".")
	for i, value := range []string{app, name, row.Checksum} {
		*dest[i].(*string) = value
	}
	return nil
}
func (*migrationRoutingRows) Err() error   { return nil }
func (*migrationRoutingRows) Close() error { return nil }

type migrationRoutingEditor struct {
	db.SchemaEditor
	backend *migrationRoutingBackend
}

func (e *migrationRoutingEditor) WithSchemaTransition([]models.Schema, []models.Schema) (db.SchemaEditor, error) {
	e.backend.events = append(e.backend.events, "editor.transition")
	return e, nil
}
func (e *migrationRoutingEditor) CreateModel(ctx context.Context, executor db.Executor, _ models.Schema) error {
	e.backend.events = append(e.backend.events, "editor.create")
	_, err := executor.Exec(ctx, "CREATE TABLE routing_item (id bigint)")
	return err
}
func (e *migrationRoutingEditor) DeleteModel(ctx context.Context, executor db.Executor, _ models.Schema) error {
	e.backend.events = append(e.backend.events, "editor.delete")
	_, err := executor.Exec(ctx, "DROP TABLE routing_item")
	return err
}

type migrationRoutingDialect struct{}

func (migrationRoutingDialect) Name() string { return "routing-test" }
func (migrationRoutingDialect) QuoteIdentifier(name string) (string, error) {
	return `"` + name + `"`, nil
}
func (migrationRoutingDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (migrationRoutingDialect) FieldType(models.Field) (string, error) {
	return "bigint", nil
}
