package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func ordinaryRelationSite(t *testing.T) (*Site, *testDB, ModelAdmin) {
	t.Helper()
	site, database := newTestSite(t)
	site.config.Store = relationStore{database}
	options := ModelAdmin{Schema: (&relationSource{}).Schema(), Fields: []string{"parent"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 || ids[0] != "1" {
			return nil, auth.ErrPermissionDenied
		}
		return []any{int64(1)}, nil
	}}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	return site, database, options
}

type selectCountStore struct {
	Store
	full, exact, locked int
}

func (store *selectCountStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	base, err := store.Store.Scope(ctx, p, site, schema)
	if err != nil {
		return nil, err
	}
	if schema.Key() == "shop.Product" {
		return selectCountScope{ScopedStore: base, owner: store}, nil
	}
	return base, nil
}

type selectCountScope struct {
	ScopedStore
	owner *selectCountStore
}

func (scope selectCountScope) List(ctx context.Context, query ListQuery) (Page, error) {
	if id := query.Filters["ID"]; id != "" {
		scope.owner.exact++
		if query.Limit != 2 {
			return Page{}, errors.New("unbounded exact target query")
		}
		row, err := scope.ScopedStore.Get(ctx, id, false)
		if errors.Is(err, ErrNotFound) {
			return Page{}, nil
		}
		return Page{Objects: []Object{row}, Count: 1}, err
	}
	scope.owner.full++
	return scope.ScopedStore.List(ctx, query)
}

func (scope selectCountScope) Get(ctx context.Context, id string, lock bool) (Object, error) {
	if lock {
		scope.owner.locked++
	}
	return scope.ScopedStore.Get(ctx, id, lock)
}

func TestOrdinaryRelationSelectRechecksOnlyOneLockedTarget(t *testing.T) {
	site, database, options := ordinaryRelationSite(t)
	for i := int64(3); i <= 202; i++ {
		database.records[strconv.FormatInt(i, 10)] = testRecord{ID: i, Tenant: "one", Name: "Scoped choice"}
	}
	resolverCalls := 0
	options.ResolveRelation = func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		resolverCalls++
		id, err := strconv.ParseInt(ids[0], 10, 64)
		return []any{id}, err
	}
	counted := &selectCountStore{Store: site.config.Store}
	site.config.Store = counted
	_, state, err := site.toOneSelectOverrides(context.Background(), principal(), options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counted.full != 1 || resolverCalls != 201 {
		t.Fatal("unexpected display candidate cost", counted.full, resolverCalls)
	}
	field, _ := options.Schema.Field("parent")
	for range 3 {
		if _, err := state.resolve(context.Background(), field, []string{"1"}); err != nil {
			t.Fatal(err)
		}
	}
	if counted.full != 1 || counted.exact != 3 || counted.locked != 6 || resolverCalls != 204 {
		t.Fatal("selected target checks rescanned choices or skipped target locks", counted.full, counted.exact, counted.locked, resolverCalls)
	}
}

func TestOrdinaryRelationSelectIncludesOnlyScopedChoicesAndExplicitEmpty(t *testing.T) {
	site, _, _ := ordinaryRelationSite(t)
	page := perform(site, "GET", "/admin/shop/editor/add/", principal(), nil, nil)
	if page.Code != 200 {
		t.Fatal(page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "Other tenant") {
		t.Fatal("hidden target disclosed")
	}
	for _, want := range []string{`<option value=""`, `<option value="1"`, "Public record"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatal("ordinary scoped relation select missing choice", want, page.Body.String())
		}
	}
	if strings.Contains(page.Body.String(), `value="1" selected`) {
		t.Fatal("empty relation silently selected first record")
	}
}

func TestOrdinaryRelationSelectPreservesCustomAndReadonlyModes(t *testing.T) {
	site, _, options := ordinaryRelationSite(t)
	for _, mode := range []string{"readonly", "raw", "autocomplete", "custom-text", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			copy := options
			overrides := map[string]forms.Field{}
			readonly := []string{}
			switch mode {
			case "readonly":
				readonly = []string{"parent"}
			case "raw":
				copy.RawIDFields = []string{"parent"}
			case "autocomplete":
				copy.AutocompleteFields = []string{"parent"}
			default:
				field := forms.NewField("parent", forms.ModelChoice)
				field.Widget = forms.InputWidget{Type: "text"}
				field.Disabled = mode == "disabled"
				overrides["parent"] = field
			}
			_, state, err := site.toOneSelectOverrides(context.Background(), principal(), copy, readonly, overrides)
			if err != nil || len(state.names) != 0 {
				t.Fatal("explicit rendering mode was replaced", err, state)
			}
		})
	}
}

type selectFaultStore struct {
	Store
	mutate func(context.Context, ListQuery) (Page, error)
}

func (store selectFaultStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	base, err := store.Store.Scope(ctx, p, site, schema)
	if schema.Key() == "shop.Product" {
		return selectFaultScope{ScopedStore: base, mutate: store.mutate}, err
	}
	return base, err
}

type selectFaultScope struct {
	ScopedStore
	mutate func(context.Context, ListQuery) (Page, error)
}

func (scope selectFaultScope) List(ctx context.Context, q ListQuery) (Page, error) {
	return scope.mutate(ctx, q)
}

func TestOrdinaryRelationSelectLookupFailuresNeverReturnPartialChoices(t *testing.T) {
	for _, mode := range []string{"overflow", "count-overflow", "provider", "cancel", "wrong-model", "duplicate", "policy-provider", "resolver-provider", "identity-substitution"} {
		t.Run(mode, func(t *testing.T) {
			site, database, options := ordinaryRelationSite(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			scope := &testScope{db: database, tenant: "one"}
			candidate := scope.object(database.records["1"])
			site.config.Store = selectFaultStore{Store: site.config.Store, mutate: func(_ context.Context, q ListQuery) (Page, error) {
				if q.Limit != 1001 {
					t.Fatal("unbounded ordinary select", q)
				}
				switch mode {
				case "overflow":
					return Page{Objects: make([]Object, 1001)}, nil
				case "count-overflow":
					return Page{Objects: []Object{candidate}, Count: 1001}, nil
				case "provider":
					return Page{Objects: []Object{candidate}}, errors.New("private provider")
				case "cancel":
					cancel()
					return Page{Objects: []Object{candidate}}, nil
				case "wrong-model":
					record, _ := models.Bind(&relationSource{})
					return Page{Objects: []Object{candidate, {Record: record}}}, nil
				case "duplicate":
					return Page{Objects: []Object{candidate, candidate}}, nil
				}
				return Page{Objects: []Object{candidate}}, nil
			}}
			if mode == "policy-provider" {
				site.config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error {
					return errors.New("private policy provider")
				})
			}
			if mode == "resolver-provider" {
				options.ResolveRelation = func(context.Context, models.Field, []string) ([]any, error) {
					return nil, errors.New("private resolver provider")
				}
			}
			if mode == "identity-substitution" {
				options.ResolveRelation = func(context.Context, models.Field, []string) ([]any, error) { return []any{int64(2)}, nil }
			}
			choices, state, err := site.toOneSelectOverrides(ctx, principal(), options, nil, nil)
			if err == nil || choices != nil || state != nil {
				t.Fatal("partial choices escaped failed lookup", choices, state, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
		})
	}
}

func TestOrdinaryRelationSelectReloadsEligibilityAndRestrictsOverrides(t *testing.T) {
	site, database, options := ordinaryRelationSite(t)
	ctx := context.Background()
	_, state, err := site.toOneSelectOverrides(ctx, principal(), options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	field, _ := options.Schema.Field("parent")
	if values, err := state.resolve(ctx, field, []string{"1"}); err != nil || len(values) != 1 || values[0] != int64(1) {
		t.Fatal(values, err)
	}
	for _, ids := range [][]string{{"2"}, {"1", "2"}, {""}, nil} {
		if _, err := state.resolve(ctx, field, ids); err == nil {
			t.Fatal("forged choice accepted", ids)
		}
	}
	value := database.records["1"]
	value.Tenant = "two"
	database.records["1"] = value
	if _, err := state.resolve(ctx, field, []string{"1"}); err == nil {
		t.Fatal("stale visible choice bypassed new scope")
	}
	value.Tenant = "one"
	database.records["1"] = value
	override := forms.NewField("parent", forms.ModelChoice)
	override.Choices = []forms.Choice{{Value: "2", Label: "Forged hidden target"}}
	fields, _, err := site.toOneSelectOverrides(ctx, principal(), options, nil, map[string]forms.Field{"parent": override})
	if err != nil || len(fields["parent"].Choices) != 1 || fields["parent"].Choices[0].Value != "" {
		t.Fatal("override widened target scope", fields, err)
	}
}
