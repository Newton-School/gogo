// Package tasks defines a minimal task dispatch port. It installs no default
// provider, worker, scheduler or eager execution fallback. Applications select
// a trusted implementation explicitly; the optional Async module provides one.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxPayloadBytes bounds raw request and result JSON. An implementation may
// additionally bound its complete wire envelope, including task metadata.
const MaxPayloadBytes = 256 << 10

var (
	ErrConfiguration = errors.New("tasks: invalid provider configuration")
	ErrInvalid       = errors.New("tasks: invalid request")
	ErrUnknownTask   = errors.New("tasks: unknown task or version")
	ErrDenied        = errors.New("tasks: operation denied")
	ErrNotFound      = errors.New("tasks: result not found")
	ErrResultExpired = errors.New("tasks: result payload expired")
	ErrUnavailable   = errors.New("tasks: backend unavailable")
	ErrWorkerJoin    = errors.New("tasks: synchronous result waits inside workers are forbidden")
	ErrConflict      = errors.New("tasks: identity conflict")
)

// Request selects one registered, versioned task. Args must be valid UTF-8 JSON,
// not Go objects or executable source. Task and Queue use ASCII task names of at
// most 192 bytes; an empty Queue selects the provider's configured routing.
// An optional ID is a lowercase UUID; Scope is bounded to 256 UTF-8 bytes and is
// only an input to current authorization, never an authorization grant.
// Reusing an ID with a reconstructed request is not an exact-envelope retry.
type Request struct {
	Task    string
	Version int
	Args    json.RawMessage
	ID      string
	Queue   string
	Scope   string
}

// Dispatcher is a trusted provider port. Implementations must freeze inputs
// before callbacks, validate registered schemas and routing, enforce current
// authorization, and distinguish rejection from an uncertain applied write.
// No result means definite pre-publication refusal. A nonnil result with an
// AcceptanceError identifies an attempted publication, not confirmed execution.
// A nil error confirms admission only. Providers must not silently run tasks
// inline or enable retries; explicit provider configuration owns those policies.
type Dispatcher interface {
	Enqueue(context.Context, Request) (Result, error)
}

// Result is an identity-bound read handle, not an access grant. Get uses current
// caller authority, waits cooperatively with context, and returns detached JSON
// only on success. Providers must refuse synchronous waits in their worker
// context and distinguish unknown IDs, expired payloads and backend outages.
// Not-found after uncertain admission does not prove that no work was accepted.
type Result interface {
	ID() string
	Get(context.Context) (json.RawMessage, error)
}

// AcceptanceError preserves identity after attempted publication. Confirmed
// means transport acceptance was observed; result registration may still have
// failed. Cause can retain a provider's exact replay handle via errors.As, but
// this port does not invent a retry or an exactly-once guarantee.
// Cause is available for explicit local inspection and is never formatted here.
type AcceptanceError struct {
	ID        string
	Confirmed bool
	Cause     error
}

func (*AcceptanceError) Error() string { return "tasks: admission could not be fully confirmed" }
func (e *AcceptanceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func (e *AcceptanceError) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }

// Failure is a terminal task outcome with a safe public code and message, not a
// raw handler error or traceback. Providers bound Code to 256 UTF-8 bytes and
// Message to 4096 UTF-8 bytes. FAILED/REVOKED/EXPIRED task outcomes differ from
// ErrResultExpired, which means the retained output is no longer available.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f Failure) Error() string { return f.Code + ": " + f.Message }
