package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type DeleteOptions struct {
	// ValidateDelete is a read-only graph policy for the complete freshly locked
	// effect, including cascades, relation updates and automatic join removals.
	// It runs before writes and again after audit; the latter still receives
	// the original graph, whose rows may now be absent. It must not depend on
	// reloading an already deleted root. Model action and target scope checks
	// remain framework-owned and cannot be bypassed by returning nil here.
	ValidateDelete func(context.Context, auth.Principal, orm.DeletionPlan) error
	Audit          func(context.Context, MutationEvent) error
	Timeout        time.Duration
	MaxObjects     int
	MaxWork        int
	CSRF           *security.CSRFConfig
	RequireMatch   bool
}

// DeleteHandler is an explicit DELETE endpoint. It consumes no body or query
// parameters and never exposes writes through Resource.Routes. Receipt replay
// is not enabled: Idempotency-Key is rejected until an application-owned policy
// can authorize replay of a deleted object. Unknown outcomes require external
// reconciliation, not a blind retry under another identity.
func (s *Resource) DeleteHandler(key func(*http.Request) (Values, error), options DeleteOptions) (http.Handler, error) {
	if s == nil || s.config.Store == nil || s.config.Store.Backend == nil || s.config.Store.Registry == nil || s.config.Policy == nil || s.config.Scope == nil || key == nil || options.ValidateDelete == nil || options.Audit == nil {
		return nil, errors.New("api: delete requires key decoder, graph policy and atomic audit")
	}
	if err := s.config.Store.Backend.Capabilities().Require("transactions", "savepoints", "row_locks"); err != nil {
		return nil, err
	}
	if len(s.config.SelectRelated) > 0 {
		if err := s.config.Store.Backend.Capabilities().Require("row_lock_of"); err != nil {
			return nil, err
		}
	}
	if err := deletionDescriptor(s.schema); err != nil {
		return nil, err
	}
	if options.RequireMatch && !s.config.EntityTags {
		return nil, errors.New("api: required If-Match needs resource entity tags")
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.MaxObjects == 0 {
		options.MaxObjects = 1000
	}
	if options.MaxWork == 0 {
		options.MaxWork = options.MaxObjects * 32
	}
	if options.Timeout < time.Millisecond || options.Timeout > 5*time.Minute || options.MaxObjects < 1 || options.MaxObjects > 10000 || options.MaxWork < options.MaxObjects || options.MaxWork > 1000000 {
		return nil, errors.New("api: invalid deletion limits")
	}
	// Handler registration snapshots structural dependencies and hooks. A
	// callback cannot redirect this operation through later Store mutation.
	resource := *s
	store := *s.config.Store
	registry := &models.Registry{}
	for _, schema := range store.Registry.All() {
		if store.Registry.IsAutomatic(schema.Key()) {
			continue
		}
		if err := registry.Register(schema); err != nil {
			return nil, err
		}
	}
	if err := registry.Freeze(); err != nil {
		return nil, err
	}
	store.Registry = registry
	store.BeforeDelete = slices.Clone(store.BeforeDelete)
	store.AfterDelete = slices.Clone(store.AfterDelete)
	resource.config.Store = &store
	resource.schema = s.schema.Clone()
	return mutationHandler([]string{"DELETE"}, 1, options.CSRF, func(r *http.Request) (IdempotencyResult, error) { return resource.deleteRequest(r, key, options) })
}

func (s *Resource) deleteRequest(r *http.Request, decode func(*http.Request) (Values, error), options DeleteOptions) (result IdempotencyResult, err error) {
	result.Outcome = MutationUnchanged
	defer func() {
		if recover() != nil {
			result = IdempotencyResult{Outcome: MutationUnchanged}
			err = ghttp.ErrUnavailable
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), options.Timeout)
	defer cancel()
	r = r.Clone(ctx)
	_, err = s.requestAction(r, "delete")
	if err != nil {
		// Request-level grant denials remain 401/403, unlike hidden object
		// decisions. Incomplete operational checks must still report failure.
		if deletionErrorKind(err, 0, new(int)) != 1 {
			err = deletePublicError(err)
		}
		return result, err
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength > 0 || len(r.TransferEncoding) > 0 || len(r.PostForm) > 0 || len(r.Header.Values("Content-Encoding")) > 0 || len(r.Header.Values("Idempotency-Key")) > 0 || len(r.Header.Values("If-None-Match")) > 0 || len(r.Header.Values("If-Unmodified-Since")) > 0 || len(r.Header.Values("If-Modified-Since")) > 0 {
		return result, mediaError(400, "INVALID_DELETE", "Unsupported deletion body, query, precondition or operation key")
	}
	// net/http supplies NoBody for an absent payload. Never probe an arbitrary
	// reader: a chunked or middleware-owned body can otherwise block without a
	// bound, and this endpoint has no body contract to interpret.
	if r.ContentLength != 0 || (r.Body != nil && r.Body != http.NoBody) {
		return result, mediaError(400, "INVALID_DELETE", "Deletion does not accept a body")
	}
	condition, err := parseIfMatch(r.Header)
	if err != nil {
		return result, err
	}
	if condition.present && !s.config.EntityTags {
		return result, mediaError(400, "INVALID_PRECONDITION", "Entity tag conditions are not enabled")
	}
	if options.RequireMatch && !condition.present {
		return result, mediaError(428, "PRECONDITION_REQUIRED", "If-Match is required")
	}
	key, err := decode(r.Clone(ctx))
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if deletionErrorKind(err, 0, new(int)) == 4 {
		return result, ghttp.ErrNotFound
	}
	if err != nil {
		return result, deletePublicError(err)
	}
	key, err = s.cleanUpdateKey(ctx, key)
	if err != nil {
		return result, err
	}
	result, err = durableResponse(ctx, s.config.Store.Backend, func(ctx context.Context) (MutationResponse, error) {
		return s.deleteModel(ctx, key, condition, options)
	})
	return result, deletePublicError(err)
}

func (s *Resource) deleteModel(ctx context.Context, key Values, condition entityCondition, options DeleteOptions) (MutationResponse, error) {
	root, err := s.receiptRecord(ctx, key)
	if err != nil {
		return MutationResponse{}, err
	}
	policyKey, err := recordIdentity(root)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := readOnlyRecord(root, func() error {
		return s.config.Policy.Authorize(ctx, auth.FromContext(ctx), "delete", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name, ID: policyKey, Object: root})
	}); err != nil {
		return MutationResponse{}, deletePublicError(err)
	}
	if condition.present && !condition.any {
		var value Values
		if err := readOnlyRecord(root, func() error {
			var err error
			value, err = s.representAction(resourceRequest{ctx: ctx}, root, "delete", true)
			return err
		}); err != nil {
			return MutationResponse{}, err
		}
		tag, err := representationTag(value)
		if err != nil {
			return MutationResponse{}, err
		}
		if !condition.matches(tag) {
			return MutationResponse{}, mediaError(412, "PRECONDITION_FAILED", "Resource representation changed")
		}
	}
	var original orm.DeletionPlan
	collector := orm.DeleteCollector{Store: s.config.Store, MaxObjects: options.MaxObjects, MaxWork: options.MaxWork,
		Scope: func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
			return s.config.Scope(ctx, auth.FromContext(ctx), schema)
		},
		Authorize: func(ctx context.Context, plan orm.DeletionPlan) error {
			// This detached graph is coordinator-owned. Every application policy
			// receives a second detached view, never this retained evidence.
			original = plan
			_, err := s.checkDeleteGraph(ctx, plan, options)
			return err
		},
	}
	if _, err := collector.Execute(ctx, root); err != nil {
		return MutationResponse{}, deletePublicError(err)
	}
	if len(original.Objects) == 0 {
		return MutationResponse{}, ghttp.ErrUnavailable
	}
	// Audit owns a detached identity, not the fence's key map.
	auditKey, err := receiptObject(key)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := options.Audit(ctx, MutationEvent{Action: "delete", ObjectKey: auditKey}); err != nil {
		return MutationResponse{}, err
	}
	targets, err := s.checkDeleteGraph(ctx, original, options)
	if err != nil {
		return MutationResponse{}, deletePublicError(err)
	}
	if err := s.fenceDeletion(ctx, original, targets); err != nil {
		return MutationResponse{}, err
	}
	return MutationResponse{Status: 204, ObjectKey: key}, ctx.Err()
}

