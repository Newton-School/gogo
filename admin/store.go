// Package admin provides explicitly registered, policy-protected model administration.
package admin

import (
	"context"
	"errors"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
)

var ErrNotFound = errors.New("admin: object not found")
var ErrConflict = errors.New("admin: object changed")

type Object struct {
	Record             models.Record
	ID, Version, Label string
}
type ListQuery struct {
	Search        string
	SearchFields  []string
	Filters       map[string]string
	Ordering      []string
	Offset, Limit int
}
type Page struct {
	Objects []Object
	Count   int64
}
type LogEntry struct {
	ActorID, Site, Model, ObjectID, ObjectLabel, Action string
	ActorLabel, RequestID                               string
	Changes                                             map[string]Change
	At                                                  time.Time
}
type Change struct{ Before, After any }

// Store must apply actor/tenant scope when constructing the returned store.
// That scope applies equally to object reads, counts, writes and history.
type Store interface {
	Scope(context.Context, auth.Principal, string, models.Schema) (ScopedStore, error)
}
type ScopedStore interface {
	List(context.Context, ListQuery) (Page, error)
	Get(context.Context, string, bool) (Object, error)
	New(context.Context) (Object, error)
	Save(context.Context, Object) (Object, error)
	Delete(context.Context, Object) error
	History(context.Context, string, int, int) ([]LogEntry, error)
	Audit(context.Context, LogEntry) error
	Atomic(context.Context, func(context.Context) error) error
}

// ObservedAtomicStore optionally proves the outcome of this exact outer
// transaction. It must reject an ambient/nested transaction before running fn,
// call committed only after durable commit and before any user after-commit
// callbacks, and otherwise preserve Atomic's rollback-on-body-error contract.
// The witness cannot be inferred from an error returned by application hooks.
type ObservedAtomicStore interface {
	AtomicObserved(context.Context, func(context.Context) error, func()) error
}

// RelationStore keeps automatic many-to-many form effects in the same parent
// transaction. InitialRelations returns only visible target primary keys.
// SaveRelations must preserve hidden links, scope all endpoints, and authorize
// the freshly locked final effect before mutation. Explicit through models use
// separately registered ModelAdmins/inlines, never an implicit form operation.
type RelationStore interface {
	InitialRelations(context.Context, Object, []string) (map[string][]any, error)
	SaveRelations(context.Context, Object, map[string][]any, func(context.Context, RelationChange) error) error
}

// RelationReader supplies bounded read-only relationship snapshots. Both the
// source and targets (and explicit intermediary rows) must obey this scoped
// store's actor/tenant boundary. It must never infer source identity from POST.
// Site additionally checks target model/object view policy before displaying
// any labels. The default ORM implementation uses batched relationship reads.
type RelationReader interface {
	ReadRelations(context.Context, Object, []string) (map[string][]Object, error)
}
type RelationChange struct {
	Field          string
	Source         Object
	Added, Removed []Object
}

type Deletion struct {
	Objects      []Object
	Updates      []RelatedUpdate
	Protected    []string
	JoinRemovals []JoinRemoval
}

// JoinRemoval removes automatic intermediary links belonging to an endpoint
// in this same deletion graph. It never grants access to the other endpoint.
// Trusted stores must verify intermediary provenance and constrain the actual
// delete by the exact endpoint FK. No internal join identities enter the UI.
type JoinRemoval struct{ Endpoint Object }

// RelatedUpdate is a SET_NULL or SET_DEFAULT effect, requiring change permission.
type RelatedUpdate struct {
	Object Object
	Field  string
	Value  any
}

// DeleteCollector is required to preview or execute cascaded model deletion.
type DeleteCollector interface {
	CollectDeletion(context.Context, Object) (Deletion, error)
}

// DeletionExecutor authorizes the final locked graph before any mutation. The
// callback must be called inside the deletion transaction, not on a preview.
type DeletionExecutor interface {
	DeleteAuthorized(context.Context, Object, func(context.Context, Deletion) error) error
}
