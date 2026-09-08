package cache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/db"
)

var (
	ErrInvalidationConfig      = errors.New("cache: invalid invalidation configuration")
	ErrInvalidationUnavailable = errors.New("cache: invalidation unavailable")
)

// InvalidationConfig selects privileged, exact-key deletion. Keys must be
// produced by Key using this Namespace. A namespace is isolation, not a grant;
// applications must authorize the mutation before scheduling invalidation.
type InvalidationConfig struct {
	Store     Store
	Namespace string
	Keys      []string
}

// Invalidation owns an immutable, bounded exact-key plan. It does not own the
// provider's lifetime or mutable internals and never discovers keys or clears a
// database. It is safe to share when its explicitly supplied provider is safe.
type Invalidation struct {
	store Store
	keys  []string
}

// InvalidationReport distinguishes replies observed from operations attempted.
// Acknowledged means Delete returned nil, including an already-absent key. A
// failed attempt may have deleted its key; unattempted keys have not been called.
type InvalidationReport struct {
	Total        int
	Attempted    int
	Acknowledged int
}

// InvalidationError retains a detached partial report and an explicitly
// inspectable provider cause. Routine formatting exposes neither keys nor the
// cause. It is not proof that the failed deletion had no effect.
type InvalidationError struct {
	Report InvalidationReport
	kind   error
	cause  error
}

func (e *InvalidationError) Error() string {
	if e == nil || e.kind == nil {
		return ErrInvalidationUnavailable.Error()
	}
	return e.kind.Error()
}
func (e *InvalidationError) Unwrap() []error {
	if e == nil {
		return nil
	}
	kind := e.kind
	if kind == nil {
		kind = ErrInvalidationUnavailable
	}
	if e.cause == nil {
		return []error{kind}
	}
	return []error{kind, e.cause}
}
func (e *InvalidationError) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }

// NewInvalidation freezes one to 256 keys, deduplicating in first-seen order.
// Namespace is one to 128 ASCII letters, digits, '.', '_' or '-'. Keys have the
// exact namespace prefix and 64 lowercase hexadecimal digest bytes; glob syntax,
// other namespaces, raw Redis keys and shortened digests are refused.
func NewInvalidation(config InvalidationConfig) (*Invalidation, error) {
	if invalidationNil(config.Store) || !invalidationName(config.Namespace) || len(config.Keys) == 0 || len(config.Keys) > 256 {
		return nil, ErrInvalidationConfig
	}
	keys := slices.Clone(config.Keys)
	seen := make(map[string]bool, len(keys))
	owned := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) != len(config.Namespace)+65 || !strings.HasPrefix(key, config.Namespace+":") {
			return nil, ErrInvalidationConfig
		}
		for _, c := range key[len(config.Namespace)+1:] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return nil, ErrInvalidationConfig
			}
		}
		if !seen[key] {
			owned = append(owned, key)
			seen[key] = true
		}
	}
	return &Invalidation{store: config.Store, keys: owned}, nil
}

// Apply deletes exact keys sequentially and stops on the first error or panic.
// There is no automatic retry and no cross-key transaction. A confirmed final
// reply remains acknowledged even when cancellation happens immediately after
// it; cancellation before the next call stops that later call.
func (i *Invalidation) Apply(ctx context.Context) (report InvalidationReport, err error) {
	if i == nil {
		return report, ErrInvalidationConfig
	}
	selected := *i // Before context/provider callbacks can replace the public handle.
	if invalidationNil(selected.store) || len(selected.keys) == 0 {
		return report, ErrInvalidationConfig
	}
	report.Total = len(selected.keys)
	defer func() {
		if recover() != nil {
			err = &InvalidationError{Report: report, kind: ErrInvalidationUnavailable}
		}
	}()
	for _, key := range selected.keys {
		if canceled := invalidationContext(ctx); canceled != nil {
			return report, &InvalidationError{Report: report, kind: canceled}
		}
		report.Attempted++
		if failure := selected.store.Delete(ctx, key); failure != nil {
			kind := ErrInvalidationUnavailable
			if failure == context.Canceled || failure == context.DeadlineExceeded {
				kind = failure
			}
			return report, &InvalidationError{Report: report, kind: kind, cause: failure}
		}
		report.Acknowledged++
	}
	return report, nil
}

// OnCommit registers Apply with the selected database alias. Nested savepoints
// retain the callback until actual outer commit and discard it on rollback.
// Outside a transaction this executes immediately, as db.OnCommit does. A nil
// registration error does not prove a deferred deletion ran. Robust controls
// whether later callbacks continue after failure, not retry or error suppression.
// Ordinary callbacks are not durable: a crash or read-through repopulation can
// leave stale data. Use bounded TTLs and authoritative reads for strict freshness.
func (i *Invalidation) OnCommit(ctx context.Context, alias string, robust bool) (err error) {
	if i == nil {
		return ErrInvalidationConfig
	}
	selected := *i
	if invalidationNil(selected.store) || len(selected.keys) == 0 || !invalidationName(alias) {
		return ErrInvalidationConfig
	}
	defer func() {
		if recover() != nil {
			err = ErrInvalidationUnavailable
		}
	}()
	if err := invalidationContext(ctx); err != nil {
		return err
	}
	return db.OnCommit(ctx, alias, func(callbackContext context.Context) error {
		_, err := selected.Apply(callbackContext)
		return err
	}, robust)
}

func invalidationName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

func invalidationNil(value any) bool {
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

func invalidationContext(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalidationUnavailable
		}
	}()
	if invalidationNil(ctx) {
		return ErrInvalidationUnavailable
	}
	switch value := ctx.Err(); value {
	case nil, context.Canceled, context.DeadlineExceeded:
		return value
	default:
		return ErrInvalidationUnavailable
	}
}
