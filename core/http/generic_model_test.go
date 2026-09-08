package http

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type genericModelTestDialect struct{}

func (genericModelTestDialect) Name() string { return "generic_model_test" }
func (genericModelTestDialect) QuoteIdentifier(name string) (string, error) {
	if !models.ValidIdentifier(name) {
		return "", errors.New("invalid identifier")
	}
	return `"` + name + `"`, nil
}
func (genericModelTestDialect) Placeholder(index int) string { return fmt.Sprintf("$%d", index) }
func (genericModelTestDialect) FieldType(models.Field) (string, error) {
	return "BIGINT", nil
}

type genericModelTestBackend struct {
	db.Backend
	queries int
	query   func(string, []any)
	failure error
	rows    func() db.Rows
}

func (*genericModelTestBackend) Alias() string       { return "generic_model_test" }
func (*genericModelTestBackend) Dialect() db.Dialect { return genericModelTestDialect{} }
func (*genericModelTestBackend) Capabilities() db.Capabilities {
	return db.Capabilities{}
}
func (b *genericModelTestBackend) Query(_ context.Context, statement string, args ...any) (db.Rows, error) {
	b.queries++
	if b.query != nil {
		b.query(statement, args)
	}
	if b.rows != nil {
		return b.rows(), b.failure
	}
	return nil, b.failure
}

func genericModelTestSchema() models.Schema {
	return models.Schema{AppLabel: "library", Name: "Book", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("title"), models.IntegerField("tenant"), models.TextField("secret"),
	}}
}

func genericModelTestOptions(t *testing.T, schemas ...models.Schema) (ModelReadOptions, *genericModelTestBackend) {
	t.Helper()
	if len(schemas) == 0 {
		schemas = []models.Schema{genericModelTestSchema()}
	}
	registry := &models.Registry{}
	for _, schema := range schemas {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	backend := &genericModelTestBackend{failure: errors.New("query failure")}
	return ModelReadOptions{
		Store: orm.New(backend, registry), Model: schemas[0].Key(), Fields: []string{"title"},
		Policy: auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil }),
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
			return orm.Q("tenant", int64(7)), nil
		},
	}, backend
}

func TestGenericModelRequiredConfigurationAndAllowlists(t *testing.T) {
	cases := map[string]func(*ModelReadOptions){
		"store":             func(o *ModelReadOptions) { o.Store = nil },
		"backend":           func(o *ModelReadOptions) { o.Store.Backend = nil },
		"typed nil backend": func(o *ModelReadOptions) { o.Store.Backend = (*genericModelTestBackend)(nil) },
		"registry":          func(o *ModelReadOptions) { o.Store.Registry = nil },
		"model":             func(o *ModelReadOptions) { o.Model = "" },
		"unknown model":     func(o *ModelReadOptions) { o.Model = "library.Unknown" },
		"case exact":        func(o *ModelReadOptions) { o.Model = "Library.Book" },
		"policy":            func(o *ModelReadOptions) { o.Policy = nil },
		"typed nil policy":  func(o *ModelReadOptions) { o.Policy = auth.PolicyFunc(nil) },
		"scope":             func(o *ModelReadOptions) { o.Scope = nil },
		"fields":            func(o *ModelReadOptions) { o.Fields = nil },
		"policy not output": func(o *ModelReadOptions) { o.Fields = nil; o.PolicyFields = []string{"title"} },
		"unknown field":     func(o *ModelReadOptions) { o.Fields = []string{"unknown"} },
		"wildcard field":    func(o *ModelReadOptions) { o.Fields = []string{"*"} },
		"duplicate field":   func(o *ModelReadOptions) { o.Fields = []string{"title", "title"} },
		"unknown policy":    func(o *ModelReadOptions) { o.PolicyFields = []string{"unknown"} },
		"duplicate policy":  func(o *ModelReadOptions) { o.PolicyFields = []string{"tenant", "tenant"} },
		"relation path":     func(o *ModelReadOptions) { o.Fields = []string{"owner__name"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options, backend := genericModelTestOptions(t)
			mutate(&options)
			model, err := newGenericModel(options)
			if model != nil || err != ErrGenericConfiguration || backend.queries != 0 {
				t.Fatal("invalid model boundary accepted or queried", model, err, backend.queries)
			}
		})
	}
}

