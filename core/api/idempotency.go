package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

// Operation includes ALL mutation arguments, including route identities and
// conditional headers, in Input. Scope is a stable tenant/resource scope, not
// an auth-version: changing grants must reauthorize the original operation,
// not silently create another logical mutation with the same client key.
type Operation struct {
	Scope, Action, Key string
	Input              Values
}

func (Operation) String() string               { return "api.Operation{redacted}" }
func (o Operation) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, o.String()) }
func (Operation) MarshalJSON() ([]byte, error) {
	return nil, errors.New("api: operation keys and input cannot be serialized")
}

type MutationOutcome string

const (
	MutationUnchanged MutationOutcome = "unchanged"
	MutationCommitted MutationOutcome = "committed"
	MutationUnknown   MutationOutcome = "unknown"
)

type MutationResponse struct {
	Status int
	// Only Content-Type (JSON), relative Location and ETag are retained.
	Headers   map[string]string
	Body      Values
	ObjectKey Values
}
type IdempotencyResult struct {
	Response MutationResponse
	Replayed bool
	Outcome  MutationOutcome
}

type IdempotencyConfig struct {
	Backend            db.Backend
	Retention, Timeout time.Duration
	// Authorize must use current trusted authority and row scope. It is called
	// first with nil object identity, then with the stored/new identity before
	// disclosing a receipt. The operation key and body are never passed to it.
	Authorize func(context.Context, string, string, Values) error
	// Redact rechecks CURRENT field visibility. It may only remove fields.
	// Both callbacks are read-only and run within the same transaction.
	Redact func(context.Context, string, string, Values, Values) (Values, error)
	Now    func() time.Time
}
type Idempotency struct {
	config IdempotencyConfig
	store  *orm.Store
}

