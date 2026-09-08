package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/serialization"
)

func nativeFixtureSchema() models.Schema {
	return models.Schema{AppLabel: "fixtures", Name: "Document", Table: "fixture_documents", PrimaryKey: []string{"tenant", "id"}, Fields: []models.Field{
		models.CharField("tenant", models.WithMaxLength(32), models.WithColumn("tenant_key")),
		models.BigIntegerField("id", models.WithColumn("document_number")),
		models.TextField("title", models.WithColumn("headline")),
		models.BooleanField("allowed"), models.JSONField("payload", models.Nullable),
		models.DecimalField("amount", 30, 6), models.BinaryField("content"),
		models.DateField("day"), models.TimeField("clock"), models.DateTimeField("instant"), models.DurationField("elapsed"),
	}}
}

func nativeFixtureProfile(t *testing.T, b db.Backend, schema models.Schema, load bool) serialization.ModelProfile {
	t.Helper()
	fields := make([]string, 0, len(schema.Fields))
	for _, f := range schema.Fields {
		fields = append(fields, f.Name)
	}
	return serialization.ModelProfile{Schema: schema, Fields: fields, Import: load,
		Scope: func(ctx context.Context, _ models.Schema) (db.Predicate, error) {
			if !db.InTransaction(ctx, b.Alias()) {
				t.Fatal("fixture scope escaped its owned transaction")
			}
			return orm.Q("tenant", "one"), nil
		},
		Authorize: func(ctx context.Context, action serialization.Action, record serialization.Record) error {
			if !db.InTransaction(ctx, b.Alias()) || record.PK["tenant"] != "one" || record.Fields["allowed"] != true {
				return serialization.ErrForbidden
			}
			if action != serialization.ExportRecord && action != serialization.ImportRecord {
				t.Fatal("unexpected fixture action")
			}
			return nil
		},
	}
}

