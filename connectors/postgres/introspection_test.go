package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type catalogReadBackend struct {
	db.Backend
	t                                   *testing.T
	begins, queries, commits, rollbacks int
	failQuery                           int
	failCloseQuery                      int
	beforeQuery                         func(int)
}

func (b *catalogReadBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	b.begins++
	if options.Isolation != sql.LevelRepeatableRead || !options.ReadOnly {
		b.t.Fatalf("catalog transaction is not a read-only consistent snapshot: %+v", options)
	}
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &catalogReadTx{Transaction: tx, backend: b}, nil
}

type catalogReadTx struct {
	db.Transaction
	backend *catalogReadBackend
}

func (tx *catalogReadTx) Exec(context.Context, string, ...any) (db.Result, error) {
	tx.backend.t.Fatal("inspection attempted Exec")
	return nil, errors.New("unexpected write")
}
func (tx *catalogReadTx) Query(ctx context.Context, query string, args ...any) (db.Rows, error) {
	tx.backend.queries++
	if tx.backend.beforeQuery != nil {
		tx.backend.beforeQuery(tx.backend.queries)
	}
	if !strings.HasPrefix(query, "SELECT ") {
		tx.backend.t.Fatalf("non-SELECT inspection: %s", query)
	}
	if tx.backend.queries == tx.backend.failQuery {
		return nil, errors.New("catalog read failed")
	}
	rows, err := tx.Transaction.Query(ctx, query, args...)
	if err == nil && tx.backend.queries == tx.backend.failCloseQuery {
		rows = catalogCloseRows{Rows: rows}
	}
	return rows, err
}

type catalogCloseRows struct{ db.Rows }

func (rows catalogCloseRows) Close() error {
	return errors.Join(rows.Rows.Close(), errors.New("catalog close failed"))
}
func (tx *catalogReadTx) Commit() error   { tx.backend.commits++; return tx.Transaction.Commit() }
func (tx *catalogReadTx) Rollback() error { tx.backend.rollbacks++; return tx.Transaction.Rollback() }

func TestPostgresCatalogPreservesMetadataReadOnly(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TYPE mood AS ENUM ('calm','busy')`,
		`CREATE DOMAIN positive_number AS integer CHECK (VALUE > 0)`,
		`CREATE TABLE catalog_parent (code varchar(12) CONSTRAINT parent_code_pk PRIMARY KEY, revision integer NOT NULL, CONSTRAINT parent_pair_unique UNIQUE(code,revision))`,
		`CREATE TABLE catalog_child (id bigint GENERATED ALWAYS AS IDENTITY CONSTRAINT child_pk PRIMARY KEY, parent_code varchar(12), parent_revision integer, title varchar(35) COLLATE "C" NOT NULL DEFAULT 'untitled', amount numeric(10,2), precise numeric, enabled boolean NOT NULL DEFAULT true, payload jsonb, ids integer[], bytes bytea, day date, wall timestamp, moment timestamptz, clock time, clock_zone timetz, address inet, elapsed interval, custom mood, domain_value positive_number, generated_length integer GENERATED ALWAYS AS (length(title)) STORED, CONSTRAINT child_parent_fk FOREIGN KEY(parent_code,parent_revision) REFERENCES catalog_parent(code,revision) ON DELETE SET NULL ON UPDATE CASCADE DEFERRABLE INITIALLY DEFERRED, CONSTRAINT title_check CHECK(length(title)>0))`,
		`CREATE INDEX child_visible_title ON catalog_child(title DESC NULLS LAST) INCLUDE (amount) WHERE enabled`,
		`CREATE INDEX child_expression ON catalog_child(lower(title))`,
		`CREATE INDEX child_pattern ON catalog_child(title varchar_pattern_ops)`,
		`CREATE UNIQUE INDEX child_nulls_unique ON catalog_child(parent_code) NULLS NOT DISTINCT`,
		`CREATE TABLE serial_sample (id serial PRIMARY KEY, short_id smallserial, big_id bigserial)`,
		`CREATE VIEW child_view AS SELECT id,title FROM catalog_child`,
		`CREATE MATERIALIZED VIEW child_materialized AS SELECT id FROM catalog_child WITH NO DATA`,
		`COMMENT ON TABLE catalog_child IS 'A catalog-only fixture'`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	guard := &catalogReadBackend{Backend: backend, t: t}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, guard, db.CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if guard.begins != 1 || guard.commits != 1 || guard.rollbacks != 1 {
		t.Fatalf("transaction lifecycle: %+v", guard)
	}
	if len(catalog.Relations) != 3 || catalog.Relations[0].Name != "catalog_child" || catalog.Relations[2].Name != "serial_sample" {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	child := catalog.Relations[0]
	if child.Kind != "table" || child.Comment != "A catalog-only fixture" {
		t.Fatalf("relation metadata: %+v", child)
	}
	columns := map[string]db.CatalogColumn{}
	for _, column := range child.Columns {
		columns[column.Name] = column
	}
	for name, kind := range map[string]models.Kind{"id": models.BigAuto, "parent_code": models.Char, "amount": models.Decimal, "enabled": models.Boolean, "payload": models.JSON, "ids": models.Array, "bytes": models.Binary, "day": models.Date, "wall": models.DateTime, "moment": models.DateTime, "clock": models.Time, "clock_zone": models.Custom, "address": models.GenericIPAddress, "elapsed": models.Duration, "custom": models.Custom, "domain_value": models.Custom} {
		if columns[name].Field.Kind != kind {
			t.Errorf("%s kind %q, want %q", name, columns[name].Field.Kind, kind)
		}
	}
	if columns["id"].Identity != "always" || !columns["id"].Field.PrimaryKey || columns["title"].Field.Null || columns["title"].Field.MaxLength != 35 || columns["title"].Collation != `pg_catalog."C"` || columns["title"].Field.DBDefault == "" {
		t.Fatal("column identity/nullability/length/collation/default lost")
	}
	if columns["amount"].Field.MaxDigits != 10 || columns["amount"].Field.DecimalPlaces != 2 || columns["precise"].Field.MaxDigits != 0 {
		t.Fatal("numeric precision lost")
	}
	if columns["ids"].Field.Element == nil || columns["ids"].Field.Element.Kind != models.Integer {
		t.Fatal("array element lost")
	}
	if columns["generated_length"].Generated != "s" || columns["generated_length"].Field.Kind != models.Generated || columns["generated_length"].Field.Element == nil || columns["generated_length"].Field.Element.Kind != models.Integer || columns["generated_length"].Field.GeneratedExpression == "" || columns["generated_length"].Field.Editable || columns["generated_length"].Field.DBDefault != "" {
		t.Fatal("generated-column metadata lost")
	}
	constraints := map[string]db.CatalogConstraint{}
	for _, constraint := range child.Constraints {
		constraints[constraint.Name] = constraint
	}
	fk := constraints["child_parent_fk"]
	if fk.Kind != "foreign_key" || !reflect.DeepEqual(fk.Columns, []string{"parent_code", "parent_revision"}) || !reflect.DeepEqual(fk.ReferencedColumns, []string{"code", "revision"}) || fk.ReferencedSchema != catalog.Schema || fk.ReferencedTable != "catalog_parent" || fk.OnDelete != "SET NULL" || fk.OnUpdate != "CASCADE" || !fk.Deferrable || !fk.InitiallyDeferred || !fk.Validated || fk.Definition == "" {
		t.Fatalf("foreign key metadata: %+v", fk)
	}
	if constraints["title_check"].Kind != "check" || constraints["title_check"].Expression == "" || constraints["child_pk"].Kind != "primary_key" {
		t.Fatal("constraint metadata lost")
	}
	indexes := map[string]db.CatalogIndex{}
	for _, index := range child.Indexes {
		indexes[index.Name] = index
	}
	index := indexes["child_visible_title"]
	if !reflect.DeepEqual(index.Columns, []string{"title"}) || !reflect.DeepEqual(index.Include, []string{"amount"}) || index.Condition != "enabled" || !reflect.DeepEqual(index.Options, []int{1}) || index.Method != "btree" || index.Constraint != "" || !index.Valid || !index.Ready || index.Definition == "" || !index.DefaultOpClasses[0] || index.Collations[0] != `pg_catalog."C"` {
		t.Fatalf("index metadata: %+v", index)
	}
	if indexes["child_expression"].Columns[0] != "" || indexes["child_expression"].Expressions[0] == "" || indexes["child_pattern"].DefaultOpClasses[0] || !strings.HasSuffix(indexes["child_pattern"].OpClasses[0], "varchar_pattern_ops") || indexes["child_nulls_unique"].NullsDistinct || !indexes["child_nulls_unique"].Unique || indexes["child_pk"].Constraint != "child_pk" {
		t.Fatal("expression/opclass/nulls/owned-index metadata lost")
	}
	for i, kind := range []models.Kind{models.Auto, models.SmallAuto, models.BigAuto} {
		column := catalog.Relations[2].Columns[i]
		if column.Field.Kind != kind || column.Identity != "serial" {
			t.Fatalf("serial metadata: %+v", column)
		}
	}
	withViews, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Schema: catalog.Schema, Relations: []string{"child_view", "child_materialized"}, IncludeViews: true})
	if err != nil || len(withViews.Relations) != 2 || withViews.Relations[0].Kind != "materialized_view" || withViews.Relations[1].Kind != "view" {
		t.Fatalf("views: %+v %v", withViews, err)
	}
}

func TestPostgresCatalogSnapshotAndExactNamespace(t *testing.T) {
	backend, other := openTest(t), openTest(t)
	ctx := context.Background()
	for _, statement := range []string{`CREATE TABLE snapshot_table (id integer PRIMARY KEY)`, `CREATE TABLE partition_parent (id integer PRIMARY KEY) PARTITION BY RANGE(id)`, `CREATE TABLE partition_child PARTITION OF partition_parent FOR VALUES FROM(0) TO(10)`, `CREATE VIEW snapshot_view AS SELECT id FROM snapshot_table`} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	guard := &catalogReadBackend{Backend: backend, t: t, beforeQuery: func(count int) {
		if count == 4 {
			if _, err := backend.Exec(ctx, `ALTER TABLE snapshot_table ADD COLUMN later text`); err != nil {
				t.Fatal(err)
			}
		}
	}}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, guard, db.CatalogOptions{Relations: []string{"snapshot_table"}})
	if err != nil || len(catalog.Relations) != 1 || len(catalog.Relations[0].Columns) != 1 {
		t.Fatalf("mixed catalog snapshots: %+v %v", catalog, err)
	}
	catalog, err = (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Relations: []string{"snapshot_table"}})
	if err != nil || len(catalog.Relations[0].Columns) != 2 {
		t.Fatalf("later committed catalog not visible: %+v %v", catalog, err)
	}
	partitions, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Relations: []string{"partition_parent", "partition_child"}})
	if err != nil || len(partitions.Relations) != 2 || partitions.Relations[0].Kind != "partition" || partitions.Relations[1].Kind != "partitioned_table" {
		t.Fatalf("partition metadata: %+v %v", partitions, err)
	}
	if _, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Relations: []string{"snapshot_view"}}); err == nil {
		t.Fatal("view selected without opt in")
	}
	if _, err := other.Exec(ctx, `CREATE TABLE "quote' table" ("strange column" integer PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	var otherSchema string
	if err := db.QueryRow(ctx, other, "SELECT current_schema()", nil, &otherSchema); err != nil {
		t.Fatal(err)
	}
	names := []string{"quote' table"}
	exact, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Schema: otherSchema, Relations: names})
	if err != nil || exact.Schema != otherSchema || len(exact.Relations) != 1 || exact.Relations[0].Name != names[0] || exact.Relations[0].Columns[0].Name != "strange column" || names[0] != "quote' table" {
		t.Fatalf("exact namespace/name inspection: %+v %v", exact, err)
	}
	var afterSchema string
	if err := db.QueryRow(ctx, backend, "SELECT current_schema()", nil, &afterSchema); err != nil || afterSchema != catalog.Schema {
		t.Fatalf("inspection changed search_path: %q %v", afterSchema, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	guard = &catalogReadBackend{Backend: backend, t: t, beforeQuery: func(count int) {
		if count == 4 {
			cancel()
		}
	}}
	partial, err := (postgres.Introspector{}).InspectCatalog(canceled, guard, db.CatalogOptions{})
	if err == nil || partial.Relations != nil || partial.Schema != "" || guard.commits != 0 {
		t.Fatalf("mid-inspection cancellation returned partial result: %+v %v", partial, err)
	}
}

func TestPostgresCatalogRejectsInvalidAndReturnsNoPartialResult(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	if _, err := backend.Exec(ctx, `CREATE TABLE catalog_good (id integer PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, options := range []db.CatalogOptions{{Relations: []string{"catalog_good", "catalog_good"}}, {Relations: []string{"bad\x00name"}}, {Schema: strings.Repeat("x", 64)}} {
		guard := &catalogReadBackend{Backend: backend, t: t}
		if catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, guard, options); err == nil || catalog.Schema != "" || catalog.Relations != nil || guard.begins != 0 {
			t.Fatalf("invalid options touched IO: %+v %v", catalog, err)
		}
	}
	for _, options := range []db.CatalogOptions{{Schema: "not_present"}, {Relations: []string{"catalog_good", "not_present"}}, {Relations: []string{"'; DROP TABLE catalog_good; --"}}} {
		if catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, options); err == nil || catalog.Schema != "" || catalog.Relations != nil {
			t.Fatalf("missing selection returned partial catalog: %+v %v", catalog, err)
		}
	}
	guard := &catalogReadBackend{Backend: backend, t: t, failQuery: 5}
	if catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, guard, db.CatalogOptions{}); err == nil || catalog.Relations != nil || guard.commits != 0 || guard.rollbacks != 1 {
		t.Fatalf("read failure returned partial catalog: %+v %v", catalog, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	guard = &catalogReadBackend{Backend: backend, t: t}
	if _, err := (postgres.Introspector{}).InspectCatalog(canceled, guard, db.CatalogOptions{}); !errors.Is(err, context.Canceled) || guard.begins != 0 {
		t.Fatalf("canceled inspection: %v", err)
	}
	if catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Relations: []string{"catalog_good"}}); err != nil || len(catalog.Relations) != 1 {
		t.Fatalf("selection string changed storage: %+v %v", catalog, err)
	}
}

func TestPostgresCatalogPreservesQuotedCollationAndRejectsOversizedText(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE COLLATION "collation.with.dot" (provider=libc,locale='C')`,
		`CREATE TABLE catalog_names (id integer PRIMARY KEY, title text COLLATE "collation.with.dot")`,
		`CREATE INDEX catalog_names_title ON catalog_names(title)`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	qualified := catalog.Schema + `."collation.with.dot"`
	if catalog.Relations[0].Columns[1].Collation != qualified {
		t.Fatalf("ambiguous collation identifier: %q", catalog.Relations[0].Columns[1].Collation)
	}
	for _, index := range catalog.Relations[0].Indexes {
		if index.Name == "catalog_names_title" && index.Collations[0] != qualified {
			t.Fatalf("ambiguous index collation: %+v", index)
		}
	}
	if _, err := backend.Exec(ctx, `DO $fixture$ BEGIN EXECUTE 'COMMENT ON TABLE catalog_names IS ' || quote_literal(repeat('x',65537)); END $fixture$`); err != nil {
		t.Fatal(err)
	}
	guard := &catalogReadBackend{Backend: backend, t: t}
	catalog, err = (postgres.Introspector{}).InspectCatalog(ctx, guard, db.CatalogOptions{})
	if err == nil || catalog.Schema != "" || catalog.Relations != nil || guard.commits != 0 || guard.rollbacks != 1 {
		t.Fatalf("oversized metadata was silently truncated: %+v %v", catalog, err)
	}
}

type catalogSessionBackend struct {
	db.Backend
	connection  db.Connection
	afterCommit func()
}

func (b *catalogSessionBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	var tx db.Transaction
	var err error
	if b.connection != nil {
		tx, err = b.connection.BeginTx(ctx, options)
	} else {
		tx, err = b.Backend.BeginTx(ctx, options)
	}
	if err != nil {
		return nil, err
	}
	return &catalogSessionTx{Transaction: tx, afterCommit: b.afterCommit}, nil
}

type catalogSessionTx struct {
	db.Transaction
	afterCommit func()
}

func (t *catalogSessionTx) Commit() error {
	err := t.Transaction.Commit()
	if t.afterCommit != nil {
		t.afterCommit()
	}
	return err
}

func TestPostgresCatalogExplicitNamespaceCannotBeShadowed(t *testing.T) {
	backend, target := openTest(t), openTest(t)
	ctx := context.Background()
	var schema, targetSchema string
	if err := db.QueryRow(ctx, backend, "SELECT pg_catalog.current_schema()", nil, &schema); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, target, "SELECT pg_catalog.current_schema()", nil, &targetSchema); err != nil {
		t.Fatal(err)
	}
	for _, b := range []db.Backend{backend, target} {
		if _, err := b.Exec(ctx, "CREATE TABLE witness (id integer PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`CREATE FUNCTION catalog_shadow_equal(pg_catalog.text,pg_catalog.text) RETURNS pg_catalog.bool LANGUAGE sql IMMUTABLE AS 'SELECT true'`,
		`CREATE OPERATOR = (FUNCTION = catalog_shadow_equal, LEFTARG = pg_catalog.text, RIGHTARG = pg_catalog.text)`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	connection, err := backend.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	quoted, err := backend.Dialect().QuoteIdentifier(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, "SET search_path = "+quoted+", pg_catalog"); err != nil {
		t.Fatal(err)
	}
	var shadowed bool
	if err := db.QueryRow(ctx, connection, "SELECT NULLIF($1,'') IS NULL", []any{targetSchema}, &shadowed); err != nil || !shadowed {
		t.Fatal("test did not activate the equality shadow", shadowed, err)
	}
	guard := &catalogSessionBackend{Backend: backend, connection: connection}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, guard, db.CatalogOptions{Schema: targetSchema, Relations: []string{"witness"}})
	if err != nil || catalog.Schema != targetSchema || len(catalog.Relations) != 1 {
		t.Fatalf("explicit namespace changed by caller search path: got %+v err %v want %s", catalog, err, targetSchema)
	}
}

func TestPostgresCatalogLateCommitCancellationReturnsNoResult(t *testing.T) {
	backend := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := backend.Exec(ctx, "CREATE TABLE witness (id integer PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	guard := &catalogSessionBackend{Backend: backend, afterCommit: cancel}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, guard, db.CatalogOptions{})
	if !errors.Is(err, context.Canceled) || catalog.Schema != "" || catalog.Relations != nil {
		t.Fatalf("read success escaped cancellation: %+v %v", catalog, err)
	}
}

func TestPostgresCatalogScalarCloseFailureReturnsNoResult(t *testing.T) {
	backend := openTest(t)
	for _, query := range []int{1, 2} {
		guard := &catalogReadBackend{Backend: backend, t: t, failCloseQuery: query}
		catalog, err := (postgres.Introspector{}).InspectCatalog(context.Background(), guard, db.CatalogOptions{})
		if err == nil || catalog.Schema != "" || catalog.Relations != nil || guard.commits != 0 || guard.rollbacks != 1 {
			t.Fatalf("query %d close failure escaped: %+v %v", query, catalog, err)
		}
	}
}
