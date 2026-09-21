package auth

import (
	"context"
	"errors"
	"strconv"

	"github.com/Newton-School/gogo/core/models"
)

// AccountModels fixes the User and Group primary-key type for one database.
// The zero value uses AutoField (32-bit integers allocated by the database).
// Choose before the first migration; never switch an existing database in place.
// This is a value configuration, not a process-global setting.
type AccountModels struct{ kind models.Kind }

// NewAccountModels selects Auto, BigAuto (64-bit), or UUID identities.
// UUID preserves the account schema used by earlier Gogo releases.
func NewAccountModels(kind models.Kind) (AccountModels, error) {
	switch kind {
	case "", models.Auto:
		return AccountModels{}, nil
	case models.BigAuto, models.UUID:
		return AccountModels{kind: kind}, nil
	default:
		return AccountModels{}, errors.New("auth: account ID kind must be auto, big_auto or uuid")
	}
}

func (m AccountModels) idField() models.Field {
	kind := m.kind
	if kind == "" {
		kind = models.Auto
	}
	return models.NewField("id", kind, models.WithStructField("ID"), models.Primary, models.ReadOnly)
}

// User is a factory for direct ORM queries using this configuration.
// IDs remain strings at the auth API boundary so sessions and policies work
// identically for integer and UUID accounts. Integer IDs use decimal strings.
func (m AccountModels) User() *User { return &User{identity: m} }

// Group is the matching ORM factory for groups; integer IDs are decimal strings.
func (m AccountModels) Group() *Group { return &Group{identity: m} }

func (m AccountModels) validID(id string) bool {
	if m.kind == models.UUID {
		return validUUID(id)
	}
	bits := 32
	if m.kind == models.BigAuto {
		bits = 64
	}
	n, err := strconv.ParseInt(id, 10, bits)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func (m AccountModels) newID() (string, error) {
	if m.kind == models.UUID {
		return newAccountID()
	}
	// Do not assign or reserve a number in Go. INSERT ... RETURNING owns it.
	return "", nil
}

func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := models.UUIDField("id").Clean(context.Background(), id)
	return err == nil
}
