package async

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/tasks"
)

// NewCoreDispatcher binds the minimal Core port to an explicitly configured
// Async client. It starts and closes no resources. The client configuration is
// captured now; later assignment through the caller's *Client does not retarget
// this dispatcher or its results. Existing explicit PublishRetry configuration
// still applies; the bridge introduces no additional retry or eager fallback.
func NewCoreDispatcher(client *Client) (tasks.Dispatcher, error) {
	if client == nil {
		return nil, tasks.ErrConfiguration
	}
	copy := *client // Client has no mutex; Registry remains a shared frozen handle.
	if copy.config.Registry == nil || coreDispatchNil(copy.config.Broker) || coreDispatchNil(copy.config.Results) || copy.config.Clock == nil || copy.config.NewID == nil || len(copy.queues) == 0 {
		return nil, tasks.ErrConfiguration
	}
	return &coreDispatcher{client: copy}, nil
}

type coreDispatcher struct{ client Client }
type coreDispatchResult struct {
	client Client
	id     string
}

var _ tasks.Dispatcher = (*coreDispatcher)(nil)
var _ tasks.Result = (*coreDispatchResult)(nil)

func (d *coreDispatcher) Enqueue(ctx context.Context, request tasks.Request) (result tasks.Result, err error) {
	defer func() {
		if recover() != nil {
			result, err = nil, tasks.ErrUnavailable
		}
	}()
	if d == nil {
		return nil, tasks.ErrConfiguration
	}
	client := d.client
	// Bound before copying or invoking any context/configured callback. Full
	// task schema and complete-envelope validation remain owned by Enqueue.
	if len(request.Args) > tasks.MaxPayloadBytes || !utf8.Valid(request.Args) || !json.Valid(request.Args) || !namePattern.MatchString(request.Task) || request.Version < 1 || request.ID != "" && !idPattern.MatchString(request.ID) || request.Queue != "" && !namePattern.MatchString(request.Queue) || len(request.Scope) > 256 || !utf8.ValidString(request.Scope) {
		return nil, tasks.ErrInvalid
	}
	request.Args = append(json.RawMessage(nil), request.Args...)
	if err := groupContextError(ctx); err != nil {
		return nil, coreDispatchError(err)
	}
	signature := Signature{Task: request.Task, Version: request.Version, Args: request.Args,
		Options: DispatchOptions{ID: request.ID, Queue: request.Queue, Scope: request.Scope}}
	receipt, admission, err := runProducerObserved(ctx, &client, &signature)
	if err == nil {
		return &coreDispatchResult{client: client, id: receipt.ID}, nil
	}
	// Only this producer's witnessed write establishes current admission. A
	// callback may return a forged or borrowed AcceptanceError before any write.
	if admission.attempted {
		return &coreDispatchResult{client: client, id: admission.id}, &tasks.AcceptanceError{
			ID: admission.id, Confirmed: admission.accepted,
			Cause: &coreDispatchCause{kind: tasks.ErrUnavailable, cause: err},
		}
	}
	return nil, coreDispatchError(err)
}

func (r *coreDispatchResult) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

func (r *coreDispatchResult) Get(ctx context.Context) (out json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			out, err = nil, tasks.ErrUnavailable
		}
	}()
	if r == nil {
		return nil, tasks.ErrInvalid
	}
	client, id := r.client, r.id
	if err := groupContextError(ctx); err != nil {
		return nil, coreDispatchError(err)
	}
	out, err = RestoreResult[json.RawMessage](&client, id).Get(ctx)
	if err != nil {
		return nil, coreDispatchError(err)
	}
	return append(json.RawMessage(nil), out...), nil
}

func coreDispatchNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// Exact sentinels retain portable classification. Unknown/mixed operational
// errors remain unavailable; their cause is retained without calling Error,
// Is, As, Format or any other user method while constructing the safe wrapper.
func coreDispatchError(err error) error {
	var kind error
	switch err {
	case nil:
		return nil
	case ErrInvalid:
		kind = tasks.ErrInvalid
	case ErrUnknownTask:
		kind = tasks.ErrUnknownTask
	case ErrDenied:
		kind = tasks.ErrDenied
	case ErrNotFound:
		kind = tasks.ErrNotFound
	case ErrResultExpired:
		kind = tasks.ErrResultExpired
	case ErrWorkerJoin:
		kind = tasks.ErrWorkerJoin
	case ErrConflict:
		kind = tasks.ErrConflict
	case context.Canceled, context.DeadlineExceeded:
		return err
	default:
		if failure, ok := err.(Failure); ok {
			return tasks.Failure{Code: failure.Code, Message: failure.Message}
		}
		kind = tasks.ErrUnavailable
	}
	return &coreDispatchCause{kind: kind, cause: err}
}

type coreDispatchCause struct{ kind, cause error }

func (e *coreDispatchCause) Error() string              { return e.kind.Error() }
func (e *coreDispatchCause) Unwrap() []error            { return []error{e.kind, e.cause} }
func (e *coreDispatchCause) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }
