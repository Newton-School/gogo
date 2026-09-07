package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type deletionBackend struct {
	db.Backend
	caps db.Capabilities
}

func (b deletionBackend) Capabilities() db.Capabilities { return b.caps }

type deletionCodec struct{}

func (deletionCodec) Encode(v any) (any, error) { return v, nil }
func (deletionCodec) Decode(v any) (any, error) { return v, nil }

func TestDeleteConstructorRequiresExplicitSafeBoundaries(t *testing.T) {
	key := func(*http.Request) (Values, error) { return Values{"id": 1}, nil }
	options := DeleteOptions{ValidateDelete: func(context.Context, auth.Principal, orm.DeletionPlan) error { return nil }, Audit: func(context.Context, MutationEvent) error { return nil }}
	for _, resource := range []*Resource{nil, {}} {
		if _, err := resource.DeleteHandler(key, options); err == nil {
			t.Fatal("unconfigured resource accepted")
		}
	}
	schema := models.Schema{AppLabel: "test", Name: "Deleted", Fields: []models.Field{models.BigAutoField("id"), models.CharField("name")}}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	output, err := FromModel(schema, ModelOptions{Fields: []string{"id", "name"}})
	if err != nil {
		t.Fatal(err)
	}
	newResource := func() *Resource {
		r, err := NewResource(ResourceConfig{Store: orm.New(deletionBackend{caps: db.Capabilities{"transactions": true, "savepoints": true, "row_locks": true}}, registry), Model: schema.Key(), Serializer: output, Policy: auth.ModelPolicy{}, Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if _, err := newResource().DeleteHandler(key, options); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Resource, *DeleteOptions){
		func(_ *Resource, o *DeleteOptions) { o.ValidateDelete = nil }, func(_ *Resource, o *DeleteOptions) { o.Audit = nil },
		func(_ *Resource, o *DeleteOptions) { o.Timeout = time.Nanosecond }, func(_ *Resource, o *DeleteOptions) { o.Timeout = 6 * time.Minute },
		func(_ *Resource, o *DeleteOptions) { o.MaxObjects = -1 }, func(_ *Resource, o *DeleteOptions) { o.MaxObjects = 10001 },
		func(_ *Resource, o *DeleteOptions) { o.MaxObjects = 10; o.MaxWork = 9 }, func(_ *Resource, o *DeleteOptions) { o.MaxWork = 1000001 },
		func(_ *Resource, o *DeleteOptions) { o.RequireMatch = true },
		func(r *Resource, _ *DeleteOptions) { r.config.SelectRelated = []string{"parent"} },
		func(r *Resource, _ *DeleteOptions) { r.schema.Unmanaged = true }, func(r *Resource, _ *DeleteOptions) { r.schema.Proxy = true }, func(r *Resource, _ *DeleteOptions) { r.schema.Abstract = true }, func(r *Resource, _ *DeleteOptions) { r.schema.Parent = "test.Base" },
		func(r *Resource, _ *DeleteOptions) { r.schema.Fields[1].Codec = deletionCodec{} },
	} {
		r, o := newResource(), options
		change(r, &o)
		if _, err := r.DeleteHandler(key, o); err == nil {
			t.Fatal("invalid deletion configuration accepted")
		}
	}
	for _, capability := range []string{"transactions", "savepoints", "row_locks"} {
		r := newResource()
		b := r.config.Store.Backend.(deletionBackend)
		delete(b.caps, capability)
		if _, err := r.DeleteHandler(key, options); err == nil {
			t.Fatal("missing capability accepted", capability)
		}
	}
	if _, err := newResource().DeleteHandler(nil, options); err == nil {
		t.Fatal("missing key decoder accepted")
	}
}

func TestDeletionFenceDescriptorRejectsNestedAndCyclicCodecs(t *testing.T) {
	base := models.CharField("value")
	if !deletionBuiltinField(base) {
		t.Fatal("built-in rejected")
	}
	for _, leaf := range []models.Field{models.CharField("value", func(f *models.Field) { f.Codec = deletionCodec{} }), {Name: "value", Kind: models.Custom}} {
		field := leaf
		for i := 0; i < 4; i++ {
			copy := field
			field = models.Field{Name: "array", Kind: models.Array, Element: &copy}
		}
		if deletionBuiltinField(field) {
			t.Fatal("nested custom codec accepted")
		}
	}
	base.Element = &base
	if deletionBuiltinField(base) {
		t.Fatal("cyclic descriptor accepted")
	}
}

func TestDeletePublicErrorKeepsJoinedOperationalFailures(t *testing.T) {
	for _, sentinel := range []error{auth.ErrPermissionDenied, auth.ErrUnauthenticated, orm.ErrNotFound, orm.ErrProtectedRelation, orm.ErrDeleteLimit} {
		want := 404
		if sentinel == orm.ErrProtectedRelation || sentinel == orm.ErrDeleteLimit {
			want = 409
		}
		for _, err := range []error{sentinel, fmt.Errorf("private wrapper: %w", sentinel), errors.Join(sentinel, sentinel)} {
			if got := ghttp.PublicError(deletePublicError(err)); got.Status != want {
				t.Fatal("pure error lost", got)
			}
		}
		for _, failure := range []struct {
			err    error
			status int
		}{{context.Canceled, 503}, {context.DeadlineExceeded, 504}, {&db.Error{Code: db.Unavailable}, 503}, {&db.Error{Code: db.UnknownCommit}, 503}} {
			mixed := errors.Join(sentinel, failure.err)
			if got := ghttp.PublicError(publicMutationError(deletePublicError(mixed))); got.Status != failure.status {
				t.Fatal("incomplete check masked provider failure", got)
			}
		}
		mixed := errors.Join(sentinel, errors.New("synthetic opaque provider failure"))
		if got := ghttp.PublicError(publicMutationError(deletePublicError(mixed))); got.Status < 500 {
			t.Fatal("opaque provider failure became a completed decision", got)
		}
	}
	for _, code := range []db.ErrorCode{db.Unavailable, db.UnknownCommit, db.Canceled, db.Deadlock} {
		if got := ghttp.PublicError(publicMutationError(deletePublicError(&db.Error{Code: code, Cause: auth.ErrPermissionDenied}))); got.Status < 500 {
			t.Fatal("provider cause displaced operational code", got)
		}
	}
}