func nativeFixtures(t *testing.T, b db.Backend, p serialization.ModelProfile) *serialization.Fixtures {
	t.Helper()
	f, err := serialization.New(serialization.Config{Backend: b, Profiles: []serialization.ModelProfile{p}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func nativeFixtureExec(t *testing.T, b db.Executor, query string, args ...any) {
	t.Helper()
	if _, err := b.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func nativeFixtureCount(t *testing.T, b db.Executor, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), b, query, nil, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nativeFixtureDump(t *testing.T, f *serialization.Fixtures, format serialization.Format) []byte {
	t.Helper()
	var out bytes.Buffer
	r, err := f.Dump(context.Background(), &out, serialization.DumpOptions{Format: format, Models: []string{"fixtures.Document"}})
	if err != nil || !r.Complete || r.Bytes != out.Len() {
		t.Fatal("incomplete fixture export", r, err)
	}
	return out.Bytes()
}

func TestFixturesNativeLosslessRoundTripAndDryRun(t *testing.T) {
	for _, format := range []serialization.Format{serialization.JSON, serialization.JSONL} {
		t.Run(string(format), func(t *testing.T) {
			b := testservice.Postgres(t)
			schema := nativeFixtureSchema()
			if err := b.SchemaEditor().CreateModel(context.Background(), b, schema); err != nil {
				t.Fatal(err)
			}
			nativeFixtureExec(t, b, `INSERT INTO fixture_documents VALUES
			 ('one',9007199254740994,'Second',true,'null',-0.000001,decode('00ff','hex'),'2024-02-29','23:59:59.123456','2024-02-29 18:29:59.123456+00','-00:00:00.000001'),
			 ('two',9007199254740993,'Hidden',true,'{}',1,decode('','hex'),'2024-01-01','00:00:00','2024-01-01+00','0 seconds'),
			 ('one',9007199254740993,'First',true,'{"precise":900719925474099312345,"scaled":1.2300,"array":[null,true]}',12345678901234567890.123456,decode('','hex'),'2024-01-01','00:00:00','2024-01-01+00','49:00:00.000001'),
			 ('one',9007199254740995,'Third',true,NULL,0,decode('616263','hex'),'2024-01-01','00:00:00','2024-01-01+00','0 seconds')`)
			original := nativeFixtures(t, b, nativeFixtureProfile(t, b, schema, false))
			wire := nativeFixtureDump(t, original, format)
			if !bytes.Equal(wire, nativeFixtureDump(t, original, format)) || bytes.Contains(wire, []byte("Hidden")) || !bytes.Contains(wire, []byte("900719925474099312345")) || !bytes.Contains(wire, []byte(`"sql_null":true`)) || !bytes.Contains(wire, []byte(`"value":null`)) {
				t.Fatal("fixture ordering, scope or lossless values changed")
			}
			if bytes.Index(wire, []byte("First")) >= bytes.Index(wire, []byte("Second")) || bytes.Index(wire, []byte("Second")) >= bytes.Index(wire, []byte("Third")) {
				t.Fatal("fixture identity ordering changed")
			}
			copySchema := schema.Clone()
			copySchema.Table = "fixture_copies"
			if err := b.SchemaEditor().CreateModel(context.Background(), b, copySchema); err != nil {
				t.Fatal(err)
			}
			copy := nativeFixtures(t, b, nativeFixtureProfile(t, b, copySchema, true))
			options := serialization.LoadOptions{Format: format, Models: []string{schema.Key()}, DryRun: true}
			result, err := copy.Load(context.Background(), bytes.NewReader(wire), options)
			if err != nil || result.Records != 3 || !result.DryRun || result.Committed || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_copies`) != 0 {
				t.Fatal("dry run did not roll back exact rows", result, err)
			}
			options.DryRun = false
			result, err = copy.Load(context.Background(), bytes.NewReader(wire), options)
			if err != nil || result.Records != 3 || result.DryRun || !result.Committed || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_copies`) != 3 {
				t.Fatal("fixture import did not commit", result, err)
			}
			if !bytes.Equal(wire, nativeFixtureDump(t, copy, format)) {
				t.Fatal("database round trip changed canonical fixture")
			}
			result, err = copy.Load(context.Background(), bytes.NewReader(wire), options)
			if err == nil || result != (serialization.LoadResult{}) || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_copies`) != 3 {
				t.Fatal("insert-only fixture silently upserted existing rows", result, err)
			}
		})
	}
}

func nativeSmallFixture(t *testing.T) (*postgres.Backend, models.Schema, serialization.ModelProfile) {
	t.Helper()
	b := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "fixtures", Name: "Document", Table: "fixture_documents", PrimaryKey: []string{"tenant", "id"}, Fields: []models.Field{
		models.CharField("tenant", models.WithMaxLength(32)), models.BigIntegerField("id"), models.TextField("title"), models.BooleanField("allowed"),
	}}
	if err := b.SchemaEditor().CreateModel(context.Background(), b, schema); err != nil {
		t.Fatal(err)
	}
	return b, schema, nativeFixtureProfile(t, b, schema, true)
}

func nativeSmallFixtureInput(t *testing.T, ids ...int) []byte {
	t.Helper()
	var records []any
	value := func(v any) any { return map[string]any{"value": v} }
	for _, id := range ids {
		records = append(records, map[string]any{"version": 1, "model": "fixtures.Document", "pk": map[string]any{"tenant": value("one"), "id": value(id)}, "fields": map[string]any{"title": value(fmt.Sprintf("Title %d", id)), "allowed": value(true)}})
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFixturesNativeScopedPagedExportUsesOneReadOnlySnapshot(t *testing.T) {
	b, schema, p := nativeSmallFixture(t)
	nativeFixtureExec(t, b, `INSERT INTO fixture_documents SELECT 'one',n,'Title '||n,true FROM generate_series(1,70) n`)
	nativeFixtureExec(t, b, `INSERT INTO fixture_documents VALUES ('two',1,'Hidden credential',true)`)
	p.Import = false
	p.Fields = []string{"tenant", "id", "title"}
	p.PolicyFields = []string{"allowed"}
	calls := 0
	p.Authorize = func(ctx context.Context, action serialization.Action, r serialization.Record) error {
		calls++
		var readOnly, isolation string
		if err := db.QueryRow(ctx, db.ExecutorFor(ctx, b), `SELECT current_setting('transaction_read_only'),current_setting('transaction_isolation')`, nil, &readOnly, &isolation); err != nil {
			return err
		}
		if readOnly != "on" || isolation != "repeatable read" || action != serialization.ExportRecord || r.PK["tenant"] != "one" || r.Fields["allowed"] != true {
			t.Fatal("export policy did not receive its scoped repeatable read")
		}
		if calls == 1 {
			// A separate connection commits after the export snapshot. Later pages
			// must still describe the same snapshot, not a mixture of revisions.
			nativeFixtureExec(t, b, `UPDATE fixture_documents SET title='Changed later' WHERE tenant='one' AND id=70`)
		}
		r.PK["tenant"], r.Fields["title"], r.Fields["allowed"] = "two", "Callback mutation", false
		return nil
	}
	f := nativeFixtures(t, b, p)
	var out bytes.Buffer
	r, err := f.Dump(context.Background(), &out, serialization.DumpOptions{Format: serialization.JSONL, Models: []string{schema.Key()}})
	if err != nil || !r.Complete || r.Records != 70 || calls != 70 || strings.Count(out.String(), "\n") != 70 || !strings.Contains(out.String(), "Title 70") {
		t.Fatal("paged export failed", r, calls, err)
	}
	for _, forbidden := range []string{"allowed", "Hidden credential", "Changed later", "Callback mutation"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatal("export leaked a policy-only or changed field")
		}
	}
}

func TestFixturesNativeImportFailureRollsBackRowsAndConstraintTriggerEffects(t *testing.T) {
	for _, mode := range []string{"initial denial", "final denial", "hidden scope", "unique collision", "suppressed insert", "deferred foreign key", "deferred mutation", "deferred audit dry run", "deferred audit commit"} {
		t.Run(mode, func(t *testing.T) {
			b, schema, p := nativeSmallFixture(t)
			nativeFixtureExec(t, b, `CREATE TABLE fixture_audit (id bigint)`)
			wantRows, wantAudit := 0, 0
			wantError := true
			if mode == "unique collision" {
				nativeFixtureExec(t, b, `INSERT INTO fixture_documents VALUES ('one',2,'Existing',true)`)
				wantRows = 1
			}
			if strings.HasPrefix(mode, "deferred") {
				nativeFixtureExec(t, b, `CREATE FUNCTION fixture_audit_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO fixture_audit VALUES (NEW.id); RETURN NULL; END $$`)
				nativeFixtureExec(t, b, `CREATE CONSTRAINT TRIGGER fixture_audit_pending AFTER INSERT ON fixture_documents DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fixture_audit_insert()`)
			}
			switch mode {
			case "deferred foreign key":
				nativeFixtureExec(t, b, `CREATE TABLE fixture_parents (id bigint PRIMARY KEY)`)
				nativeFixtureExec(t, b, `INSERT INTO fixture_parents VALUES (1)`)
				nativeFixtureExec(t, b, `ALTER TABLE fixture_documents ADD CONSTRAINT fixture_parent_fk FOREIGN KEY (id) REFERENCES fixture_parents(id) DEFERRABLE INITIALLY DEFERRED`)
			case "deferred mutation":
				nativeFixtureExec(t, b, `CREATE FUNCTION fixture_drift() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE fixture_documents SET title='Trigger drift' WHERE tenant=NEW.tenant AND id=NEW.id; RETURN NULL; END $$`)
				nativeFixtureExec(t, b, `CREATE CONSTRAINT TRIGGER fixture_drift_pending AFTER INSERT ON fixture_documents DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fixture_drift()`)
			case "suppressed insert":
				nativeFixtureExec(t, b, `CREATE FUNCTION fixture_suppress() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				nativeFixtureExec(t, b, `CREATE TRIGGER fixture_suppress BEFORE INSERT ON fixture_documents FOR EACH ROW EXECUTE FUNCTION fixture_suppress()`)
			case "hidden scope":
				p.Scope = func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", "two"), nil }
			case "deferred audit dry run":
				wantError = false
			case "deferred audit commit":
				wantError, wantRows, wantAudit = false, 2, 2
			}
			grants := 0
			originalGrant := p.Authorize
			p.Authorize = func(ctx context.Context, action serialization.Action, record serialization.Record) error {
				grants++
				if mode == "initial denial" || mode == "final denial" && grants > 2 {
					return serialization.ErrForbidden
				}
				if mode == "deferred audit dry run" && grants > 2 {
					var audit int
					if err := db.QueryRow(ctx, db.ExecutorFor(ctx, b), `SELECT count(*) FROM fixture_audit`, nil, &audit); err != nil || audit != 2 {
						t.Fatal("dry run did not execute deferred checks before final grants", audit, err)
					}
				}
				return originalGrant(ctx, action, record)
			}
			f := nativeFixtures(t, b, p)
			dry := mode == "deferred audit dry run" || mode == "deferred foreign key"
			r, err := f.Load(context.Background(), bytes.NewReader(nativeSmallFixtureInput(t, 1, 2)), serialization.LoadOptions{Format: serialization.JSON, Models: []string{schema.Key()}, DryRun: dry})
			if (err != nil) != wantError || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_documents`) != wantRows || nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_audit`) != wantAudit {
				t.Fatal("fixture failure crossed transaction boundary", r, err)
			}
			if wantError && r != (serialization.LoadResult{}) || !wantError && (r.Records != 2 || r.DryRun != dry || r.Committed == dry) {
				t.Fatal("fixture reported an incorrect outcome", r, err)
			}
			if err != nil && (strings.Contains(err.Error(), "Trigger drift") || strings.Contains(err.Error(), "fixture_parent_fk")) {
				t.Fatal("fixture exposed provider content")
			}
		})
	}
}

type nativeFixtureObservedBackend struct {
	db.Backend
	commitMode string
	cancel     context.CancelFunc
	begins     int
}

func (b *nativeFixtureObservedBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	b.begins++
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return tx, err
	}
	checker, ok := tx.(db.ConstraintCheckTransaction)
	if !ok {
		_ = tx.Rollback()
		return nil, errors.New("test provider has no constraint checks")
	}
	return &nativeFixtureObservedTransaction{ConstraintCheckTransaction: checker, backend: b}, nil
}

