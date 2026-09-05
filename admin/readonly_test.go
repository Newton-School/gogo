package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

type readonlyScope struct {
	ScopedStore
	rows   map[string][]Object
	err    error
	calls  int
	onRead func()
}

func (s *readonlyScope) ReadRelations(context.Context, Object, []string) (map[string][]Object, error) {
	s.calls++
	if s.onRead != nil {
		s.onRead()
	}
	return s.rows, s.err
}

func TestReadonlyRelationsNoticeCancellationInsideSuccessfulCallbacks(t *testing.T) {
	for _, stage := range []string{"model", "reader", "object", "resolver"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			site, _ := newTestSite(t)
			schema := models.Schema{AppLabel: "shop", Name: "Collection", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("products", models.Relation{Target: "shop.Product"}, models.Optional)}}
			row, _ := models.NewRecord(schema)
			_ = row.Set("id", int64(1))
			row.State().Persisted = true
			related, _ := models.Bind(&testRecord{ID: 1, Tenant: "a", Name: "Visible"})
			reader := &readonlyScope{rows: map[string][]Object{"products": {{ID: "one", Record: related, Label: "Visible"}}}}
			if stage == "reader" {
				reader.onRead = cancel
			}
			site.config.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
				if stage == "model" && resource.Object == nil || stage == "object" && resource.Object != nil {
					cancel()
				}
				return nil
			})
			options := ModelAdmin{Schema: schema, ReadonlyFields: []string{"products"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
				if stage == "resolver" {
					cancel()
				}
				return []any{ids[0]}, nil
			}}
			values, err := site.readonlyValues(ctx, principal(), options, Object{ID: "collection", Record: row}, reader, options.ReadonlyFields)
			if !errors.Is(err, context.Canceled) || values != nil {
				t.Fatal("callback cancellation rendered a partial readonly snapshot", err)
			}
			if stage == "model" && reader.calls != 0 {
				t.Fatal("reader called after model policy canceled")
			}
		})
	}
}

func TestReadonlyRelationsEscapeLabelsAndApplyObjectAndResolverPolicy(t *testing.T) {
	site, _ := newTestSite(t)
	schema := models.Schema{AppLabel: "shop", Name: "Collection", Fields: []models.Field{models.BigAutoField("id"), models.CharField("name"), models.ManyToManyField("products", models.Relation{Target: "shop.Product"}, models.Optional)}}
	row, _ := models.NewRecord(schema)
	_ = row.Set("id", int64(1))
	_ = row.Set("name", "Collection")
	row.State().Persisted = true
	object := Object{ID: "collection-1", Record: row}
	related := func(id int64, label string) Object {
		model := &testRecord{ID: id, Tenant: "a", Name: label}
		model.ModelState().Persisted = true
		record, _ := models.Bind(model)
		return Object{ID: label, Record: record, Label: label}
	}
	reader := &readonlyScope{rows: map[string][]Object{"products": {related(1, `<img src=x onerror=bad()>`), related(2, "hidden-by-policy"), related(3, "hidden-by-resolver")}}}
	site.config.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
		if record, ok := resource.Object.(models.Record); ok && record != nil {
			value, _ := record.Get("ID")
			if value == int64(2) {
				return auth.ErrPermissionDenied
			}
		}
		return nil
	})
	options := ModelAdmin{Schema: schema, Fields: []string{"name", "products"}, ReadonlyFields: []string{"products"}, Fieldsets: []Fieldset{{Name: "Collection", Fields: []string{"name", "products"}}}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if ids[0] == "3" {
			return nil, auth.ErrPermissionDenied
		}
		return []any{ids[0]}, nil
	}}
	values, err := site.readonlyValues(context.Background(), principal(), options, object, reader, options.ReadonlyFields)
	if err != nil || values["products"] != `<img src=x onerror=bad()>` {
		t.Fatal("scoped readonly selection failed", err)
	}
	form, err := forms.NewModelForm(context.Background(), row, forms.ModelFormOptions{Fields: options.Fields, Readonly: options.ReadonlyFields})
	if err != nil {
		t.Fatal(err)
	}
	body, err := site.renderModelForm(context.Background(), options, object, form, options.ReadonlyFields, values)
	if err != nil || strings.Contains(string(body), "<img") || strings.Contains(string(body), "hidden-by") || strings.Contains(string(body), `name="products"`) {
		t.Fatal("readonly renderer leaked or enabled relationship", err)
	}
	if _, err := site.renderModelForm(context.Background(), options, object, form, options.ReadonlyFields, nil); err == nil {
		t.Fatal("readonly renderer accepted a missing scoped relationship snapshot")
	}
	reader.err = errors.New("reader unavailable")
	if _, err := site.readonlyValues(context.Background(), principal(), options, object, reader, options.ReadonlyFields); err == nil {
		t.Fatal("reader error silently rendered partial values")
	}
	reader.calls = 0
	site.config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return auth.ErrPermissionDenied })
	values, err = site.readonlyValues(context.Background(), principal(), options, object, reader, options.ReadonlyFields)
	if err != nil || reader.calls != 0 || values["products"] != "" {
		t.Fatal("denied target model queried or exposed relationships")
	}
}

func TestReadonlyRelationsPropagateProviderFailuresAndCancellation(t *testing.T) {
	for _, stage := range []string{"model", "object", "resolver"} {
		for _, failure := range []error{errors.New("provider unavailable"), context.Canceled, context.DeadlineExceeded} {
			t.Run(stage+"/"+failure.Error(), func(t *testing.T) {
				site, _ := newTestSite(t)
				schema := models.Schema{AppLabel: "shop", Name: "Collection", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("products", models.Relation{Target: "shop.Product"}, models.Optional)}}
				row, _ := models.NewRecord(schema)
				_ = row.Set("id", int64(1))
				row.State().Persisted = true
				related, _ := models.Bind(&testRecord{ID: 1, Tenant: "a", Name: "Visible"})
				reader := &readonlyScope{rows: map[string][]Object{"products": {{ID: "one", Record: related, Label: "Visible"}}}}
				site.config.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
					if stage == "model" && resource.Object == nil || stage == "object" && resource.Object != nil {
						return failure
					}
					return nil
				})
				options := ModelAdmin{Schema: schema, ReadonlyFields: []string{"products"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
					if stage == "resolver" {
						return nil, failure
					}
					return []any{ids[0]}, nil
				}}
				values, err := site.readonlyValues(context.Background(), principal(), options, Object{ID: "collection", Record: row}, reader, options.ReadonlyFields)
				if !errors.Is(err, failure) || values != nil {
					t.Fatal("provider failure rendered a partial readonly snapshot", err)
				}
				if stage == "model" && reader.calls != 0 {
					t.Fatal("reader called after model policy failure")
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := site.readonlyValues(ctx, principal(), options, Object{ID: "collection", Record: row}, reader, options.ReadonlyFields); !errors.Is(err, context.Canceled) {
					t.Fatal("canceled request continued readonly rendering", err)
				}
			})
		}
	}
}
