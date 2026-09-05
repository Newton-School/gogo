package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

type relationSource struct {
	models.Base
	ID, ParentID int64
}

type orderedLookupStore struct {
	relationStore
	rows []Object
}

func (s orderedLookupStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	base, err := s.relationStore.Scope(ctx, p, site, schema)
	if schema.Key() == "shop.Product" {
		return orderedLookupScope{ScopedStore: base, rows: s.rows}, err
	}
	return base, err
}

type orderedLookupScope struct {
	ScopedStore
	rows []Object
}

func (s orderedLookupScope) List(_ context.Context, q ListQuery) (Page, error) {
	if q.Offset >= len(s.rows) {
		return Page{}, nil
	}
	return Page{Objects: s.rows[q.Offset:min(q.Offset+q.Limit, len(s.rows))]}, nil
}
func TestAutocompletePagesEligibleChoicesWithoutHiddenCounts(t *testing.T) {
	site, database := newTestSite(t)
	store := orderedLookupStore{relationStore: relationStore{database}}
	scope := &testScope{db: database}
	for id := int64(1); id <= 145; id++ {
		store.rows = append(store.rows, scope.object(testRecord{ID: id, Tenant: "one", Name: fmt.Sprintf("Choice %d", id)}))
	}
	site.config.Store = store
	if err := site.Register(ModelAdmin{Schema: (&relationSource{}).Schema(), Fields: []string{"parent"}, AutocompleteFields: []string{"parent"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 {
			return nil, auth.ErrPermissionDenied
		}
		var id int64
		_, _ = fmt.Sscan(ids[0], &id)
		if id <= 100 {
			return nil, auth.ErrPermissionDenied
		}
		return []any{id}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	for page, expected := range []struct {
		first string
		count int
		more  bool
	}{{"101", 20, true}, {"121", 20, true}, {"141", 5, false}} {
		response := perform(site, "GET", fmt.Sprintf("/admin/autocomplete/?app_label=shop&model_name=editor&field_name=parent&page=%d", page+1), principal(), nil, nil)
		var result struct {
			Results    []struct{ ID, Text string }
			Pagination struct{ More bool }
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Results) != expected.count || result.Results[0].ID != expected.first || result.Pagination.More != expected.more {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}

func TestRelationWidgetsUseScopedEndpointAndValidateRawIDs(t *testing.T) {
	site, database := newTestSite(t)
	site.config.Store = relationStore{database}
	options := ModelAdmin{Schema: (&relationSource{}).Schema(), Fields: []string{"parent"}, AutocompleteFields: []string{"parent"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 || ids[0] != "1" {
			return nil, auth.ErrPermissionDenied
		}
		return []any{int64(1)}, nil
	}}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	response := perform(site, "GET", "/admin/shop/editor/add/", principal(), nil, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `data-relation-url="/admin/autocomplete/?`) || !strings.Contains(response.Body.String(), `id="id_parent_choices"`) || !strings.Contains(response.Body.String(), `role="status"`) || !strings.Contains(response.Body.String(), ".js\" defer") {
		t.Fatal(response.Code, response.Body.String())
	}
	record, _ := models.Bind(&relationSource{})
	overrides, err := site.relationOverrides(context.Background(), options, Object{Record: record, ID: `key"<&`})
	if err != nil {
		t.Fatal(err)
	}
	widget, err := overrides["parent"].Widget.Render(forms.BoundField{Field: overrides["parent"], Name: "parent", ID: "id_parent", Value: `"><script>alert(1)</script>`})
	if err != nil || strings.Contains(string(widget), `<script>`) || !strings.Contains(string(widget), "object_id=") {
		t.Fatal(widget, err)
	}
	for _, id := range []string{"1", "2"} {
		form, err := forms.NewModelForm(context.Background(), record, forms.ModelFormOptions{Fields: options.Fields, Overrides: overrides, ResolveRelation: options.ResolveRelation}, forms.WithData(map[string][]string{"parent": {id}}))
		// A complete model checker is required for FK existence, separately
		// from relation eligibility. Verify the resolver's field rejection here.
		if err != nil {
			t.Fatal(err)
		}
		if id == "2" && len(form.Errors()["parent"]) == 0 {
			t.Fatal("raw widget bypassed resolver")
		}
	}
	asset := perform(site, "GET", "/admin/assets/admin."+site.jsVersion+".js", auth.Principal{}, nil, nil)
	if asset.Code != 200 || len(asset.Result().Cookies()) != 0 || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") || !strings.Contains(asset.Body.String(), "textContent") {
		t.Fatal(asset.Code, asset.Header())
	}
}

func (*relationSource) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Editor", Fields: []models.Field{models.BigAutoField("ID"), models.ForeignKeyField("parent", models.Relation{Target: "shop.Product", OnDelete: models.Protect}, models.WithStructField("ParentID"))}}
}

type relationStore struct{ *testDB }

func (s relationStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	base, err := s.testDB.Scope(ctx, p, site, schema)
	if err != nil {
		return nil, err
	}
	if schema.Key() == "shop.Editor" {
		return sourceScope{ScopedStore: base}, nil
	}
	return base, nil
}

type sourceScope struct{ ScopedStore }

func (s sourceScope) New(context.Context) (Object, error) {
	record, err := models.Bind(&relationSource{})
	return Object{Record: record}, err
}

func TestAutocompleteResolvesDeclaredRelationAndChecksBothPolicies(t *testing.T) {
	site, database := newTestSite(t)
	site.config.Store = relationStore{database}
	if err := site.Register(ModelAdmin{Schema: (&relationSource{}).Schema(), Fields: []string{"parent"}, AutocompleteFields: []string{"parent"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 || ids[0] != "1" {
			return nil, auth.ErrPermissionDenied
		}
		return []any{int64(1)}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	path := "/admin/autocomplete/?app_label=shop&model_name=editor&field_name=parent&term=Public"
	response := perform(site, "GET", path, principal(), nil, nil)
	var result struct {
		Results    []struct{ ID, Text string }
		Pagination struct{ More bool }
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Results) != 1 || result.Results[0].ID != "1" || strings.Contains(response.Body.String(), "Other tenant") {
		t.Fatal(response.Code, response.Body.String())
	}
	site.config.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, action string, resource auth.Resource) error {
		if resource.Model == "Product" && action == "view" {
			return auth.ErrPermissionDenied
		}
		return nil
	})
	response = perform(site, "GET", path, principal(), nil, nil)
	if response.Code != 403 || strings.Contains(response.Body.String(), "Public record") {
		t.Fatal("target permission bypass", response.Code, response.Body.String())
	}
	site.config.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, action string, resource auth.Resource) error {
		if resource.Model == "Editor" && action == "add" {
			return auth.ErrPermissionDenied
		}
		return nil
	})
	response = perform(site, "GET", path, principal(), nil, nil)
	if response.Code != 403 {
		t.Fatal("source permission bypass", response.Code)
	}
}

func TestAutocompleteRejectsArbitraryTargetsAndUndeclaredFields(t *testing.T) {
	site, _ := newTestSite(t)
	for _, path := range []string{"/admin/autocomplete/?app_label=shop&model_name=product&field_name=Name", "/admin/autocomplete/?target_model=secret", "/admin/autocomplete/?page=10001"} {
		response := perform(site, "GET", path, principal(), nil, nil)
		if response.Code != 400 && response.Code != 403 {
			t.Fatal(path, response.Code, response.Body.String())
		}
	}
}

func TestAutocompleteRejectsResolverIdentitySubstitution(t *testing.T) {
	site, database := newTestSite(t)
	site.config.Store = relationStore{database}
	if err := site.Register(ModelAdmin{Schema: (&relationSource{}).Schema(), Fields: []string{"parent"}, AutocompleteFields: []string{"parent"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		// A resolver cannot approve the displayed object by returning a
		// different identity with the same result cardinality.
		return []any{int64(9876)}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	response := perform(site, "GET", "/admin/autocomplete/?app_label=shop&model_name=editor&field_name=parent&term=Public", principal(), nil, nil)
	var result struct{ Results []struct{ ID, Text string } }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Results) != 0 || strings.Contains(response.Body.String(), "Public record") {
		t.Fatal("resolver substitution disclosed a mismatched choice", response.Code, response.Body.String())
	}
}
