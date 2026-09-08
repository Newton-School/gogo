package files

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

var (
	ErrInvalidOwner       = errors.New("files: invalid owner or metadata")
	ErrForbidden          = errors.New("files: access denied")
	ErrMetadataConflict   = errors.New("files: metadata or owner conflict")
	ErrTransaction        = errors.New("files: operation requires an independent transaction")
	ErrOutcomeUnknown     = errors.New("files: commit outcome unknown; reconcile before retry")
	ErrPublicationUnknown = errors.New("files: storage publication outcome unknown; reconcile before retry")
	ErrCommittedCallback  = errors.New("files: database committed; after-commit callback failed")
)

// Info is detached, privileged library metadata, not a public HTTP representation.
// It deliberately excludes owner references and policy-only owner field values.
type Info struct {
	ID, StorageAlias, ObjectKey string
	State                       State
	ContentType                 string
	Bytes                       int64
	Checksum                    string
	CreatedAt                   time.Time
	FinalizedAt                 *time.Time
}

type OwnerInput struct {
	Binding string
	Key     map[string]any
}

// StoreInput is prevalidated content, not a raw multipart upload contract.
// The application must approve actual bytes, MIME/extension/image/field rules
// before this boundary. ContentType is canonical metadata, not sniffing evidence.
type StoreInput struct {
	Identity    UploadIdentity
	Owner       OwnerInput
	ContentType string
}

type OwnerAction string

const (
	StoreFile   OwnerAction = "store"
	ReadFile    OwnerAction = "read"
	ReplaceFile OwnerAction = "replace"
)

// OwnerSnapshot is read-only policy input. Each callback receives fresh Key and
// Fields maps and a detached Previous. It cannot widen the selected field list.
type OwnerSnapshot struct {
	Binding, Reference string
	Key                map[string]any
	Fields             map[string]any
	Previous           *Info
}

type OwnerBinding struct {
	Name, Model, Field string
	PolicyFields       []string
	Scope              orm.QueryScope
	// Required current authority check. Exact ErrForbidden is a denial; other
	// errors are operational failures. Policies may lock independent ACL rows
	// through the supplied transaction context but must not mutate data/grants.
	Authorize func(context.Context, OwnerAction, OwnerSnapshot) error
}

type ServiceConfig struct {
	Backend      db.Backend
	Registry     *models.Registry
	StorageAlias string
	Storage      Storage
	Bindings     []OwnerBinding
}

// StoreFailure reports that the blob may need privileged reconciliation. Its
// result is zero unless Committed is true. Published is an observed storage outcome, not a
// claim about database commit, absence of a metadata row, or safe orphan deletion.
type StoreFailure struct {
	Identity  UploadIdentity
	Published bool
	// Committed is set only after this operation observed Commit(nil). Info is
	// then the detached verified committed value, despite a callback failure.
	Committed bool
	// PublicationUnknown means the provider panicked or violated its result
	// contract. Published=false then does not establish that the key is absent.
	PublicationUnknown bool
	kind               error
}

func (e *StoreFailure) Error() string {
	if e == nil || e.kind == nil {
		return ErrUnavailable.Error()
	}
	return e.kind.Error()
}
func (e *StoreFailure) Unwrap() error {
	if e == nil || e.kind == nil {
		return ErrUnavailable
	}
	return e.kind
}
func (e *StoreFailure) GoString() string           { return e.Error() }
func (e *StoreFailure) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }
