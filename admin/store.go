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

type Deletion struct {
	Objects   []Object
	Updates   []RelatedUpdate
	Protected []string
}

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
