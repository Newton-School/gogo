package integration_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type apiScopeSnapshotContext struct {
	context.Context
	afterScope func()
}

func (c *apiScopeSnapshotContext) Err() error {
	if callback := c.afterScope; callback != nil {
		c.afterScope = nil
		callback()
	}
	return c.Context.Err()
}

func TestPostgresAPIReadSnapshotsScopeBeforeLaterCallbacks(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE snapshot_author (id bigint PRIMARY KEY, tenant text NOT NULL, name text NOT NULL)`,
		`CREATE TABLE snapshot_book (id bigint PRIMARY KEY, tenant text NOT NULL, title text NOT NULL, author_id bigint REFERENCES snapshot_author(id))`,
		`INSERT INTO snapshot_author VALUES (1,'one','Visible author'),(2,'two','Hidden author')`,
		`INSERT INTO snapshot_book VALUES (1,'one','First visible',1),(2,'one','Second visible',2),(3,'two','Hidden book',2)`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	author := models.Schema{AppLabel: "snapshot", Name: "Author", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("tenant"), models.TextField("name"),
	}}
	book := models.Schema{AppLabel: "snapshot", Name: "Book", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("tenant"), models.TextField("title"),
		models.ForeignKeyField("author", models.Relation{Target: author.Key(), OnDelete: models.Cascade}, models.Nullable, models.WithColumn("author_id")),
	}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{author, book} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	probe := orm.For(orm.New(backend, registry), func() *models.MapRecord {
		record, _ := models.NewRecord(book)
		return record
	}).SelectRelated("author")
	if records, err := probe.All(ctx); err != nil || len(records) != 3 {
		t.Fatal("invalid eager-read fixture", len(records), err)
	}
	serializer, err := api.New(api.Definition{Fields: []api.Field{
		api.StringField("title"),
		api.ComputedField("author_name", func(_ context.Context, value api.ValueReader) (any, error) {
			row := value.(models.Record)
			related := row.State().Related["author"]
			if related == nil {
				return nil, nil
			}
			return related.(models.Record).Get("name")
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"baseline", "root", "related cache"} {
		t.Run(phase, func(t *testing.T) {
			requestContext := &apiScopeSnapshotContext{Context: context.Background()}
			allowed := []string{"one"}
			rootCalls, targetCalls, mutations := 0, 0, 0
			resource, err := api.NewResource(api.ResourceConfig{
				Store: orm.New(backend, registry), Model: book.Key(), Serializer: serializer,
				Policy:         auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil }),
				AllowAnonymous: true, IncludeCount: true, SelectRelated: []string{"author"},
				Scope: func(_ context.Context, _ auth.Principal, schema models.Schema) (db.Predicate, error) {
					if schema.Key() == book.Key() {
						rootCalls++
					} else {
						targetCalls++
					}
					if phase == "root" && schema.Key() == book.Key() || phase == "related cache" && schema.Key() == author.Key() {
						requestContext.afterScope = func() { mutations++; allowed[0] = "two" }
						return orm.Q("tenant__in", allowed), nil
					}
					return orm.Q("tenant", "one"), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			resource.ListHandler().ServeHTTP(out, httptest.NewRequest("GET", "/books/", nil).WithContext(requestContext))
			body := out.Body.String()
			wantMutations := 1
			if phase == "baseline" {
				wantMutations = 0
			}
			if out.Code != 200 || !strings.Contains(body, "First visible") || !strings.Contains(body, "Visible author") || strings.Contains(body, "Hidden") || !strings.Contains(body, `"count":2`) || rootCalls != 1 || targetCalls != 1 || mutations != wantMutations {
				t.Fatal("later callback retargeted captured API scope", phase, out.Code, body, rootCalls, targetCalls, mutations)
			}
		})
	}
}
