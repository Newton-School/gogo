package postgres_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/management"
	"github.com/Newton-School/gogo/internal/codegen"
)

func TestPostgresInspectDBGeneratedModelsCompileAndQuery(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE customer (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name varchar(30) NOT NULL CONSTRAINT customer_name_unique UNIQUE)`,
		`CREATE TABLE invoice (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, customer_id bigint NOT NULL CONSTRAINT invoice_customer_fk REFERENCES customer(id) ON DELETE CASCADE, amount numeric(10,2) NOT NULL, note text, created timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, payload jsonb NOT NULL DEFAULT '{}', computed bigint GENERATED ALWAYS AS(id*2) STORED)`,
		`CREATE INDEX invoice_customer_idx ON invoice(customer_id) WHERE amount > 0`,
		`INSERT INTO customer(name) VALUES ('Ada')`,
		`INSERT INTO invoice(customer_id,amount) VALUES(1,12.50)`,
		`CREATE TABLE catalog_key (region varchar(8) NOT NULL, code integer NOT NULL, value text NOT NULL, CONSTRAINT catalog_key_pk PRIMARY KEY(region,code))`,
		`INSERT INTO catalog_key VALUES('west',9,'old')`,
		`CREATE TABLE customer_profile (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, customer_name varchar(30) NOT NULL CONSTRAINT profile_name_unique UNIQUE, CONSTRAINT profile_customer_fk FOREIGN KEY(customer_name) REFERENCES customer(name))`,
		`INSERT INTO customer_profile(customer_name) VALUES('Ada')`,
		`CREATE VIEW invoice_summary AS SELECT id,amount FROM invoice`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var schema string
	if err := db.QueryRow(ctx, backend, "SELECT current_schema()", nil, &schema); err != nil {
		t.Fatal(err)
	}
	source, err := management.InspectDB(ctx, backend, postgres.Introspector{}, management.InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{IncludeViews: true}, PrimaryKeys: map[string][]string{"invoice_summary": {"id"}}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps", "legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	module := fmt.Sprintf("module example.com/inspection\n\ngo 1.26.0\n\nrequire (\ngithub.com/Newton-School/gogo v0.0.0\ngithub.com/Newton-School/gogo/connectors/postgres v0.0.0\n)\nreplace github.com/Newton-School/gogo => %q\nreplace github.com/Newton-School/gogo/connectors/postgres => %q\n", root, filepath.Join(root, "connectors", "postgres"))
	for path, content := range map[string][]byte{"go.mod": []byte(module), "apps/legacy/models.go": source, "apps/legacy/models_test.go": []byte(inspectedClientTest)} {
		if err := os.WriteFile(filepath.Join(dir, path), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := codegen.Generate(dir); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "apps", "legacy", "zz_gogo.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "InvoiceFields") || !strings.Contains(string(generated), "CustomerID") {
		t.Fatal("typed references were not generated")
	}
	command := exec.CommandContext(ctx, "go", "test", "-race", "./apps/legacy", "-count=1")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=mod -p=1", "GOGO_INSPECTION_SCHEMA="+schema)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated client failed: %v\n%s\n%s", err, output, source)
	}
}

const inspectedClientTest = `package legacy
import (
 "context"
 "encoding/json"
 "os"
 "testing"
 "github.com/Newton-School/gogo/connectors/postgres"
 "github.com/Newton-School/gogo/core/app"
 "github.com/Newton-School/gogo/core/models"
 "github.com/Newton-School/gogo/core/orm"
)
func TestGeneratedClient(t *testing.T) {
 ctx:=context.Background()
 backend,err:=postgres.Open(ctx,postgres.Config{DSN:os.Getenv("GOGO_TEST_POSTGRES_DSN"),SearchPath:os.Getenv("GOGO_INSPECTION_SCHEMA")});if err!=nil{t.Fatal(err)};defer backend.Close()
 registry:=&models.Registry{}
 for _,schema:=range []models.Schema{(&Customer{}).Schema(),(&Invoice{}).Schema(),(&CatalogKey{}).Schema(),(&CustomerProfile{}).Schema(),(&InvoiceSummary{}).Schema()}{if !schema.Unmanaged{t.Fatal("ownership adopted")};if err:=registry.Register(schema);err!=nil{t.Fatal(err)};if err:=backend.SchemaEditor().CreateModel(ctx,backend,schema);err!=nil{t.Fatal(err)}}
 if err:=registry.Freeze();err!=nil{t.Fatal(err)}
 if err:=registerModels(&app.Registry{});err!=nil{t.Fatal(err)}
 store:=orm.New(backend,registry)
 invoice,err:=orm.For(store,func()*Invoice{return &Invoice{}}).Filter(orm.Q("id",int64(1))).Get(ctx)
 if err!=nil{t.Fatal(err)}
 if invoice.ID!=1 || invoice.CustomerID!=1 || invoice.Amount!="12.50" || invoice.Note!=nil || invoice.Created.IsZero() || invoice.Computed==nil || *invoice.Computed!=2 || !json.Valid(invoice.Payload){t.Fatalf("incorrect typed hydration: %+v",invoice)}
 invoice.Amount="18.75"
 if err:=store.Save(ctx,invoice,orm.SaveOptions{UpdateFields:[]string{"amount"}});err!=nil{t.Fatal(err)}
 if invoice.Computed==nil || *invoice.Computed!=2{t.Fatal("generated column changed")}
 row,err:=orm.For(store,func()*Invoice{return &Invoice{}}).SelectRelated("customer_id").Filter(orm.Q("customer_id__name","Ada")).Get(ctx)
 if err!=nil || row.Amount!="18.75"{t.Fatal("FK traversal failed",row,err)}
 if _,err:=models.Bind(&Customer{});err!=nil{t.Fatal(err)}
 key,err:=orm.For(store,func()*CatalogKey{return &CatalogKey{}}).Filter(orm.Q("region","west"),orm.Q("code",9)).Get(ctx);if err!=nil{t.Fatal(err)}
 key.Value="new";if err:=store.Save(ctx,key,orm.SaveOptions{UpdateFields:[]string{"value"}});err!=nil{t.Fatal(err)}
 if fields:=key.Schema().PKFields();len(fields)!=2 || fields[0].Name!="region" || fields[1].Name!="code"{t.Fatal("composite key order changed")}
 profile,err:=orm.For(store,func()*CustomerProfile{return &CustomerProfile{}}).SelectRelated("customer_name").Filter(orm.Q("customer_name__id",1)).Get(ctx);if err!=nil || profile.CustomerName!="Ada"{t.Fatal("non-PK one-to-one target failed",profile,err)}
 field,_:=profile.Schema().Field("customer_name");if field.Kind!=models.OneToOne || field.Relation.TargetFields[0]!="name"{t.Fatal("unique foreign key cardinality or target changed")}
 summary,err:=orm.For(store,func()*InvoiceSummary{return &InvoiceSummary{}}).Filter(orm.Q("id",1)).Get(ctx);if err!=nil || summary.Amount==nil || *summary.Amount!="18.75"{t.Fatal("view model query failed",summary,err)}
}
`

func TestPostgresInspectDBRejectsNativeMappingIssues(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	for _, statement := range []string{`CREATE TABLE raw_interval (id integer PRIMARY KEY, value interval)`, `CREATE TABLE raw_wall (id integer PRIMARY KEY, value timestamp)`, `CREATE TABLE raw_array (id integer PRIMARY KEY, value integer[])`, `CREATE TABLE raw_numeric (id integer PRIMARY KEY, value numeric)`} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"raw_interval", "raw_wall", "raw_array", "raw_numeric"} {
		source, err := management.InspectDB(ctx, backend, postgres.Introspector{}, management.InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{Relations: []string{table}}})
		if err == nil || source != nil || !strings.Contains(err.Error(), table) || !strings.Contains(err.Error(), "value") {
			t.Fatalf("unsupported native type generated source: %s %v", source, err)
		}
	}
}

func TestPostgresInspectDBRejectsUnrepresentableIndexAndConstraintOptions(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE raw_index_storage (id integer PRIMARY KEY,value integer)`,
		`CREATE INDEX raw_storage_idx ON raw_index_storage(value) WITH(fillfactor=80)`,
		`CREATE TABLE raw_noinherit (id integer PRIMARY KEY,value integer CONSTRAINT noinherit_check CHECK(value>0) NO INHERIT)`,
		`CREATE TABLE raw_reference (id integer PRIMARY KEY)`,
		`CREATE TABLE raw_match (id integer PRIMARY KEY,value integer CONSTRAINT match_fk REFERENCES raw_reference(id) MATCH FULL)`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, relations := range [][]string{{"raw_index_storage"}, {"raw_noinherit"}, {"raw_match", "raw_reference"}} {
		source, err := management.InspectDB(ctx, backend, postgres.Introspector{}, management.InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{Relations: relations}})
		if err == nil || source != nil || !strings.Contains(err.Error(), relations[0]) {
			t.Fatalf("native options silently omitted: %s %v", source, err)
		}
	}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Relations: []string{"raw_index_storage"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, index := range catalog.Relations[0].Indexes {
		if index.Name == "raw_storage_idx" {
			found = true
			if index.MappingIssue == "" || !strings.Contains(index.Definition, "fillfactor") {
				t.Fatal("raw unsupported metadata not preserved", index)
			}
		}
	}
	if !found {
		t.Fatal("native storage index absent")
	}
}

func TestPostgresInspectDBPrimaryKeyExcludesIncludedColumns(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	if _, err := backend.Exec(ctx, `CREATE TABLE primary_include (id integer NOT NULL, label text NOT NULL, CONSTRAINT primary_include_pk PRIMARY KEY(id) INCLUDE(label))`); err != nil {
		t.Fatal(err)
	}
	catalog, err := (postgres.Introspector{}).InspectCatalog(ctx, backend, db.CatalogOptions{Relations: []string{"primary_include"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Relations) != 1 || len(catalog.Relations[0].Columns) != 2 {
		t.Fatal("incomplete observed catalog", catalog)
	}
	columns := catalog.Relations[0].Columns
	if !columns[0].Field.PrimaryKey || columns[1].Field.PrimaryKey {
		t.Fatalf("included index column became a primary-key field: id=%t label=%t", columns[0].Field.PrimaryKey, columns[1].Field.PrimaryKey)
	}
	if source, err := management.RenderInspectedModels(catalog, management.InspectDBOptions{AppLabel: "legacy"}); err == nil || source != nil {
		t.Fatalf("primary-key INCLUDE metadata silently omitted: %s %v", source, err)
	}
}