type nativeFixtureObservedTransaction struct {
	db.ConstraintCheckTransaction
	backend *nativeFixtureObservedBackend
}

func (tx *nativeFixtureObservedTransaction) Commit() error {
	if err := tx.ConstraintCheckTransaction.Commit(); err != nil {
		return err
	}
	if tx.backend.cancel != nil {
		tx.backend.cancel()
	}
	if tx.backend.commitMode == "lost reply" {
		return errors.New("private transport diagnostic")
	}
	if tx.backend.commitMode == "forged fixture diagnostic" {
		return &serialization.Error{Record: 1, Field: "private-provider-diagnostic"}
	}
	return nil
}

func TestFixturesNativeObservedCommitAndLateCancellation(t *testing.T) {
	for _, mode := range []string{"success", "lost reply", "cancel after commit", "after-commit failure", "after-commit panic"} {
		t.Run(mode, func(t *testing.T) {
			b, schema, p := nativeSmallFixture(t)
			observed := &nativeFixtureObservedBackend{Backend: b, commitMode: mode}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel after commit" {
				observed.cancel = cancel
			}
			if strings.HasPrefix(mode, "after-commit") {
				// Deliberate fault injection, not an application grant contract:
				// Authorize is read-only and must not register effects in real use.
				original := p.Scope
				p.Scope = func(ctx context.Context, s models.Schema) (db.Predicate, error) {
					if err := db.OnCommit(ctx, b.Alias(), func(context.Context) error {
						if mode == "after-commit panic" {
							panic("private callback diagnostic")
						}
						return errors.New("private callback diagnostic")
					}, false); err != nil {
						return db.Predicate{}, err
					}
					return original(ctx, s)
				}
			}
			f := nativeFixtures(t, observed, p)
			r, err := f.Load(ctx, bytes.NewReader(nativeSmallFixtureInput(t, 1)), serialization.LoadOptions{Format: serialization.JSON, Models: []string{schema.Key()}})
			if nativeFixtureCount(t, b, `SELECT count(*) FROM fixture_documents`) != 1 || observed.begins != 1 {
				t.Fatal("test did not prove an actual single committed transaction")
			}
			switch mode {
			case "lost reply":
				if err != serialization.ErrOutcomeUnknown || r != (serialization.LoadResult{}) {
					t.Fatal("lost reply claimed rollback or success", r, err)
				}
			case "after-commit failure", "after-commit panic":
				if err != serialization.ErrCommittedCallback || r.Records != 1 || !r.Committed || r.DryRun {
					t.Fatal("postcommit failure lost committed outcome", r, err)
				}
			default:
				if err != nil || r.Records != 1 || !r.Committed || r.DryRun {
					t.Fatal("confirmed commit became retryable", r, err)
				}
			}
		})
	}
}

