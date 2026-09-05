package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// CreateIdempotencyOptions explicitly enables durable HTTP operation keys.
// Install IdempotencyMigrations on the SAME backend before serving requests.
type CreateIdempotencyOptions struct {
	// Version identifies this resource mutation contract, not the actor's
	// current grant version. Changing it creates a different operation family.
	Version string
	// Scope is a stable trusted tenant/resource identity. It must be read-only
	// and must not contain the operation key. Actor identity is bound separately.
	Scope func(context.Context, auth.Principal) (string, error)
	// Redact rechecks nested/computed field visibility against the CURRENT
	// object, only removing fields from the original receipt. The resource's
	// output allowlist and AllowField checks additionally constrain every replay.
	// It must be read-only; returning newly computed values is not a replay.
	Redact func(context.Context, auth.Principal, models.Record, Values) (Values, error)
	// Vary includes application-specific mutation arguments supplied by trusted
	// middleware, such as a locale-dependent setting. Method, host, path and
	// parsed JSON are already bound. Do not include authentication credentials.
	// Like Scope, Vary is read-only and must not perform business mutations.
	Vary      func(*http.Request) (Values, error)
	Retention time.Duration
	// Keys are required by default. Optional deliberately permits unkeyed POSTs.
	Optional bool
}

type createIdempotency struct {
	options CreateIdempotencyOptions
	service *Idempotency
	action  string
}

func (s *Resource) createOperations(options CreateOptions) (*createIdempotency, error) {
	if options.Idempotency == nil {
		return nil, nil
	}
	config := *options.Idempotency
	if !validOperationPart(config.Version, 128) || config.Scope == nil || config.Redact == nil {
		return nil, errors.New("api: create idempotency requires version, stable scope and current receipt redaction")
	}
	state := &createIdempotency{options: config, action: "create:" + operationDigest([]string{s.schema.Key(), config.Version})}
	permit := func(ctx context.Context, scope, action string, key Values) error {
		p := auth.FromContext(ctx)
		currentScope, err := config.Scope(ctx, p)
		if err != nil {
			return err
		}
		if scope != currentScope || action != state.action {
			return auth.ErrPermissionDenied
		}
		if err := s.config.Policy.Authorize(ctx, p, "add", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name}); err != nil {
			return err
		}
		if key == nil {
			return ctx.Err()
		}
		row, err := s.receiptRecord(ctx, key)
		if err != nil {
			return err
		}
		before, err := recordSnapshot(row)
		if err != nil {
			return err
		}
		if err := readOnlyRecord(row, func() error {
			if err := options.ValidateWrite(ctx, p, row); err != nil {
				return err
			}
			return s.config.Policy.Authorize(ctx, p, "add", auth.Resource{App: s.schema.AppLabel, Model: s.schema.Name, ID: key, Object: row})
		}); err != nil {
			return err
		}
		final, err := s.receiptRecord(ctx, key)
		if err != nil {
			return err
		}
		after, err := recordSnapshot(final)
		if err != nil || before != after {
			return auth.ErrPermissionDenied
		}
		return ctx.Err()
	}
	service, err := NewIdempotency(IdempotencyConfig{Backend: s.config.Store.Backend, Retention: config.Retention, Timeout: options.Timeout, Authorize: permit, Redact: func(ctx context.Context, scope, action string, key, body Values) (Values, error) {
		if err := permit(ctx, scope, action, key); err != nil {
			return nil, err
		}
		row, err := s.receiptRecord(ctx, key)
		if err != nil {
			return nil, err
		}
		before, err := recordSnapshot(row)
		if err != nil {
			return nil, err
		}
		var filtered Values
		err = readOnlyRecord(row, func() error {
			p := auth.FromContext(ctx)
			filtered, err = config.Redact(ctx, p, row, body)
			if err != nil {
				return err
			}
			// Copy only currently exposed fields. Nested removal remains the
			// explicit receipt policy's responsibility, never an implicit fetch.
			allowed := Values{}
			for _, field := range s.config.Serializer.fields {
				if field.Hidden || field.WriteOnly {
					continue
				}
				if s.config.AllowField != nil {
					visible, err := s.config.AllowField(ctx, p, row, field.Name)
					if err != nil {
						return err
					}
					if !visible {
						continue
					}
				}
				if value, found := filtered[field.Name]; found {
					allowed[field.Name] = value
				}
			}
			filtered = allowed
			return nil
		})
		if err != nil {
			return nil, err
		}
		// Receipt authorization/redaction runs after the create pipeline too.
		// A nested write to a related target must not evade its final scope
		// check merely because the created root record stayed unchanged.
		if err := s.validateCreateRelations(ctx, row); err != nil {
			return nil, err
		}
		final, err := s.receiptRecord(ctx, key)
		if err != nil {
			return nil, err
		}
		after, err := recordSnapshot(final)
		if err != nil || before != after {
			return nil, auth.ErrPermissionDenied
		}
		return filtered, ctx.Err()
	}})
	if err != nil {
		return nil, err
	}
	state.service = service
	return state, nil
}

func (s *Resource) receiptRecord(ctx context.Context, key Values) (*models.MapRecord, error) {
	fields := s.schema.PKFields()
	if len(key) != len(fields) {
		return nil, ghttp.ErrNotFound
	}
	query := orm.For(s.config.Store, func() *models.MapRecord { row, _ := models.NewRecord(s.schema); return row }).WithScope(func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		return s.config.Scope(ctx, auth.FromContext(ctx), schema)
	})
	for _, field := range fields {
		value, present := key[field.Name]
		if !present || value == nil {
			return nil, ghttp.ErrNotFound
		}
		value, err := cleanQueryValue(ctx, field, value)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ghttp.ErrNotFound
		}
		query = query.Filter(orm.Q(field.Name, value))
	}
	return query.SelectForUpdate(false, false).Get(ctx)
}

func (state *createIdempotency) execute(r *http.Request, ctx context.Context, input Values, mutate func(context.Context) (MutationResponse, error)) (IdempotencyResult, error) {
	key := r.Header.Values("Idempotency-Key")
	if len(key) != 1 || key[0] == "" {
		return IdempotencyResult{Outcome: MutationUnchanged}, mediaError(400, "INVALID_OPERATION", "One nonempty Idempotency-Key is required")
	}
	operationKey := key[0]
	method, host, path := r.Method, r.Host, r.URL.EscapedPath()
	scope, err := state.options.Scope(ctx, auth.FromContext(ctx))
	if err != nil {
		return IdempotencyResult{Outcome: MutationUnchanged}, err
	}
	var vary Values
	if state.options.Vary != nil {
		vary, err = state.options.Vary(r.WithContext(ctx))
		if err != nil {
			return IdempotencyResult{Outcome: MutationUnchanged}, err
		}
	}
	arguments := Values{"method": method, "host": host, "path": path, "body": input, "vary": vary}
	return state.service.Execute(ctx, Operation{Scope: scope, Action: state.action, Key: operationKey, Input: arguments}, mutate)
}
