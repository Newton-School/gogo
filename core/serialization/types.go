// Package serialization exports explicitly authorized fixture profiles and
// imports complete non-auto-key rows without running model lifecycle hooks.
package serialization

import (
	"context"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type Format string

const (
	JSON  Format = "json"
	JSONL Format = "jsonl"
)

type Action string

const (
	ExportRecord Action = "export"
	ImportRecord Action = "import"
)

var (
	ErrConfiguration     = errors.New("serialization: invalid fixture configuration")
	ErrInvalid           = errors.New("serialization: invalid fixture")
	ErrForbidden         = errors.New("serialization: access denied")
	ErrUnavailable       = errors.New("serialization: operation unavailable")
	ErrLimit             = errors.New("serialization: fixture limit exceeded")
	ErrTransaction       = errors.New("serialization: independent transaction required")
	ErrOutcomeUnknown    = errors.New("serialization: commit outcome unknown; reconcile before retry")
	ErrCommittedCallback = errors.New("serialization: committed; after-commit callback failed")
)

// Error exposes a one-based record and a registered field name, never input
// cells, SQL, connection data or provider diagnostics. Zero values are safe.
type Error struct {
	Record int
	Field  string
	kind   error
}

func (e *Error) Error() string {
	if e == nil || e.kind == nil {
		return ErrUnavailable.Error()
	}
	return e.kind.Error()
}
func (e *Error) Unwrap() error {
	if e == nil || e.kind == nil {
		return ErrUnavailable
	}
	return e.kind
}
func (e *Error) GoString() string           { return e.Error() }
func (e *Error) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }

// Record is a detached, privileged policy view. Fields includes explicitly
// selected and PolicyFields values; policy-only values never enter the stream.
type Record struct {
	Model      string
	PK, Fields map[string]any
}
type ModelProfile struct {
	Schema               models.Schema
	Fields, PolicyFields []string
	Scope                orm.QueryScope
	// Authorize is mandatory, cooperative and read-only. Independent mutable
	// ACLs must be coordinated by the application. No application write is
	// authorized by this callback contract, including an OnCommit callback.
	Authorize func(context.Context, Action, Record) error
	Import    bool
}
type Limits struct{ MaxBytes, MaxRecords, MaxRecordBytes int }

const (
	MaxBytes       = 32 << 20
	MaxRecords     = 10000
	MaxRecordBytes = 1 << 20
)

type Config struct {
	Backend  db.Backend
	Profiles []ModelProfile
	Limits   Limits
}
type DumpOptions struct {
	Format Format
	Models []string
}
type LoadOptions struct {
	Format Format
	Models []string
	DryRun bool
}
type DumpResult struct {
	Records, Bytes int
	Complete       bool
}
type LoadResult struct {
	Records           int
	Committed, DryRun bool
}

type Fixtures struct{ state *fixtureState }
type fixtureState struct {
	backend  db.Backend
	dialect  db.Dialect
	alias    string
	registry *models.Registry
	profiles map[string]profile
	limits   Limits
}
type profile struct {
	schema             models.Schema
	fields, policy, pk []string
	scope              orm.QueryScope
	authorize          func(context.Context, Action, Record) error
	load               bool
}

// Alias is immutable metadata, not a callback into the selected provider.
func (f *Fixtures) Alias() string {
	if f == nil || f.state == nil {
		return ""
	}
	return f.state.alias
}
