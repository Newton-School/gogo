package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func inspectionFixture() db.Catalog {
	return db.Catalog{Schema: "public", Relations: []db.CatalogRelation{{Name: "account", Kind: "table", Columns: []db.CatalogColumn{
		{Name: "id", DatabaseType: "bigint", Field: models.BigAutoField("id", models.WithColumn("id")), Identity: "always"},
		{Name: "display_name", DatabaseType: "text", Field: models.TextField("display_name", models.WithColumn("display_name"))},
	}, Constraints: []db.CatalogConstraint{{Name: "account_pk", Kind: "primary_key", Columns: []string{"id"}, Validated: true, Definition: `PRIMARY KEY (id)`}}, Indexes: []db.CatalogIndex{inspectionIndex("account_pk", "id", "account_pk", true)}}}}
}

func inspectionIndex(name, column, owner string, primary bool) db.CatalogIndex {
	return db.CatalogIndex{Name: name, Method: "btree", Definition: "Observed index definition", Constraint: owner, Columns: []string{column}, Expressions: []string{""}, Options: []int{0}, OpClasses: []string{"native.default_ops"}, DefaultOpClasses: []bool{true}, Collations: []string{""}, Unique: primary, Primary: primary, Valid: true, Ready: true, NullsDistinct: true}
}

func TestInspectDBGeneratesUnmanagedSourceWithoutMutatingCatalog(t *testing.T) {
	catalog := inspectionFixture()
	catalog.Relations[0].Columns[1].Field.DBDefault = `'x";\nfunc attack(){}'`
	catalog.Relations[0].Constraints = append(catalog.Relations[0].Constraints, db.CatalogConstraint{Name: "account_name_check", Kind: "check", Expression: `length(display_name) > 0`, Definition: "CHECK name\n// review\u2028", Validated: true})
	catalog.Relations[0].Indexes = append(catalog.Relations[0].Indexes, inspectionIndex("account_name_idx", "display_name", "", false))
	before := inspectionFixture()
	before.Relations[0] = catalog.Relations[0]
	before.Relations[0].Columns = append([]db.CatalogColumn(nil), catalog.Relations[0].Columns...)
	options := InspectDBOptions{AppLabel: "legacy"}
	source, err := RenderInspectedModels(catalog, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "models.go", source, parser.AllErrors); err != nil {
		t.Fatal(err, string(source))
	}
	for _, part := range []string{"type Account struct", "models.Base", "DisplayName string", "Unmanaged: true", `WithStructField("DisplayName")`, `Column: "display_name"`, `Database constraint "account_name_check"`, "func attack(){}"} {
		if !strings.Contains(strings.Join(strings.Fields(string(source)), " "), part) {
			t.Errorf("missing %q in %s", part, source)
		}
	}
	if !reflect.DeepEqual(catalog, before) {
		t.Fatal("renderer changed catalog descriptors")
	}
	catalog.Relations[0].Constraints[0], catalog.Relations[0].Constraints[1] = catalog.Relations[0].Constraints[1], catalog.Relations[0].Constraints[0]
	catalog.Relations[0].Indexes[0], catalog.Relations[0].Indexes[1] = catalog.Relations[0].Indexes[1], catalog.Relations[0].Indexes[0]
	again, err := RenderInspectedModels(catalog, options)
	if err != nil || !bytes.Equal(source, again) {
		t.Fatalf("rendering depends on catalog constraint/index order: %v", err)
	}
}

