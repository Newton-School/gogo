package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type UpdateOptions struct {
	Input   *Serializer
	Factory func() models.Model
	// Prepare may set server-owned proposed values after input binding. Neither
	// preparation nor save hooks may retarget the locked primary key.
	Prepare func(context.Context, models.Record) error
	// ValidateWrite is read-only and checks the fully prepared proposed row,
	// then its persisted state before response/audit commit and receipt replay.
	ValidateWrite func(context.Context, auth.Principal, models.Record) error
	Audit         func(context.Context, MutationEvent) error
	MaxBytes      int64
	Timeout       time.Duration
	CSRF          *security.CSRFConfig
	Idempotency   *MutationIdempotencyOptions
	// RequireMatch requires an If-Match condition and Resource.EntityTags.
	// The standard '*' condition checks existence, not a particular revision.
	RequireMatch bool
}

// UpdateHandler exposes explicit PUT/PATCH routes; Resource.Routes remains
// read-only. This path accepts stored scalar/JSON/to-one fields, never implicit
// nested, file, collection, inheritance or primary-key updates.
func (s *Resource) UpdateHandler(key func(*http.Request) (Values, error), options UpdateOptions) (http.Handler, error) {
	if s == nil || key == nil || options.Input == nil || options.Factory == nil || options.ValidateWrite == nil || options.Audit == nil {
		return nil, errors.New("api: update requires key decoder, input, typed factory, write policy and atomic audit")
	}
	if err := s.config.Store.Backend.Capabilities().Require("transactions", "savepoints", "row_locks"); err != nil {
		return nil, err
	}
	if len(s.config.SelectRelated) > 0 {
		if err := s.config.Store.Backend.Capabilities().Require("row_lock_of"); err != nil {
			return nil, err
		}
	}
	if s.schema.Parent != "" || s.schema.Unmanaged || s.schema.Proxy {
		return nil, errors.New("api: generic update requires a managed concrete model")
	}
	if options.RequireMatch && !s.config.EntityTags {
		return nil, errors.New("api: required If-Match needs resource entity tags")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 1 << 20
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.MaxBytes < 1 || options.MaxBytes > 10<<20 || options.Timeout < time.Millisecond || options.Timeout > 5*time.Minute {
		return nil, errors.New("api: invalid update limits")
	}
	keys := map[string]bool{}
	for _, field := range s.schema.PKFields() {
		keys[field.Name] = true
	}
	for _, field := range options.Input.fields {
		if field.ReadOnly {
			continue
		}
		model, found := s.schema.Field(field.Source)
		if !found || !model.IsStored() || keys[field.Source] || model.Kind == models.Generated || model.IsAuto() || field.Nested != nil || field.Element != nil || model.Kind == models.File || model.Kind == models.Image {
			return nil, errors.New("api: update input requires explicit writable stored scalar or JSON fields without primary keys")
		}
	}
	if _, _, err := s.freshModel(options.Factory); err != nil {
		return nil, err
	}
	operations, err := s.newOperations(options.Idempotency, "update", "change", options.Timeout, options.ValidateWrite)
	if err != nil {
		return nil, err
	}
	return mutationHandler([]string{"PUT", "PATCH"}, options.MaxBytes, options.CSRF, func(r *http.Request) (IdempotencyResult, error) { return s.updateRequest(r, key, options, operations) })
}

func (s *Resource) updateRequest(r *http.Request, decode func(*http.Request) (Values, error), options UpdateOptions, operations *resourceOperations) (result IdempotencyResult, err error) {
	result.Outcome = MutationUnchanged
	defer func() {
		if recover() != nil {
			result = IdempotencyResult{Outcome: MutationUnchanged}
			err = ghttp.ErrUnavailable
		}
	}()
	request, err := s.requestAction(r, "change")
	if err != nil {
		return result, err
	}
	if r.URL.RawQuery != "" || operations == nil && len(r.Header.Values("Idempotency-Key")) != 0 || len(r.Header.Values("If-None-Match")) != 0 || len(r.Header.Values("If-Unmodified-Since")) != 0 || len(r.Header.Values("If-Modified-Since")) != 0 {
		return result, mediaError(400, "INVALID_UPDATE", "Unsupported query, precondition or operation-key policy")
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
	if len(r.Header.Values("Content-Type")) != 1 || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return result, mediaError(415, "UNSUPPORTED_MEDIA_TYPE", "This endpoint accepts JSON")
	}
	parsed, err := Parse(r, ParseOptions{MaxBytes: options.MaxBytes})
	if err != nil {
		return result, err
	}
	defer parsed.Close()
	ctx, cancel := context.WithTimeout(request.ctx, options.Timeout)
	defer cancel()
	key, err := decode(r.Clone(ctx))
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(err, ErrInvalidKey) {
		return result, ghttp.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	key, err = s.cleanUpdateKey(ctx, key)
	if err != nil {
		return result, err
	}
	mutate := func(ctx context.Context) (MutationResponse, error) {
		return s.updateModel(ctx, key, parsed.Values, r.Method == "PATCH", condition, options)
	}
	if operations != nil && (!operations.options.Optional || len(r.Header.Values("Idempotency-Key")) != 0) {
		return operations.executeArguments(r, ctx, parsed.Values, Values{"key": key, "if_match": r.Header.Values("If-Match")}, mutate)
	}
	return durableResponse(ctx, s.config.Store.Backend, mutate)
}

func (s *Resource) cleanUpdateKey(ctx context.Context, input Values) (Values, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fields := s.schema.PKFields()
	if len(input) != len(fields) {
		return nil, ghttp.ErrNotFound
	}
	key := Values{}
	for _, field := range fields {
		value, found := input[field.Name]
		if !found || value == nil {
			return nil, ghttp.ErrNotFound
		}
		value, err := cleanQueryValue(ctx, field, value)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, ghttp.ErrNotFound
		}
		key[field.Name] = value
	}
	return receiptObject(key)
}

func (s *Resource) updateModel(ctx context.Context, key, input Values, partial bool, condition entityCondition, options UpdateOptions) (MutationResponse, error) {
	current, err := s.receiptRecord(ctx, key)
	if err != nil {
		return MutationResponse{}, err
	}
	original, err := recordIdentity(current)
	if err != nil {
		return MutationResponse{}, err
	}
	before, err := recordSnapshot(current)
	if err != nil {
		return MutationResponse{}, err
	}
	err = readOnlyRecord(current, func() error {
		return s.config.Policy.Authorize(ctx, auth.FromContext(ctx), "change", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name, ID: original, Object: current})
	})
	if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, auth.ErrUnauthenticated) {
		return MutationResponse{}, ghttp.ErrNotFound
	}
	if err != nil {
		return MutationResponse{}, err
	}
	if condition.present && !condition.any {
		var value Values
		err := readOnlyRecord(current, func() error {
			var err error
			value, err = s.representAction(resourceRequest{ctx: ctx}, current, "change", true)
			return err
		})
		if err != nil {
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
	model, proposed, err := s.freshModel(options.Factory)
	if err != nil {
		return MutationResponse{}, err
	}
	for _, field := range s.schema.Fields {
		if !field.IsStored() {
			continue
		}
		value, err := current.Get(field.Name)
		if err != nil {
			return MutationResponse{}, err
		}
		if err := proposed.Set(field.Name, value); err != nil {
			return MutationResponse{}, err
		}
	}
	proposed.State().Persisted, proposed.State().Database = true, s.config.Store.Backend.Alias()
	values, err := options.Input.Validate(ctx, input, BindOptions{Partial: partial})
	if err != nil {
		return MutationResponse{}, err
	}
	for _, name := range sortedInputNames(values) {
		if err := proposed.Set(name, values[name]); err != nil {
			return MutationResponse{}, err
		}
	}
	if options.Prepare != nil {
		if err := options.Prepare(ctx, proposed); err != nil {
			return MutationResponse{}, err
		}
	}
	checkIdentity := func(record models.Record) error {
		identity, err := recordIdentity(record)
		if err != nil || !reflect.DeepEqual(identity, original) || !record.State().Persisted || record.State().Database != s.config.Store.Backend.Alias() {
			return auth.ErrPermissionDenied
		}
		return nil
	}
	if err := checkIdentity(proposed); err != nil {
		return MutationResponse{}, err
	}
	store := *s.config.Store
	persisted := ""
	store.AfterSave = append([]orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if err := checkIdentity(event.Record); err != nil {
			return err
		}
		row, err := s.receiptRecord(ctx, original)
		if err != nil {
			return err
		}
		persisted, err = recordSnapshot(row)
		return err
	}}, store.AfterSave...)
	if err := store.Save(ctx, model, orm.SaveOptions{ForceUpdate: true, Prepare: func(ctx context.Context, row models.Record) error {
		if err := checkIdentity(row); err != nil {
			return err
		}
		if err := s.validateCreateRelations(ctx, row); err != nil {
			return err
		}
		return models.FullClean(ctx, row, models.CleanOptions{}, &store)
	}, Guard: func(ctx context.Context, row models.Record) error {
		if err := checkIdentity(row); err != nil {
			return err
		}
		if err := s.validateCreateRelations(ctx, row); err != nil {
			return err
		}
		if err := readOnlyRecord(row, func() error {
			if err := options.ValidateWrite(ctx, auth.FromContext(ctx), row); err != nil {
				return err
			}
			return s.config.Policy.Authorize(ctx, auth.FromContext(ctx), "change", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name, ID: original, Object: row})
		}); err != nil {
			return err
		}
		if err := checkIdentity(row); err != nil {
			return err
		}
		// A nested callback write must not be silently overwritten by this
		// UPDATE and thereby escape current-state/concurrency checks.
		return s.matchStoredSnapshot(ctx, original, before)
	}}); err != nil {
		return MutationResponse{}, err
	}
	if err := checkIdentity(proposed); err != nil {
		return MutationResponse{}, err
	}
	if persisted == "" {
		return MutationResponse{}, ghttp.ErrUnavailable
	}
	if err := s.matchStoredSnapshot(ctx, original, persisted); err != nil {
		return MutationResponse{}, err
	}
	fresh, err := s.receiptRecord(ctx, original)
	if err != nil {
		return MutationResponse{}, err
	}
	var output Values
	err = readOnlyRecord(fresh, func() error {
		var err error
		output, err = s.representAction(resourceRequest{ctx: ctx}, fresh, "change", false)
		return err
	})
	if err != nil {
		return MutationResponse{}, err
	}
	// Generic PUT may normalize input. Do not claim its incoming bytes were
	// stored unchanged by returning a validator; read detail for a fresh tag.
	response, err := sealMutationResponse(MutationResponse{Status: 200, Body: output, ObjectKey: original})
	if err != nil {
		return MutationResponse{}, err
	}
	auditKey, err := receiptObject(original)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := options.Audit(ctx, MutationEvent{Action: "change", ObjectKey: auditKey, Fields: sortedInputNames(values)}); err != nil {
		return MutationResponse{}, err
	}
	if err := s.matchStoredSnapshot(ctx, original, persisted); err != nil {
		return MutationResponse{}, err
	}
	final, err := s.receiptRecord(ctx, original)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := readOnlyRecord(final, func() error { return options.ValidateWrite(ctx, auth.FromContext(ctx), final) }); err != nil {
		return MutationResponse{}, err
	}
	if err := s.validateCreateRelations(ctx, final); err != nil {
		return MutationResponse{}, err
	}
	if err := s.matchStoredSnapshot(ctx, original, persisted); err != nil {
		return MutationResponse{}, err
	}
	return response, ctx.Err()
}

func (s *Resource) matchStoredSnapshot(ctx context.Context, key Values, expected string) error {
	row, err := s.receiptRecord(ctx, key)
	if err != nil {
		return err
	}
	actual, err := recordSnapshot(row)
	if err != nil {
		return err
	}
	if actual != expected {
		return auth.ErrPermissionDenied
	}
	return ctx.Err()
}