func deletePublicError(err error) error {
	// Extract the operational cause before a general error mapper can mistake
	// a joined permission sentinel for a completed authorization decision.
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var provider *db.Error
	if errors.As(err, &provider) {
		// Provider causes can themselves wrap authorization sentinels. Only the
		// stable database code determines this public operational response.
		return ghttp.PublicError(&db.Error{Code: provider.Code})
	}
	switch deletionErrorKind(err, 0, new(int)) {
	case 1:
		return ghttp.ErrNotFound
	case 2:
		return mediaError(409, "DELETE_PROTECTED", "A relationship prevents deletion")
	case 3:
		return mediaError(409, "DELETE_LIMIT", "Deletion exceeds configured graph limits")
	}
	if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, orm.ErrNotFound) || errors.Is(err, orm.ErrProtectedRelation) || errors.Is(err, orm.ErrDeleteLimit) {
		return ghttp.ErrUnavailable
	}
	return err
}

// A joined outage/cancellation is not a completed denial, protection or limit
// check. Preserve the full operational error for the public error boundary.
func deletionErrorKind(err error, depth int, nodes *int) int {
	*nodes++
	if err == nil || depth > 64 || *nodes > 1024 {
		return 0
	}
	switch err {
	case auth.ErrPermissionDenied, auth.ErrUnauthenticated, orm.ErrNotFound:
		return 1
	case orm.ErrProtectedRelation:
		return 2
	case orm.ErrDeleteLimit:
		return 3
	case ErrInvalidKey:
		return 4
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		kind := 0
		for _, child := range joined.Unwrap() {
			current := deletionErrorKind(child, depth+1, nodes)
			if current == 0 || kind != 0 && kind != current {
				return 0
			}
			kind = current
		}
		return kind
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return deletionErrorKind(wrapped.Unwrap(), depth+1, nodes)
	}
	return 0
}