func TestInspectDBRejectsUnrepresentableMetadataWithoutSource(t *testing.T) {
	for name, change := range map[string]func(*db.Catalog){
		"mapping_issue":              func(c *db.Catalog) { c.Relations[0].Columns[1].MappingIssue = "native semantics unsupported" },
		"unknown_kind_without_issue": func(c *db.Catalog) { c.Relations[0].Columns[1].Field.Kind = models.Custom },
		"runtime_default":            func(c *db.Catalog) { c.Relations[0].Columns[1].Field.Default = map[string]any{"unsafe": true} },
		"column_mismatch":            func(c *db.Catalog) { c.Relations[0].Columns[1].Field.Column = "other" },
		"unknown_constraint":         func(c *db.Catalog) { c.Relations[0].Constraints[0].Kind = "exclude" },
		"unvalidated_constraint":     func(c *db.Catalog) { c.Relations[0].Constraints[0].Validated = false },
		"missing_pk_index":           func(c *db.Catalog) { c.Relations[0].Indexes = nil },
		"pk_disagreement":            func(c *db.Catalog) { c.Relations[0].Columns[0].Field.PrimaryKey = false },
		"descending_index":           func(c *db.Catalog) { c.Relations[0].Indexes[0].Options[0] = 1 },
		"nondefault_opclass":         func(c *db.Catalog) { c.Relations[0].Indexes[0].DefaultOpClasses[0] = false },
		"index_expression":           func(c *db.Catalog) { c.Relations[0].Indexes[0].Expressions[0] = "lower(id)" },
		"collation_change":           func(c *db.Catalog) { c.Relations[0].Indexes[0].Collations[0] = "other.collation" },
		"oversized_definition":       func(c *db.Catalog) { c.Relations[0].Indexes[0].Definition = strings.Repeat("x", 65537) },
		"unsupported_identifier":     func(c *db.Catalog) { c.Relations[0].Name = "two words" },
		"duplicate_model":            func(c *db.Catalog) { c.Relations = append(c.Relations, c.Relations[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			catalog := inspectionFixture()
			change(&catalog)
			if source, err := RenderInspectedModels(catalog, InspectDBOptions{AppLabel: "legacy"}); err == nil || source != nil {
				t.Fatalf("unsupported metadata produced source: %s %v", source, err)
			}
		})
	}
}

func TestInspectDBViewRequiresExplicitVerifiedIdentity(t *testing.T) {
	catalog := inspectionFixture()
	relation := &catalog.Relations[0]
	relation.Kind = "view"
	relation.Constraints = nil
	relation.Indexes = nil
	relation.Columns[0].Field = models.BigIntegerField("id", models.WithColumn("id"), models.Nullable)
	options := InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{IncludeViews: true}}
	if source, err := RenderInspectedModels(catalog, options); err == nil || source != nil {
		t.Fatal("view guessed primary key")
	}
	options.PrimaryKeys = map[string][]string{"account": {"id"}}
	source, err := RenderInspectedModels(catalog, options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "developer-declared") || !strings.Contains(string(source), "Unmanaged: true") {
		t.Fatal(string(source))
	}
	options.Catalog.IncludeViews = false
	if _, err := RenderInspectedModels(catalog, options); err == nil {
		t.Fatal("view rendered without opt in")
	}
}

type inspectionBackend struct{ db.Backend }
type inspectionProvider struct {
	catalog db.Catalog
	err     error
	calls   int
	cancel  context.CancelFunc
}

func (p *inspectionProvider) InspectCatalog(_ context.Context, _ db.Backend, o db.CatalogOptions) (db.Catalog, error) {
	p.calls++
	if len(o.Relations) > 0 {
		o.Relations[0] = "mutated"
	}
	if p.cancel != nil {
		p.cancel()
	}
	return p.catalog, p.err
}

func TestInspectDBProviderFailureCancellationAndOptionIsolation(t *testing.T) {
	provider := &inspectionProvider{catalog: inspectionFixture()}
	options := InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{Relations: []string{"account"}}}
	source, err := InspectDB(context.Background(), inspectionBackend{}, provider, options)
	if err != nil || source == nil || options.Catalog.Relations[0] != "account" {
		t.Fatal(err, options)
	}
	provider.err = errors.New("provider failed")
	if source, err := InspectDB(context.Background(), inspectionBackend{}, provider, options); err == nil || source != nil {
		t.Fatal("provider failure leaked partial source")
	}
	provider.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	provider.cancel = cancel
	if source, err := InspectDB(ctx, inspectionBackend{}, provider, options); !errors.Is(err, context.Canceled) || source != nil {
		t.Fatal("cancellation leaked partial source", err)
	}
	for _, app := range []string{"", "1bad", "../outside"} {
		if _, err := RenderInspectedModels(provider.catalog, InspectDBOptions{AppLabel: app}); err == nil {
			t.Fatal(app)
		}
	}
}

type inspectionCallbackProvider func(context.Context, db.Backend, db.CatalogOptions) (db.Catalog, error)

