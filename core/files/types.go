// Package files provides private, bounded blob storage. Storage operations do
// not authorize application users or create file metadata records.
package files

import (
	"errors"
	"fmt"
	"time"
)

const (
	DefaultMaxBytes  int64 = 10 << 20
	DefaultListLimit       = 100
	MaxListLimit           = 1000
)

var (
	ErrConfiguration                = errors.New("files: invalid configuration")
	ErrInvalidKey                   = errors.New("files: invalid storage key")
	ErrLimit                        = errors.New("files: storage limit exceeded")
	ErrNotFound                     = errors.New("files: object not found")
	ErrCollision                    = errors.New("files: object already exists")
	ErrClosed                       = errors.New("files: storage closed")
	ErrUnavailable                  = errors.New("files: storage unavailable")
	ErrStorageCapabilityUnavailable = errors.New("files: storage capability unavailable")
)

// SaveResult describes an observed publication, not a database transaction.
// Before publication all fields are zero, including when Save fails. After
// publication Published remains true even if temporary cleanup or descriptor
// completion fails. Callers must not blindly retry an already-published key.
type SaveResult struct {
	Key          string
	Bytes        int64
	SHA256       string
	ModifiedTime time.Time
	Published    bool
}

// ListOptions selects keys lexicographically after After. An empty After starts
// at the beginning. Limit defaults to DefaultListLimit and cannot exceed 1000.
type ListOptions struct {
	After string
	Limit int
}

// Page is a detached, bounded key page. Next is empty when this scan found no
// following key; otherwise it is the last returned key, for ListOptions.After.
type Page struct {
	Keys []string
	Next string
}

type LocalConfig struct {
	Directory string
	MaxBytes  int64
}

func (LocalConfig) String() string               { return "files.LocalConfig{directory:redacted}" }
func (c LocalConfig) GoString() string           { return c.String() }
func (c LocalConfig) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, c.String()) }
func (LocalConfig) MarshalJSON() ([]byte, error) { return nil, ErrConfiguration }

// readError retains a reader's error identity without including its possibly
// sensitive message in normal formatting. Filesystem errors are never retained.
type readError struct{ cause error }

func (e *readError) Error() string              { return ErrUnavailable.Error() }
func (e *readError) Unwrap() []error            { return []error{ErrUnavailable, e.cause} }
func (e *readError) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }
