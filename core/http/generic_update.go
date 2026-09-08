package http

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

// UpdateViewOptions declares a scoped, existing-object HTML form. Fields is an
// explicit input allowlist; ReadonlyFields are neither rendered nor accepted.
// Factory must return a fresh matching model. Initial values come only from the
// selected row, not Factory defaults or an application initial-value callback.
type UpdateViewOptions struct {
	TemplateViewOptions
	Store          *orm.Store
	Model          string
	Policy         auth.Policy
	AllowAnonymous bool
	Scope          func(context.Context, auth.Principal, models.Schema) (db.Predicate, error)
	Factory        func() models.Model
	// Key returns every primary-key component. ErrInvalidLookup means not found;
	// other callback failures are operational errors. Scope runs before Key.
	Key            func(*http.Request) (map[string]any, error)
	Fields         []string
	ReadonlyFields []string
	Prepare        func(context.Context, models.Record) error
	// ValidateWrite is mandatory. Each invocation receives a detached proposed
	// or persisted record, never the mutable model that will be saved.
	ValidateWrite func(context.Context, auth.Principal, models.Record) error
	// SuccessURL receives only detached original primary-key values. It must
	// return a local root-relative URL, sealed before the final stored-row fence.
	SuccessURL   func(context.Context, map[string]any) (string, error)
	CSRF         *security.CSRFConfig
	MaxBodyBytes int64
}

type genericUpdate struct {
	form         *genericCreate
	key          func(*http.Request) (map[string]any, error)
	updateFields []string
}

// NewUpdateView returns an immutable GET/HEAD/POST handler for local scalar and
// JSON models. POST requires row locks and its own durable transaction; there is
// no insert fallback, relation/file persistence, or automatic retry. A row lock
// serializes current POSTs, not edits made since an earlier browser GET.
func NewUpdateView(options UpdateViewOptions) (handler http.Handler, err error) {
	defer func() {
		if recover() != nil {
			handler, err = nil, ErrGenericConfiguration
		}
	}()
	if options.Key == nil {
		return nil, ErrGenericConfiguration
	}
	form, err := newGenericCreate(CreateViewOptions{
		TemplateViewOptions: options.TemplateViewOptions,
		Store:               options.Store, Model: options.Model, Policy: options.Policy,
		AllowAnonymous: options.AllowAnonymous, Scope: options.Scope, Factory: options.Factory,
		Fields: options.Fields, ReadonlyFields: options.ReadonlyFields, Prepare: options.Prepare,
		ValidateWrite: options.ValidateWrite, SuccessURL: options.SuccessURL,
		CSRF: options.CSRF, MaxBodyBytes: options.MaxBodyBytes,
	})
	if err != nil {
		return nil, err
	}
	capabilities := form.model.store.Backend.Capabilities()
	if capabilities.Require("transactions", "row_locks") != nil {
		return nil, ErrGenericConfiguration
	}
	keys := map[string]bool{}
	for _, field := range form.model.schema.PKFields() {
		keys[field.Name] = true
	}
	fields := make([]string, 0, len(form.model.schema.Fields))
	for _, field := range form.model.schema.Fields {
		if field.IsStored() && !keys[field.Name] {
			fields = append(fields, field.Name)
		}
	}
	if len(fields) == 0 {
		return nil, ErrGenericConfiguration
	}
	view := &genericUpdate{form: form, key: options.Key, updateFields: fields}
	return http.HandlerFunc(view.serve), nil
}

func (v *genericUpdate) serve(w http.ResponseWriter, r *http.Request) {
	response, frozen, err := v.form.executeForm(r, v.authorize, v.run)
	if err != nil {
		response = genericErrorResponse(err)
	}
	if frozen == nil {
		frozen = &http.Request{Method: http.MethodGet, Header: make(http.Header)}
	}
	if response.Headers == nil {
		response.Headers = make(http.Header)
	}
	response.Headers.Set("Cache-Control", "private, no-store")
	response.Headers.Set("X-Content-Type-Options", "nosniff")
	response.Headers.Set("Content-Length", strconv.Itoa(len(response.Body)))
	if err := response.Write(w, frozen); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func (v *genericUpdate) authorize(call *readViewCall) error {
	ctx := call.base.Context()
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	err := v.form.options.Authorize(call.request())
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	principal := auth.FromContext(ctx)
	if !principal.Authenticated && !v.form.model.allowAnonymous {
		return auth.ErrUnauthenticated
	}
	if principal.Authenticated && (!principal.Active || principal.ID == "") {
		return auth.ErrPermissionDenied
	}
	err = v.form.model.policy.Authorize(ctx, principal, "change", auth.Resource{App: v.form.model.schema.AppLabel, Model: v.form.model.schema.Name})
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	return err
}