type genericModelTestCodec struct{}

func (genericModelTestCodec) Encode(any) (any, error) { panic("codec must not run") }
func (genericModelTestCodec) Decode(any) (any, error) { panic("codec must not run") }

func TestGenericModelUnsupportedDescriptorsFailBeforeCallbacks(t *testing.T) {
	for _, kind := range []models.Kind{models.ForeignKey, models.OneToOne, models.ManyToMany, models.Generated, models.Binary, models.File, models.Image, models.HStore, models.Range, models.SearchVector, models.Geometry, models.Geography, models.Raster, models.Custom, models.Array} {
		t.Run(string(kind), func(t *testing.T) {
			schema := genericModelTestSchema()
			field := models.Field{Name: "unsupported", Kind: kind}
			if kind == models.ForeignKey || kind == models.OneToOne || kind == models.ManyToMany {
				field.Relation = &models.Relation{Target: "library.Author", OnDelete: models.Cascade}
			}
			if kind == models.Array {
				element := models.IntegerField("item")
				field.Element = &element
			}
			schema.Fields = append(schema.Fields, field)
			options, backend := genericModelTestOptions(t, schema)
			for _, policyOnly := range []bool{false, true} {
				candidate := options
				if policyOnly {
					candidate.PolicyFields = []string{field.Name}
				} else {
					candidate.Fields = []string{field.Name}
				}
				if model, err := newGenericModel(candidate); model != nil || err != ErrGenericConfiguration || backend.queries != 0 {
					t.Fatal("unsupported descriptor accepted", kind, policyOnly, model, err)
				}
			}
		})
	}
	for _, target := range []string{"output", "primary key", "json primary key", "no primary key"} {
		t.Run(target, func(t *testing.T) {
			schema := genericModelTestSchema()
			switch target {
			case "output":
				schema.Fields[1].Codec = genericModelTestCodec{}
			case "primary key":
				schema.Fields[0].Codec = genericModelTestCodec{}
			case "json primary key":
				schema.Fields[0].Kind = models.JSON
			case "no primary key":
				schema.Abstract = true
				schema.Fields = schema.Fields[1:]
			}
			options, _ := genericModelTestOptions(t, schema)
			if model, err := newGenericModel(options); model != nil || err != ErrGenericConfiguration {
				t.Fatal("unsupported identity/codec accepted", model, err)
			}
		})
	}
}

func TestGenericModelConstructorBounds(t *testing.T) {
	for _, test := range []struct {
		name                             string
		fields, output, policy, identity int
		want                             bool
	}{
		{"exact field and output bounds", 1024, 64, 64, 1, true},
		{"too many schema fields", 1025, 1, 0, 1, false},
		{"too many output fields", 66, 65, 0, 1, false},
		{"too many policy fields", 66, 1, 65, 1, false},
		{"exact composite identity", 66, 1, 0, 64, true},
		{"too many identity fields", 67, 1, 0, 65, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := models.Schema{AppLabel: "library", Name: "Book"}
			for i := 0; i < test.fields; i++ {
				name := fmt.Sprintf("field_%d", i)
				schema.Fields = append(schema.Fields, models.IntegerField(name))
				if i < test.identity {
					schema.PrimaryKey = append(schema.PrimaryKey, name)
				}
			}
			options, backend := genericModelTestOptions(t, schema)
			options.Fields = nil
			for i := 0; i < test.output; i++ {
				options.Fields = append(options.Fields, schema.Fields[i].Name)
			}
			for i := 0; i < test.policy; i++ {
				options.PolicyFields = append(options.PolicyFields, schema.Fields[i].Name)
			}
			model, err := newGenericModel(options)
			if (err == nil) != test.want || (model != nil) != test.want || !test.want && err != ErrGenericConfiguration || backend.queries != 0 {
				t.Fatal("constructor bound changed", model != nil, err, backend.queries)
			}
		})
	}
}

func TestGenericModelSnapshotsOptionsAndPrivateSelection(t *testing.T) {
	options, backend := genericModelTestOptions(t)
	options.Fields = []string{"title", "tenant"}
	options.PolicyFields = []string{"tenant", "secret"}
	grants, scopes := 0, 0
	options.AllowAnonymous = true
	options.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { grants++; return nil })
	options.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
		scopes++
		return orm.Q("tenant", int64(7)), nil
	}
	model, err := newGenericModel(options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(model.fields, []string{"title", "tenant"}) || !reflect.DeepEqual(model.selected, []string{"title", "tenant", "secret", "id"}) || model.store == options.Store {
		t.Fatal("output/policy/identity union is incorrect", model.fields, model.selected)
	}
	options.Fields[0], options.PolicyFields[1] = "secret", "title"
	*options.Store = orm.Store{}
	options.Policy, options.Scope, options.AllowAnonymous = nil, nil, false
	if err := model.authorize(context.Background()); err != nil || grants != 1 {
		t.Fatal("configured authorization changed", err, grants)
	}
	query, err := model.query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	statement, args, err := query.SQLContext(context.Background())
	if err != nil || !strings.HasPrefix(statement, `SELECT "title", "tenant", "secret", "id"`) || !reflect.DeepEqual(args, []any{int64(7)}) || scopes != 1 || backend.queries != 0 {
		t.Fatal("selected query/config changed", statement, args, err, scopes, backend.queries)
	}
	if !reflect.DeepEqual(model.fields, []string{"title", "tenant"}) {
		t.Fatal("policy or identity fields became output", model.fields)
	}
}

func TestGenericModelAuthorizationAndTokenCeilings(t *testing.T) {
	active := auth.Principal{ID: "actor", Authenticated: true, Active: true, Permissions: []string{"library.view_book"}}
	for _, test := range []struct {
		name      string
		principal auth.Principal
		anonymous bool
		want      error
		calls     int
	}{
		{"anonymous denied", auth.Principal{}, false, auth.ErrUnauthenticated, 0},
		{"public explicit", auth.Principal{}, true, nil, 1},
		{"active", active, false, nil, 1},
		{"inactive", auth.Principal{ID: "actor", Authenticated: true}, true, auth.ErrPermissionDenied, 0},
		{"missing identity", auth.Principal{Authenticated: true, Active: true}, true, auth.ErrPermissionDenied, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, backend := genericModelTestOptions(t)
			calls := 0
			options.AllowAnonymous = test.anonymous
			options.Policy = auth.PolicyFunc(func(_ context.Context, principal auth.Principal, action string, resource auth.Resource) error {
				calls++
				if action != "view" || resource.App != "library" || resource.Model != "Book" || resource.ID != nil || resource.Object != nil {
					t.Fatal("wrong model grant", action, resource)
				}
				if len(principal.Permissions) > 0 {
					principal.Permissions[0] = "changed"
				}
				return nil
			})
			model, err := newGenericModel(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx := auth.WithPrincipal(context.Background(), test.principal)
			if err := model.authorize(ctx); err != test.want || calls != test.calls || backend.queries != 0 || !reflect.DeepEqual(auth.FromContext(ctx).Permissions, test.principal.Permissions) {
				t.Fatal("authorization changed authority or executed query", err, calls, backend.queries)
			}
		})
	}
	for _, scope := range []string{"library.view_book", "library.change_book"} {
		options, _ := genericModelTestOptions(t)
		calls := 0
		options.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { calls++; return nil })
		model, err := newGenericModel(options)
		if err != nil {
			t.Fatal(err)
		}
		principal, err := auth.ConstrainPrincipal(active, []string{scope})
		if err != nil {
			t.Fatal(err)
		}
		err = model.authorize(auth.WithPrincipal(context.Background(), principal))
		if scope == "library.view_book" && (err != nil || calls != 1) || scope != "library.view_book" && (err != auth.ErrPermissionDenied || calls != 0) {
			t.Fatal("custom policy widened token ceiling", scope, err, calls)
		}
	}
	options, _ := genericModelTestOptions(t)
	options.Policy = auth.ModelPolicy{}
	model, err := newGenericModel(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := model.authorize(auth.WithPrincipal(context.Background(), active)); err != nil {
		t.Fatal(err)
	}
	active.Permissions = nil
	if err := model.authorize(auth.WithPrincipal(context.Background(), active)); err != auth.ErrPermissionDenied {
		t.Fatal("model permission not required", err)
	}
}

type genericModelTestContext struct {
	context.Context
	hook func()
}

func (c *genericModelTestContext) Err() error {
	if hook := c.hook; hook != nil {
		c.hook = nil
		hook()
	}
	return c.Context.Err()
}

