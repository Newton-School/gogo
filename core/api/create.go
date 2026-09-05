package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

// MutationEvent deliberately contains no field values, credentials or body.
// Audit must persist in the supplied transaction on the resource backend.
type MutationEvent struct {
	Action    string
	ObjectKey Values
	Fields    []string
}

type CreateOptions struct {
	Input *Serializer
	// Factory returns a NEW typed model matching the registered schema. Its
	// model Clean method and the Store's save hooks remain active.
	Factory func() models.Model
	// Prepare supplies server-owned defaults/ownership after serializer binding.
	Prepare func(context.Context, models.Record) error
	// ValidateWrite is a mandatory read-only policy for the fully prepared
	// proposed record. It runs after model hooks/defaults, before INSERT.
	ValidateWrite func(context.Context, auth.Principal, models.Record) error
	Audit         func(context.Context, MutationEvent) error
	Location      func(context.Context, Values) (string, error)
	MaxBytes      int64
	Timeout       time.Duration
	Idempotency   *CreateIdempotencyOptions
	// Nil uses secure cookies. An explicit config may disable Secure for local
	// development, but Exempt is not accepted. Verified scoped bearer identities
	// are exempt; cookie/custom identities always require CSRF protection.
	CSRF *security.CSRFConfig
}

// CreateHandler is an explicit POST endpoint for registration in app urls.go.
// This initial write path accepts bounded JSON and scalar/JSON/explicitly
// resolved to-one serializer fields. Nested/file/M2M persistence requires its
// own later policy; read-only Resource.Routes does not silently expose writes.
func (s *Resource) CreateHandler(options CreateOptions) (http.Handler, error) {
	if s == nil || options.Input == nil || options.Factory == nil || options.ValidateWrite == nil || options.Audit == nil || options.Location == nil {
		return nil, errors.New("api: create requires input, typed factory, write policy, atomic audit and location")
	}
	if err := s.config.Store.Backend.Capabilities().Require("transactions", "savepoints"); err != nil {
		return nil, err
	}
	if s.schema.Parent != "" || s.schema.Unmanaged || s.schema.Proxy {
		return nil, errors.New("api: generic create requires a managed concrete model")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 1 << 20
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.MaxBytes < 1 || options.MaxBytes > 10<<20 || options.Timeout < time.Millisecond || options.Timeout > 5*time.Minute {
		return nil, errors.New("api: invalid create limits")
	}
	for _, field := range options.Input.fields {
		if field.ReadOnly {
			continue
		}
		model, found := s.schema.Field(field.Source)
		if !found || !model.IsStored() || model.Kind == models.Generated || model.IsAuto() || field.Nested != nil || field.Element != nil || model.Kind == models.File || model.Kind == models.Image {
			return nil, errors.New("api: create input requires explicit writable stored scalar or JSON fields")
		}
	}
	for _, field := range s.schema.Fields {
		if field.IsStored() && field.Relation != nil {
			if err := s.config.Store.Backend.Capabilities().Require("row_locks"); err != nil {
				return nil, err
			}
		}
	}
	if _, _, err := s.freshModel(options.Factory); err != nil {
		return nil, err
	}
	operations, err := s.createOperations(options)
	if err != nil {
		return nil, err
	}
	csrf := security.CSRFConfig{Secure: true, MaxBodyBytes: options.MaxBytes}
	if options.CSRF != nil {
		csrf = *options.CSRF
		csrf.TrustedOrigins = slices.Clone(csrf.TrustedOrigins)
		if csrf.Exempt != nil {
			return nil, errors.New("api: create CSRF exemptions are authentication-owned")
		}
		csrf.MaxBodyBytes = options.MaxBytes
	}
	csrf.Exempt = func(r *http.Request) bool {
		p := auth.FromContext(r.Context())
		_, scoped := p.TokenScopes()
		return scoped && p.Authenticated && p.Active
	}
	protect, err := security.CSRF(csrf)
	if err != nil {
		return nil, err
	}
	handler := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := s.createRequest(r, options, operations)
		w.Header().Set("X-Gogo-Mutation", string(result.Outcome))
		if err != nil {
			public := publicMutationError(err)
			if result.Outcome == MutationUnknown {
				public = mediaError(503, "MUTATION_UNKNOWN", "Mutation outcome is unknown; reconcile before retry")
			}
			ghttp.WriteError(w, r, public)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		response, err := ghttp.JSON(result.Response.Status, result.Response.Body)
		if err != nil {
			ghttp.WriteError(w, r, ghttp.ErrUnavailable)
			return
		}
		for name, value := range result.Response.Headers {
			response.Headers.Set(name, value)
		}
		_ = response.Write(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", "Accept, Authorization, Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			ghttp.WriteError(w, r, mediaError(405, "METHOD_NOT_ALLOWED", "Method not allowed"))
			return
		}
		principal := auth.FromContext(r.Context())
		if !principal.Authenticated {
			ghttp.WriteError(w, r, auth.ErrUnauthenticated)
			return
		}
		if !principal.Active {
			ghttp.WriteError(w, r, auth.ErrPermissionDenied)
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}

func (s *Resource) freshModel(factory func() models.Model) (models.Model, models.Record, error) {
	model := factory()
	record, err := models.Bind(model)
	if err != nil {
		return nil, nil, err
	}
	want, err := s.schema.Fingerprint()
	if err != nil {
		return nil, nil, err
	}
	got, err := record.Schema().Fingerprint()
	if err != nil || want != got || record.State().Persisted {
		return nil, nil, errors.New("api: factory must return a fresh model matching the resource schema")
	}
	return model, record, nil
}

func (s *Resource) createRequest(r *http.Request, options CreateOptions, operations *createIdempotency) (result IdempotencyResult, err error) {
	result.Outcome = MutationUnchanged
	defer func() {
		if recover() != nil {
			result = IdempotencyResult{Outcome: MutationUnchanged}
			err = ghttp.ErrUnavailable
		}
	}()
	request, err := s.requestAction(r, "add")
	if err != nil {
		return result, err
	}
	if r.URL.RawQuery != "" || operations == nil && len(r.Header.Values("Idempotency-Key")) != 0 || len(r.Header.Values("If-Match")) != 0 || len(r.Header.Values("If-None-Match")) != 0 {
		return result, mediaError(400, "INVALID_CREATE", "Unsupported query, precondition or operation-key policy")
	}
	// JSON avoids ambiguous multipart hashes and CSRF's form-body consumption.
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
	mutate := func(ctx context.Context) (MutationResponse, error) {
		return s.createModel(ctx, parsed.Values, options)
	}
	if operations != nil && (!operations.options.Optional || len(r.Header.Values("Idempotency-Key")) != 0) {
		return operations.execute(r, ctx, parsed.Values, mutate)
	}
	return durableResponse(ctx, s.config.Store.Backend, mutate)
}

func recordIdentity(record models.Record) (Values, error) {
	values := Values{}
	for _, field := range record.Schema().PKFields() {
		value, err := record.Get(field.Name)
		if err != nil {
			return nil, err
		}
		values[field.Name] = value
	}
	return values, nil
}
func recordSnapshot(record models.Record) (string, error) {
	values := Values{}
	for _, field := range record.Schema().Fields {
		if field.IsStored() {
			value, err := record.Get(field.Name)
			if err != nil {
				return "", err
			}
			values[field.Name] = value
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", errors.New("api: model snapshot cannot be encoded")
	}
	return operationDigest(string(encoded)), nil
}
func readOnlyRecord(record models.Record, fn func() error) error {
	before, err := recordSnapshot(record)
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	after, err := recordSnapshot(record)
	if err != nil || before != after {
		return auth.ErrPermissionDenied
	}
	return nil
}

func (s *Resource) createModel(ctx context.Context, input Values, options CreateOptions) (MutationResponse, error) {
	model, record, err := s.freshModel(options.Factory)
	if err != nil {
		return MutationResponse{}, err
	}
	values, err := options.Input.Validate(ctx, input, BindOptions{})
	if err != nil {
		return MutationResponse{}, err
	}
	for _, name := range sortedInputNames(values) {
		if err := record.Set(name, values[name]); err != nil {
			return MutationResponse{}, err
		}
	}
	if options.Prepare != nil {
		if err := options.Prepare(ctx, record); err != nil {
			return MutationResponse{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return MutationResponse{}, err
	}
	var inserted Values
	store := *s.config.Store
	// Capture RETURNING identity before application after-save hooks can alter
	// the in-memory model. Audits and response lookups must target that INSERT.
	store.AfterSave = append([]orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		var err error
		inserted, err = recordIdentity(event.Record)
		return err
	}}, store.AfterSave...)
	guard := func(ctx context.Context, proposed models.Record) error {
		// Defaults and trusted preparation may supply a callable/UUID primary
		// key. Freeze that first INSERT candidate, not the empty factory value.
		identity, err := recordIdentity(proposed)
		if err != nil {
			return err
		}
		if err := s.validateCreateRelations(ctx, proposed); err != nil {
			return err
		}
		if err := readOnlyRecord(proposed, func() error { return options.ValidateWrite(ctx, auth.FromContext(ctx), proposed) }); err != nil {
			return err
		}
		if err := readOnlyRecord(proposed, func() error {
			return s.config.Policy.Authorize(ctx, auth.FromContext(ctx), "add", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name, ID: identity, Object: proposed})
		}); err != nil {
			return err
		}
		current, err := recordIdentity(proposed)
		if err != nil || !reflect.DeepEqual(current, identity) {
			return auth.ErrPermissionDenied
		}
		return ctx.Err()
	}
	if err := store.Save(ctx, model, orm.SaveOptions{ForceInsert: true, Prepare: func(ctx context.Context, proposed models.Record) error {
		// Scope before unscoped model existence validation so a hidden and a
		// missing foreign key have the same public failure. Guard rechecks any
		// relation changed by the typed model's subsequent Clean method.
		if err := s.validateCreateRelations(ctx, proposed); err != nil {
			return err
		}
		return models.FullClean(ctx, proposed, models.CleanOptions{}, &store)
	}, Guard: guard}); err != nil {
		return MutationResponse{}, err
	}
	current, err := recordIdentity(record)
	if err != nil || inserted == nil || !reflect.DeepEqual(current, inserted) {
		return MutationResponse{}, auth.ErrPermissionDenied
	}
	query := orm.For(s.config.Store, func() *models.MapRecord { row, _ := models.NewRecord(s.schema); return row }).WithScope(func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		return s.config.Scope(ctx, auth.FromContext(ctx), schema)
	})
	for _, name := range sortedInputNames(inserted) {
		query = query.Filter(orm.Q(name, inserted[name]))
	}
	fresh, err := query.Get(ctx)
	if err != nil {
		return MutationResponse{}, err
	}
	before, err := recordSnapshot(fresh)
	if err != nil {
		return MutationResponse{}, err
	}
	var output Values
	err = readOnlyRecord(fresh, func() error {
		var err error
		output, err = s.representAction(resourceRequest{ctx: ctx}, fresh, "add", false)
		return err
	})
	if err != nil {
		return MutationResponse{}, err
	}
	key, err := receiptObject(inserted)
	if err != nil {
		return MutationResponse{}, err
	}
	location, err := options.Location(ctx, key)
	if err != nil {
		return MutationResponse{}, err
	}
	response, err := sealMutationResponse(MutationResponse{Status: 201, Body: output, ObjectKey: inserted, Headers: map[string]string{"Location": location}})
	if err != nil {
		return MutationResponse{}, err
	}
	key, err = receiptObject(inserted)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := options.Audit(ctx, MutationEvent{Action: "add", ObjectKey: key, Fields: sortedInputNames(values)}); err != nil {
		return MutationResponse{}, err
	}
	// Audit/representation extension points cannot replace the business state
	// after the authorized response was sealed, even via a nested domain call.
	after, err := query.Get(ctx)
	if err != nil {
		return MutationResponse{}, err
	}
	snapshot, err := recordSnapshot(after)
	if err != nil || snapshot != before {
		return MutationResponse{}, auth.ErrPermissionDenied
	}
	if err := readOnlyRecord(after, func() error { return options.ValidateWrite(ctx, auth.FromContext(ctx), after) }); err != nil {
		return MutationResponse{}, err
	}
	final, err := query.Get(ctx)
	if err != nil {
		return MutationResponse{}, err
	}
	snapshot, err = recordSnapshot(final)
	if err != nil || snapshot != before {
		return MutationResponse{}, auth.ErrPermissionDenied
	}
	if err := s.validateCreateRelations(ctx, final); err != nil {
		return MutationResponse{}, err
	}
	return response, ctx.Err()
}

// Recheck every to-one target after hooks against the same mandatory target
// scope as reads. Lock its row so a concurrent ownership transfer cannot race
// the reference write. An earlier serializer choice check is not sufficient.
func (s *Resource) validateCreateRelations(ctx context.Context, record models.Record) error {
	for _, field := range record.Schema().Fields {
		if !field.IsStored() || field.Relation == nil {
			continue
		}
		value, err := record.Get(field.Name)
		if err != nil {
			return err
		}
		if value == nil {
			continue
		}
		target, found := s.config.Store.Registry.Get(field.Relation.Target)
		if !found {
			return errors.New("api: relation target is not registered")
		}
		keys := slices.Clone(field.Relation.TargetFields)
		if len(keys) == 0 {
			for _, key := range target.PKFields() {
				keys = append(keys, key.Name)
			}
		}
		if len(keys) != 1 {
			return errors.New("api: scalar relation requires one target key")
		}
		query := orm.For(s.config.Store, func() *models.MapRecord { row, _ := models.NewRecord(target); return row }).WithScope(func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
			return s.config.Scope(ctx, auth.FromContext(ctx), schema)
		})
		_, err = query.Filter(orm.Q(keys[0], value)).SelectForUpdate(false, false).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return &ValidationError{Fields: map[string][]models.FieldError{field.Name: {{Code: "invalid_choice", Message: "Related object is unavailable."}}}}
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func sortedInputNames(values Values) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// durableResponse confirms this outer commit, never a typed error from an
// unrelated transaction. It seals before commit and never retries callbacks.
func durableResponse(ctx context.Context, backend db.Backend, mutate func(context.Context) (MutationResponse, error)) (result IdempotencyResult, err error) {
	result.Outcome = MutationUnchanged
	var response MutationResponse
	committed, prepared := false, false
	defer func() {
		if recover() != nil {
			if committed && prepared {
				result.Outcome, result.Response = MutationCommitted, response
				err = mediaError(503, "COMMITTED_CALLBACK_FAILED", "Mutation committed but an after-commit callback failed")
			} else {
				result = IdempotencyResult{Outcome: MutationUnknown}
				err = mediaError(503, "MUTATION_UNKNOWN", "Mutation outcome is unknown; reconcile before retry")
			}
		}
	}()
	err = db.Atomic(ctx, backend, db.AtomicOptions{Durable: true}, func(ctx context.Context) (err error) {
		defer rollbackMutationPanic(&err)
		if err := db.OnCommit(ctx, backend.Alias(), func(context.Context) error { committed = true; return nil }, false); err != nil {
			return err
		}
		response, err = mutate(ctx)
		if err != nil {
			return err
		}
		response, err = sealMutationResponse(response)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		prepared = true
		return nil
	})
	if committed && prepared {
		result.Outcome, result.Response = MutationCommitted, response
	} else if prepared && db.IsCode(err, db.UnknownCommit) {
		result.Outcome = MutationUnknown
	}
	return result, err
}

func publicMutationError(err error) error {
	var provider *db.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &provider) {
		return ghttp.PublicError(err)
	}
	var validation *ValidationError
	if errors.As(err, &validation) && mutationValidationOnly(err, 0, new(int)) {
		fields := map[string][]string{}
		for name, items := range validation.Fields {
			for _, item := range items {
				fields[name] = append(fields[name], item.Message)
			}
		}
		return &ghttp.Error{Status: 422, Code: "VALIDATION_ERROR", Message: "Invalid input", Fields: fields}
	}
	var modelValidation *models.ValidationError
	if errors.As(err, &modelValidation) && models.IsValidationOnly(err) {
		fields := map[string][]string{}
		for name, items := range modelValidation.Fields {
			for _, item := range items {
				fields[name] = append(fields[name], item.Message)
			}
		}
		return &ghttp.Error{Status: 422, Code: "VALIDATION_ERROR", Message: "Invalid input", Fields: fields}
	}
	return err
}

// API validation adds its own typed field collection to the model validation
// contract. An arbitrary joined provider error must suppress all 422 fields.
func mutationValidationOnly(err error, depth int, nodes *int) bool {
	*nodes++
	if err == nil || depth > 64 || *nodes > 1024 {
		return false
	}
	switch value := err.(type) {
	case *ValidationError:
		return value != nil
	case interface{ Unwrap() []error }:
		children := value.Unwrap()
		if len(children) == 0 || len(children) > 1024 {
			return false
		}
		for _, child := range children {
			if !mutationValidationOnly(child, depth+1, nodes) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return mutationValidationOnly(value.Unwrap(), depth+1, nodes)
	default:
		return models.IsValidationOnly(err)
	}
}