func NewIdempotency(config IdempotencyConfig) (*Idempotency, error) {
	if config.Backend == nil || config.Authorize == nil || config.Redact == nil {
		return nil, errors.New("api: idempotency requires backend and current authorization/redaction policies")
	}
	if err := config.Backend.Capabilities().Require("transactions", "savepoints", "row_locks"); err != nil {
		return nil, err
	}
	if config.Retention == 0 {
		config.Retention = 24 * time.Hour
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Timeout < time.Millisecond || config.Timeout > 5*time.Minute || config.Retention < config.Timeout || config.Retention > 30*24*time.Hour {
		return nil, errors.New("api: invalid idempotency retention or timeout")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	registry := &models.Registry{}
	if err := registry.Register((&IdempotencyRecord{}).Schema()); err != nil {
		return nil, err
	}
	if err := registry.Freeze(); err != nil {
		return nil, err
	}
	// Infrastructure receipts must not trigger application model save hooks.
	return &Idempotency{config: config, store: orm.New(config.Backend, registry)}, nil
}

func operationDigest(value any) string {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
func validOperationPart(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func operationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&15 | 64
	value[8] = value[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

// Execute owns the OUTERMOST transaction. Mutation, audit/outbox and receipt
// must use this context and this backend alias. External effects belong in a
// transactional outbox; this method never retries the mutation callback.
func (s *Idempotency) Execute(ctx context.Context, operation Operation, mutate func(context.Context) (MutationResponse, error)) (result IdempotencyResult, err error) {
	result.Outcome = MutationUnchanged
	var response MutationResponse
	var committed, prepared bool
	defer func() {
		if recover() != nil {
			// A callback could panic after commit; without confirmation do not
			// expose a response or tell the caller it is safe to start a new key.
			if committed && prepared {
				result.Outcome, result.Response = MutationCommitted, response
				err = mediaError(503, "COMMITTED_CALLBACK_FAILED", "Mutation committed but an after-commit callback failed")
			} else {
				result = IdempotencyResult{Outcome: MutationUnknown}
				err = mediaError(503, "MUTATION_UNKNOWN", "Mutation outcome is unknown; retry the same operation key")
			}
		}
	}()
	if s == nil || s.store == nil || mutate == nil {
		return result, errors.New("api: idempotency service and mutation required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	p := auth.FromContext(ctx)
	if !p.Authenticated {
		return result, auth.ErrUnauthenticated
	}
	if !p.Active {
		return result, auth.ErrPermissionDenied
	}
	if !validOperationPart(p.ID, 1024) || !validOperationPart(operation.Scope, 1024) || !validOperationPart(operation.Action, 128) || len(operation.Key) < 1 || len(operation.Key) > 255 {
		return result, mediaError(400, "INVALID_OPERATION", "Invalid operation identity")
	}
	for _, value := range operation.Key {
		if value < 33 || value > 126 {
			return result, mediaError(400, "INVALID_OPERATION", "Invalid operation key")
		}
	}
	if db.InTransaction(ctx, s.store.Backend.Alias()) {
		return result, errors.New("api: idempotent operation must own its transaction")
	}
	input, err := receiptObject(operation.Input)
	if err != nil {
		return result, mediaError(400, "INVALID_OPERATION", "Invalid operation input")
	}
	canonical, err := canonicalOperation(input)
	if err != nil {
		return result, mediaError(400, "INVALID_OPERATION", "Invalid operation input")
	}
	scopeHash := operationDigest([]string{"gogo.api.scope.v1", p.ID, operation.Scope})
	keyDigest := operationDigest([]string{"gogo.api.key.v1", operation.Key})
	requestDigest := operationDigest(canonical)
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	err = db.Atomic(ctx, s.store.Backend, db.AtomicOptions{Durable: true}, func(ctx context.Context) error {
		// Register first: a typed error from an inner/other-alias callback is
		// not proof that THIS outer transaction committed. This marker is.
		if err := db.OnCommit(ctx, s.store.Backend.Alias(), func(context.Context) error { committed = true; return nil }, false); err != nil {
			return err
		}
		if err := s.permit(ctx, operation, nil); err != nil {
			return err
		}
		id, err := operationID()
		if err != nil {
			return err
		}
		now := s.config.Now().UTC().Truncate(time.Microsecond)
		query := orm.For(s.store, func() *IdempotencyRecord { return &IdempotencyRecord{} })
		key := orm.UniqueKey{Constraint: "gogo_idempotency_identity", Values: map[string]any{"scope_hash": scopeHash, "action": operation.Action, "key_digest": keyDigest}}
		record, created, err := query.GetOrCreate(ctx, key, orm.Defaults{"id": id, "request_digest": requestDigest, "response_status": 0, "response_headers": map[string]string{}, "response_body": Values{}, "object_key": Values{}, "created_at": now, "expires_at": now.Add(s.config.Retention)})
		if err != nil {
			return err
		}
		record, err = query.Filter(orm.Q("id", record.ID)).SelectForUpdate(false, false).Get(ctx)
		if err != nil {
			return err
		}
		now = s.config.Now().UTC().Truncate(time.Microsecond)
		if !created && now.Before(record.ExpiresAt) {
			if err := s.permit(ctx, operation, Values(record.ObjectKey)); err != nil {
				return err
			}
			if record.RequestDigest != requestDigest {
				return mediaError(409, "IDEMPOTENCY_CONFLICT", "Operation key was used with different input")
			}
			response, err = sealMutationResponse(MutationResponse{Status: record.ResponseStatus, Headers: record.ResponseHeaders, Body: Values(record.ResponseBody), ObjectKey: Values(record.ObjectKey)})
			if err != nil {
				return err
			}
			response, err = s.redact(ctx, operation, response)
			if err == nil {
				result.Replayed = true
				prepared = true
			}
			return err
		}
		response, err = mutate(ctx)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err = sealMutationResponse(response)
		if err != nil {
			return err
		}
		if err := s.permit(ctx, operation, response.ObjectKey); err != nil {
			return err
		}
		response, err = s.redact(ctx, operation, response)
		if err != nil {
			return err
		}
		record.RequestDigest, record.ResponseStatus = requestDigest, response.Status
		record.ResponseHeaders, record.ResponseBody, record.ObjectKey = response.Headers, response.Body, response.ObjectKey
		sealedAt := s.config.Now().UTC().Truncate(time.Microsecond)
		record.CreatedAt, record.ExpiresAt = sealedAt, sealedAt.Add(s.config.Retention)
		if err := s.store.Save(ctx, record, orm.SaveOptions{ForceUpdate: true}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		prepared = true
		return nil
	})
	switch {
	case committed && prepared:
		result.Outcome, result.Response = MutationCommitted, response
	case prepared && db.IsCode(err, db.UnknownCommit):
		result = IdempotencyResult{Outcome: MutationUnknown}
	default:
		result = IdempotencyResult{Outcome: MutationUnchanged}
	}
	return result, err
}

func (s *Idempotency) permit(ctx context.Context, operation Operation, key Values) error {
	var snapshot Values
	if key != nil {
		var err error
		snapshot, err = receiptObject(key)
		if err != nil {
			return err
		}
	}
	err := s.config.Authorize(ctx, operation.Scope, operation.Action, snapshot)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func (s *Idempotency) redact(ctx context.Context, operation Operation, response MutationResponse) (MutationResponse, error) {
	key, err := receiptObject(response.ObjectKey)
	if err != nil {
		return MutationResponse{}, err
	}
	body, err := receiptObject(response.Body)
	if err != nil {
		return MutationResponse{}, err
	}
	filtered, err := s.config.Redact(ctx, operation.Scope, operation.Action, key, body)
	if ctx.Err() != nil {
		return MutationResponse{}, ctx.Err()
	}
	if err != nil {
		return MutationResponse{}, err
	}
	filtered, err = receiptObject(filtered)
	if err != nil || !isRedaction(map[string]any(response.Body), map[string]any(filtered)) {
		return MutationResponse{}, errors.New("api: redaction must only remove response fields")
	}
	response.Body = filtered
	return response, nil
}

func sealMutationResponse(response MutationResponse) (MutationResponse, error) {
	invalid := errors.New("api: invalid mutation response")
	if response.Status != 200 && response.Status != 201 && response.Status != 204 {
		return MutationResponse{}, invalid
	}
	key, err := receiptObject(response.ObjectKey)
	if err != nil || len(key) < 1 || len(key) > 16 {
		return MutationResponse{}, invalid
	}
	for name, value := range key {
		if !models.ValidIdentifier(name) || value == nil {
			return MutationResponse{}, invalid
		}
		switch value.(type) {
		case string:
			if len(value.(string)) > 1024 {
				return MutationResponse{}, invalid
			}
		case json.Number:
			normalized, err := canonicalKeyNumber(value.(json.Number))
			if err != nil {
				return MutationResponse{}, invalid
			}
			key[name] = normalized
		default:
			return MutationResponse{}, invalid
		}
	}
	body := response.Body
	if body == nil {
		body = Values{}
	}
	body, err = receiptObject(body)
	if err != nil || response.Status == 204 && len(body) != 0 {
		return MutationResponse{}, invalid
	}
	// JSON database backends may rewrite numeric lexemes (1e3 -> 1000).
	// Canonicalize both initial and replayed bodies before sealing, preserving
	// exact values and stable wire JSON without relying on storage formatting.
	canonicalBody, err := canonicalOperation(body)
	if err != nil {
		return MutationResponse{}, invalid
	}
	body = Values(canonicalBody.(map[string]any))
	headers := map[string]string{}
	for name, value := range response.Headers {
		if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
			return MutationResponse{}, invalid
		}
		switch name {
		case "Content-Type":
			if value != "application/json" {
				return MutationResponse{}, invalid
			}
		case "Location":
			if !strings.HasPrefix(value, "/") || security.SafeNext(value, "") == "" {
				return MutationResponse{}, invalid
			}
		case "ETag":
			if !validReceiptETag(value) {
				return MutationResponse{}, invalid
			}
		default:
			return MutationResponse{}, invalid
		}
		headers[name] = value
	}
	if response.Status != 204 {
		headers["Content-Type"] = "application/json"
	}
	return MutationResponse{Status: response.Status, Headers: headers, Body: body, ObjectKey: key}, nil
}

func validReceiptETag(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, character := range []byte(value[1 : len(value)-1]) {
		if character != 0x21 && (character < 0x23 || character > 0x7e) {
			return false
		}
	}
	return true
}