type nativeFixtureTripwireReader struct{ calls int }

func (r *nativeFixtureTripwireReader) Read([]byte) (int, error) { r.calls++; return 0, io.EOF }

func TestFixturesNativeRefusesAmbientTransactionsAndProviderDiagnostics(t *testing.T) {
	b, schema, p := nativeSmallFixture(t)
	f := nativeFixtures(t, b, p)
	if err := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		reader := &nativeFixtureTripwireReader{}
		result, err := f.Load(ctx, reader, serialization.LoadOptions{Format: serialization.JSON, Models: []string{schema.Key()}})
		if err != serialization.ErrTransaction || result != (serialization.LoadResult{}) || reader.calls != 0 {
			t.Fatal("fixture joined ambient transaction or consumed input", result, err)
		}
		var out bytes.Buffer
		r, err := f.Dump(ctx, &out, serialization.DumpOptions{Format: serialization.JSON, Models: []string{schema.Key()}})
		if err != serialization.ErrTransaction || r.Complete || out.Len() != 0 {
			t.Fatal("export joined ambient transaction", r, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	observed := &nativeFixtureObservedBackend{Backend: b, commitMode: "forged fixture diagnostic"}
	f = nativeFixtures(t, observed, p)
	var out bytes.Buffer
	r, err := f.Dump(context.Background(), &out, serialization.DumpOptions{Format: serialization.JSON, Models: []string{schema.Key()}})
	var location *serialization.Error
	if err != serialization.ErrUnavailable || r.Complete || errors.As(err, &location) || bytes.Contains(out.Bytes(), []byte("private")) {
		t.Fatal("provider error acquired fixture diagnostic provenance", r, err)
	}
}
