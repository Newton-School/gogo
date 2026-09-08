package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"time"
)

// Reader is owned by its caller and must be closed. It exposes no physical path.
type Reader interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Storage accepts server-owned keys, never user filenames. Implementations must
// document publication, cancellation and consistency guarantees. A storage handle
// is privileged: application authorization belongs before every operation.
type Storage interface {
	Open(context.Context, string) (Reader, error)
	Save(context.Context, string, io.Reader) (SaveResult, error)
	Delete(context.Context, string) error
	Exists(context.Context, string) (bool, error)
	List(context.Context, ListOptions) (Page, error)
	Size(context.Context, string) (int64, error)
	URL(context.Context, string) (string, error)
	ModifiedTime(context.Context, string) (time.Time, error)
}

// NewKey generates a cryptographically random opaque key. Retain the key before
// Save when a later metadata operation may require reconciliation.
func NewKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(value[:]), nil
}

func validKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	for i := range key {
		if !((key[i] >= '0' && key[i] <= '9') || (key[i] >= 'a' && key[i] <= 'f')) {
			return false
		}
	}
	return true
}