func TestGenericModelRootScopeIsFrozenBeforeLaterContextCallback(t *testing.T) {
	options, backend := genericModelTestOptions(t)
	ctx := &genericModelTestContext{Context: context.Background()}
	allowed := []int64{7, 8}
	events := []string{}
	options.Scope = func(_ context.Context, _ auth.Principal, schema models.Schema) (db.Predicate, error) {
		events = append(events, "scope")
		schema.Fields[0].Name = "changed"
		ctx.hook = func() { events = append(events, "context"); allowed[0] = 99 }
		return orm.Q("tenant__in", allowed), nil
	}
	backend.query = func(statement string, args []any) {
		events = append(events, "query")
		if !reflect.DeepEqual(args, []any{int64(7), int64(8)}) || strings.Contains(statement, "changed") || strings.Contains(statement, `"secret"`) {
			t.Fatal("scope or non-output fields changed", statement, args)
		}
	}
	model, err := newGenericModel(options)
	if err != nil || len(events) != 0 || backend.queries != 0 {
		t.Fatal("constructor invoked request hooks/query", err, events)
	}
	query, err := model.query(ctx)
	if err != nil || !reflect.DeepEqual(events, []string{"scope", "context"}) || backend.queries != 0 {
		t.Fatal("root scope order changed", err, events)
	}
	_, err = query.Iterator(ctx)
	if !errors.Is(err, backend.failure) || !reflect.DeepEqual(events, []string{"scope", "context", "query"}) || model.schema.Fields[0].Name != "id" {
		t.Fatal("root scope was rerun, mutated, or error concealed", err, events, model.schema.Fields[0].Name)
	}
}

func TestGenericModelScopeFailuresDoNotExecuteSQL(t *testing.T) {
	for _, mode := range []string{"scope error", "scope cancellation", "nil context", "canceled context"} {
		t.Run(mode, func(t *testing.T) {
			options, backend := genericModelTestOptions(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			options.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				calls++
				if mode == "scope cancellation" {
					cancel()
					return db.Predicate{}, nil
				}
				return db.Predicate{}, errors.Join(auth.ErrPermissionDenied, errors.New("scope provider failure"))
			}
			model, err := newGenericModel(options)
			if err != nil {
				t.Fatal(err)
			}
			var input context.Context = ctx
			if mode == "nil context" {
				input = nil
			} else if mode == "canceled context" {
				cancel()
			}
			if _, err := model.query(input); err != ErrUnavailable || backend.queries != 0 {
				t.Fatal("scope failure broadened query or became absence/denial", err, backend.queries)
			}
			if (mode == "nil context" || mode == "canceled context") && calls != 0 {
				t.Fatal("scope ran after invalid context", calls)
			}
		})
	}
}

func TestGenericModelRelatedScopeUsesItsOwnSchema(t *testing.T) {
	author := models.Schema{AppLabel: "library", Name: "Author", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("name"), models.IntegerField("tenant"),
	}}
	book := genericModelTestSchema()
	book.Fields = append(book.Fields, models.ForeignKeyField("owner", models.Relation{Target: author.Key(), OnDelete: models.Cascade}))
	options, backend := genericModelTestOptions(t, book, author)
	if err := options.Store.Registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	scopes := []string{}
	options.Scope = func(_ context.Context, _ auth.Principal, schema models.Schema) (db.Predicate, error) {
		scopes = append(scopes, schema.Key())
		if schema.Key() == author.Key() {
			return orm.Q("tenant", int64(9)), nil
		}
		return orm.Q("tenant", int64(7)), nil
	}
	model, err := newGenericModel(options)
	if err != nil {
		t.Fatal(err)
	}
	query, err := model.query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// This deliberately derives a to-one ORM query to inspect the foundation's
	// scope wrapper. It does not assert generic List/Detail eager-load support.
	statement, args, err := query.Only("id", "title", "owner").SelectRelated("owner").SQLContext(context.Background())
	if err != nil || !reflect.DeepEqual(scopes, []string{book.Key(), author.Key()}) || !reflect.DeepEqual(args, []any{int64(9), int64(7)}) || !strings.Contains(statement, "LEFT JOIN") || backend.queries != 0 {
		t.Fatal("related root/target scope was not independent", statement, args, err, scopes, backend.queries)
	}
}
