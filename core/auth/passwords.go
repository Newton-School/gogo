package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"golang.org/x/crypto/argon2"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrCredentials = errors.New("invalid credentials")
var ErrPasswordHash = errors.New("invalid password hash")

type PasswordParams struct {
	Memory, Iterations uint32
	Parallelism        uint8
}

func DefaultPasswordParams() PasswordParams {
	return PasswordParams{Memory: 64 * 1024, Iterations: 3, Parallelism: 1}
}
func (p PasswordParams) validate() error {
	if p.Memory < 8*1024 || p.Memory > 256*1024 || p.Iterations < 1 || p.Iterations > 10 || p.Parallelism < 1 || p.Parallelism > 16 {
		return ErrPasswordHash
	}
	return nil
}
func HashPassword(password string) (string, error) {
	return HashPasswordWith(password, DefaultPasswordParams())
}

// UnusablePassword creates a unique marker that never authenticates. It is not
// a hash of an empty password. Use it for accounts without local credentials.
func UnusablePassword() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "!" + base64.RawURLEncoding.EncodeToString(value), nil
}

// HasUsablePassword reports the presence of a local credential, not whether an
// arbitrary stored hash is well formed. Verification always validates encoding.
func HasUsablePassword(encoded string) bool {
	return encoded != "" && !strings.HasPrefix(encoded, "!")
}
func HashPasswordWith(password string, p PasswordParams) (string, error) {
	if len(password) > 4096 {
		return "", errors.New("password too long")
	}
	if err := p.validate(); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Iterations, p.Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func VerifyPassword(password, encoded string) (valid, needsRehash bool, err error) {
	if len(password) > 4096 || len(encoded) > 512 {
		return false, false, ErrPasswordHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, false, ErrPasswordHash
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, false, ErrPasswordHash
	}
	nums := make([]uint64, 3)
	for i, prefix := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(params[i], prefix) {
			return false, false, ErrPasswordHash
		}
		n, e := strconv.ParseUint(strings.TrimPrefix(params[i], prefix), 10, 32)
		if e != nil {
			return false, false, ErrPasswordHash
		}
		nums[i] = n
	}
	if nums[2] > 255 {
		return false, false, ErrPasswordHash
	}
	p := PasswordParams{uint32(nums[0]), uint32(nums[1]), uint8(nums[2])}
	if p.validate() != nil {
		return false, false, ErrPasswordHash
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil || len(salt) < 16 || len(salt) > 64 {
		return false, false, ErrPasswordHash
	}
	expected, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil || len(expected) != 32 {
		return false, false, ErrPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, 32)
	valid = subtle.ConstantTimeCompare(actual, expected) == 1
	return valid, valid && passwordUpgradeNeeded(p), nil
}

// Automatic rehashing must not silently lower a client's stronger work factors.
// Mixed profiles require an explicit operator-chosen migration policy instead.
func passwordUpgradeNeeded(p PasswordParams) bool {
	target := DefaultPasswordParams()
	return p != target && p.Memory <= target.Memory && p.Iterations <= target.Iterations && p.Parallelism <= target.Parallelism
}

type PasswordValidator func(context.Context, string, Principal) error

func MinimumLength(min int) PasswordValidator {
	return func(_ context.Context, p string, _ Principal) error {
		if utf8.RuneCountInString(p) < min {
			return errors.New("password is too short")
		}
		return nil
	}
}
func NonNumeric(_ context.Context, password string, _ Principal) error {
	for _, r := range password {
		if !unicode.IsDigit(r) {
			return nil
		}
	}
	return errors.New("password must not be entirely numeric")
}
func CommonPasswords(words []string) PasswordValidator {
	set := map[string]struct{}{}
	for _, word := range words {
		set[strings.ToLower(strings.TrimSpace(word))] = struct{}{}
	}
	return func(_ context.Context, p string, _ Principal) error {
		if _, ok := set[strings.ToLower(strings.TrimSpace(p))]; ok {
			return errors.New("password is too common")
		}
		return nil
	}
}
func ValidatePassword(ctx context.Context, password string, p Principal, validators ...PasswordValidator) error {
	var errs []error
	for _, v := range validators {
		if err := ctx.Err(); err != nil {
			return err
		}
		if v != nil {
			candidate := p
			candidate.Permissions = slices.Clone(p.Permissions)
			errs = append(errs, v(ctx, password, candidate))
		}
	}
	return errors.Join(errs...)
}

// PasswordBackend separates identity lookup from password work and upgrades.
// UpdateHash must compare the previous hash to avoid overwriting a concurrent change.
type PasswordBackend interface {
	Lookup(context.Context, string) (Principal, string, error)
	UpdateHash(context.Context, string, string, string) error
}

// CredentialValidator optionally rechecks current identity state after password
// work. The built-in account backend uses it to reject concurrent replacement.
type CredentialValidator interface {
	RevalidateCredential(context.Context, string, string) (Principal, error)
}
type Authenticator struct {
	Backend PasswordBackend
	dummy   string
	slots   chan struct{}
}

func NewAuthenticator(backend PasswordBackend, maxConcurrent int) (*Authenticator, error) {
	if backend == nil || maxConcurrent < 1 || maxConcurrent > 64 {
		return nil, errors.New("invalid authentication configuration")
	}
	dummy, err := HashPassword("unusable-dummy-verification-value")
	if err != nil {
		return nil, err
	}
	return &Authenticator{backend, dummy, make(chan struct{}, maxConcurrent)}, nil
}
func (a *Authenticator) Authenticate(ctx context.Context, identifier, password string) (Principal, error) {
	if len(identifier) > 512 || len(password) > 4096 {
		return Principal{}, ErrCredentials
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return Principal{}, ctx.Err()
	}
	p, encoded, lookupErr := a.Backend.Lookup(ctx, identifier)
	if lookupErr != nil && !errors.Is(lookupErr, ErrUnauthenticated) {
		return Principal{}, lookupErr
	}
	if lookupErr != nil {
		encoded = a.dummy
	}
	valid, rehash, err := verifyCredential(password, encoded, a.dummy, VerifyPassword)
	if err != nil || !valid || lookupErr != nil || !p.Active {
		return Principal{}, ErrCredentials
	}
	if err = ctx.Err(); err != nil {
		return Principal{}, err
	}
	if rehash {
		replacement, e := HashPassword(password)
		if e != nil {
			return Principal{}, e
		}
		if e = a.Backend.UpdateHash(ctx, p.ID, encoded, replacement); e != nil {
			return Principal{}, e
		}
		encoded = replacement
	}
	if validator, ok := a.Backend.(CredentialValidator); ok {
		id := p.ID
		p, err = validator.RevalidateCredential(ctx, id, encoded)
		if err != nil {
			return Principal{}, err
		}
		if !p.Active || p.ID != id || p.AuthVersion == 0 {
			return Principal{}, ErrCredentials
		}
	}
	p.Authenticated = true
	return p, nil
}

// Malformed and unusable credentials must perform the same bounded dummy work
// as a missing account, rather than becoming a fast account-discovery oracle.
func verifyCredential(password, encoded, dummy string, verify func(string, string) (bool, bool, error)) (bool, bool, error) {
	valid, rehash, err := verify(password, encoded)
	if err != nil {
		_, _, _ = verify(password, dummy)
	}
	return valid, rehash, err
}