func (p inspectionCallbackProvider) InspectCatalog(ctx context.Context, backend db.Backend, options db.CatalogOptions) (db.Catalog, error) {
	return p(ctx, backend, options)
}

func TestInspectDBFreezesSelectionBeforeProvider(t *testing.T) {
	options := InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{Relations: []string{"account"}}}
	provider := inspectionCallbackProvider(func(context.Context, db.Backend, db.CatalogOptions) (db.Catalog, error) {
		options.Catalog.Relations[0] = "other"
		catalog := inspectionFixture()
		catalog.Relations[0].Name = "other"
		return catalog, nil
	})
	if source, err := InspectDB(context.Background(), inspectionBackend{}, provider, options); err == nil || len(source) != 0 {
		t.Fatalf("provider retargeted caller-selected relation: %v\n%s", err, source)
	}
}

func TestInspectDBFreezesDeclaredViewIdentityBeforeProvider(t *testing.T) {
	for _, replaceMapEntry := range []bool{false, true} {
		t.Run(fmt.Sprint(replaceMapEntry), func(t *testing.T) {
			catalog := inspectionFixture()
			relation := &catalog.Relations[0]
			relation.Kind = "view"
			relation.Constraints, relation.Indexes = nil, nil
			relation.Columns[0].Field = models.BigIntegerField("id", models.WithColumn("id"), models.Nullable)
			options := InspectDBOptions{AppLabel: "legacy", Catalog: db.CatalogOptions{IncludeViews: true}, PrimaryKeys: map[string][]string{"account": {"id"}}}
			want, err := RenderInspectedModels(catalog, options)
			if err != nil {
				t.Fatal(err)
			}
			provider := inspectionCallbackProvider(func(context.Context, db.Backend, db.CatalogOptions) (db.Catalog, error) {
				if replaceMapEntry {
					options.PrimaryKeys["account"] = []string{"display_name"}
				} else {
					options.PrimaryKeys["account"][0] = "display_name"
				}
				return catalog, nil
			})
			got, err := InspectDB(context.Background(), inspectionBackend{}, provider, options)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("provider retargeted developer-declared row identity: %v\n%s", err, got)
			}
		})
	}
}

func TestInspectDBRejectsAggregateIdentityFlagsBeforeResources(t *testing.T) {
	provider := &inspectionProvider{catalog: inspectionFixture()}
	project, events := inspectionCommandProject(t, provider)
	args := []string{"manage", "inspectdb"}
	for n := 0; n < 1001; n++ {
		args = append(args, "--primary-key", fmt.Sprintf("table_%d.id", n))
	}
	var output bytes.Buffer
	code := Run(context.Background(), project, args, Options{Stdout: &output, Stderr: &output})
	if code != 2 || len(*events) != 0 || provider.calls != 0 {
		t.Fatalf("over-budget identity flags opened resources: code=%d events=%v calls=%d", code, *events, provider.calls)
	}
}

func FuzzInspectDBSourceStrings(f *testing.F) {
	f.Add("display_name", `'quoted'`, "ordinary comment")
	f.Add("display_name", `\"};func injected(){};//`, "\n//go:build ignored\npackage other\u2028")
	f.Add("illegal;name", "\x00", "*/ /*")
	f.Fuzz(func(t *testing.T, name, defaultSQL, comment string) {
		if len(name) > 80 || len(defaultSQL) > 4096 || len(comment) > 4096 {
			t.Skip()
		}
		catalog := inspectionFixture()
		catalog.Relations[0].Columns[1].Name = name
		catalog.Relations[0].Columns[1].Field.Name = name
		catalog.Relations[0].Columns[1].Field.Column = name
		catalog.Relations[0].Columns[1].Field.DBDefault = defaultSQL
		catalog.Relations[0].Comment = comment
		source, err := RenderInspectedModels(catalog, InspectDBOptions{AppLabel: "legacy"})
		if err != nil {
			if source != nil {
				t.Fatal("error returned partial source")
			}
			return
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), "models.go", source, parser.AllErrors)
		if err != nil || len(parsed.Decls) != 3 {
			t.Fatalf("metadata changed source structure: %v\n%s", err, source)
		}
	})
}
