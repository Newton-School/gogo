package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// AccountChange describes the complete requested grant/security effect, never
// credentials. Authorize must evaluate current trusted actor authority, not
// infer permission from a user/group ID supplied by the client.
type AccountChange struct {
	Action, UserID, GroupID, Name string
	PermissionIDs                 []int64
	GroupIDs                      []string
	Active, Staff, Superuser      bool
}

type AccountsConfig struct {
	Store               *orm.Store
	NormalizeIdentifier func(string) (string, error)
	Authorize           func(context.Context, AccountChange) error
	PasswordValidators  []PasswordValidator
	MaxPasswordWork     int
}

type Accounts struct {
	store      *orm.Store
	normalize  func(string) (string, error)
	authorize  func(context.Context, AccountChange) error
	validators []PasswordValidator
	slots      chan struct{}
}

func NewAccounts(config AccountsConfig) (*Accounts, error) {
	if config.Store == nil || config.Store.Backend == nil || config.Store.Registry == nil {
		return nil, errors.New("auth: registered account models and backend required")
	}
	for _, required := range append(Schemas(), (&contenttypes.ContentType{}).Schema()) {
		actual, ok := config.Store.Registry.Get(required.Key())
		if !ok {
			return nil, errors.New("auth: required account model is not registered")
		}
		want, e1 := required.Fingerprint()
		got, e2 := actual.Fingerprint()
		if e1 != nil || e2 != nil || want != got {
			return nil, errors.New("auth: registered account model differs from default contract")
		}
	}
	if config.NormalizeIdentifier == nil {
		config.NormalizeIdentifier = NormalizeIdentifier
	}
	if config.MaxPasswordWork == 0 {
		config.MaxPasswordWork = 4
	}
	if config.MaxPasswordWork < 1 || config.MaxPasswordWork > 64 {
		return nil, errors.New("auth: invalid password work bound")
	}
	if config.PasswordValidators == nil {
		config.PasswordValidators = []PasswordValidator{MinimumLength(12), NonNumeric}
	}
	return &Accounts{store: config.Store, normalize: config.NormalizeIdentifier, authorize: config.Authorize, validators: slices.Clone(config.PasswordValidators), slots: make(chan struct{}, config.MaxPasswordWork)}, nil
}

// NormalizeIdentifier trims surrounding whitespace and preserves case. A
// project may replace it before creating identities (for example an email or
// Unicode-normalization policy). The same normalizer always handles writes and
// login lookup. Changing it for an existing table requires an explicit migration.
func NormalizeIdentifier(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || utf8.RuneCountInString(value) > 255 || !utf8.ValidString(value) {
		return "", errors.New("auth: invalid identifier")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("auth: invalid identifier")
		}
	}
	return value, nil
}

func (a *Accounts) identifier(value string) (string, error) {
	value, err := a.normalize(value)
	if err != nil {
		return "", err
	}
	return NormalizeIdentifier(value)
}

// NormalizeLoginIdentifier exposes this backend's exact identity equivalence
// rule to HTTP throttling. Using a different normalizer for rate buckets lets
// equivalent spellings bypass a per-identity limit.
func (a *Accounts) NormalizeLoginIdentifier(value string) (string, error) {
	return a.identifier(value)
}

func (a *Accounts) permit(ctx context.Context, change AccountChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.authorize == nil {
		return ErrPermissionDenied
	}
	if err := checkAccountTokenScope(FromContext(ctx), change); err != nil {
		return err
	}
	change.PermissionIDs = slices.Clone(change.PermissionIDs)
	change.GroupIDs = slices.Clone(change.GroupIDs)
	err := a.authorize(ctx, change)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (a *Accounts) Lookup(ctx context.Context, identifier string) (Principal, string, error) {
	normalized, err := a.identifier(identifier)
	if err != nil {
		return Principal{}, "", ErrUnauthenticated
	}
	user, principal, err := a.snapshot(ctx, orm.Q("identifier", normalized))
	if err != nil {
		return Principal{}, "", err
	}
	return principal, storedHash(user), nil
}

func (a *Accounts) LoadPrincipal(ctx context.Context, id string) (Principal, error) {
	if !validAccountID(id) {
		return Principal{}, ErrUnauthenticated
	}
	_, principal, err := a.snapshot(ctx, orm.Q("id", id))
	return principal, err
}

// RevalidateCredential is used after expensive password work, including a
// successful compare-and-swap rehash. A concurrent credential change cannot
// turn an old password into a session carrying the new account version.
func (a *Accounts) RevalidateCredential(ctx context.Context, id, encoded string) (Principal, error) {
	user, principal, err := a.snapshot(ctx, orm.Q("id", id))
	if err != nil {
		return Principal{}, err
	}
	if !principal.Active || subtle.ConstantTimeCompare([]byte(storedHash(user)), []byte(encoded)) != 1 {
		return Principal{}, ErrCredentials
	}
	return principal, nil
}

func storedHash(user *User) string {
	if user.PasswordHash == nil {
		return ""
	}
	return *user.PasswordHash
}

// Account/grant mutations bump auth_version in the same transaction. Reading
// it again closes mixed-snapshot reads even inside a caller-owned READ COMMITTED
// transaction. Contention fails closed after a bounded retry; there is no stale
// authorization cache and no implicit transaction isolation escalation.
func (a *Accounts) snapshot(ctx context.Context, where db.Predicate) (*User, Principal, error) {
	for range 3 {
		user, err := orm.For(a.store, func() *User { return &User{} }).Filter(where).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return nil, Principal{}, ErrUnauthenticated
		}
		if err != nil {
			return nil, Principal{}, err
		}
		if user.AuthVersion < 1 {
			return nil, Principal{}, ErrUnauthenticated
		}
		permissions, err := a.permissions(ctx, user.ID)
		if err != nil {
			return nil, Principal{}, err
		}
		current, err := orm.For(a.store, func() *User { return &User{} }).Filter(orm.Q("id", user.ID)).Only("auth_version", "password_hash", "active", "staff", "superuser").Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return nil, Principal{}, ErrUnauthenticated
		}
		if err != nil {
			return nil, Principal{}, err
		}
		if current.AuthVersion != user.AuthVersion || storedHash(current) != storedHash(user) || current.Active != user.Active || current.Staff != user.Staff || current.Superuser != user.Superuser {
			continue
		}
		principal := Principal{ID: user.ID, Authenticated: user.Active, Active: user.Active, Staff: user.Staff, Superuser: user.Superuser, AuthVersion: uint64(user.AuthVersion), Permissions: permissions}
		return user, principal, nil
	}
	return nil, Principal{}, errors.New("auth: account changed during authorization lookup")
}

const maxAccountGrants = 10000

func (a *Accounts) permissions(ctx context.Context, id string) ([]string, error) {
	direct, err := orm.For(a.store, func() *UserPermission { return &UserPermission{} }).Filter(orm.Q("user_id", id)).Limit(maxAccountGrants + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	memberships, err := orm.For(a.store, func() *UserGroup { return &UserGroup{} }).Filter(orm.Q("user_id", id)).Limit(maxAccountGrants + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(direct) > maxAccountGrants || len(memberships) > maxAccountGrants {
		return nil, errors.New("auth: account grant bound exceeded")
	}
	permissionSet := map[int64]bool{}
	for _, item := range direct {
		permissionSet[item.PermissionID] = true
	}
	if len(memberships) > 0 {
		groups := make([]string, len(memberships))
		for i, item := range memberships {
			groups[i] = item.GroupID
		}
		grants, err := orm.For(a.store, func() *GroupPermission { return &GroupPermission{} }).Filter(orm.Q("group_id__in", groups)).Limit(maxAccountGrants + 1).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(grants) > maxAccountGrants {
			return nil, errors.New("auth: account grant bound exceeded")
		}
		for _, item := range grants {
			permissionSet[item.PermissionID] = true
		}
	}
	if len(permissionSet) == 0 {
		return nil, nil
	}
	if len(permissionSet) > maxAccountGrants {
		return nil, errors.New("auth: account grant bound exceeded")
	}
	ids := make([]int64, 0, len(permissionSet))
	for id := range permissionSet {
		ids = append(ids, id)
	}
	permissions, err := orm.For(a.store, func() *Permission { return &Permission{} }).Filter(orm.Q("id__in", ids)).All(ctx)
	if err != nil {
		return nil, err
	}
	typeIDs := make([]int64, len(permissions))
	for i, item := range permissions {
		typeIDs[i] = item.ContentTypeID
	}
	if len(typeIDs) == 0 {
		return nil, nil
	}
	types, err := orm.For(a.store, func() *contenttypes.ContentType { return &contenttypes.ContentType{} }).Filter(orm.Q("id__in", typeIDs), orm.Q("active", true)).All(ctx)
	if err != nil {
		return nil, err
	}
	apps := map[int64]string{}
	for _, item := range types {
		apps[item.ID] = item.AppLabel
	}
	var result []string
	for _, item := range permissions {
		if app, ok := apps[item.ContentTypeID]; ok {
			result = append(result, app+"."+item.Codename)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func (a *Accounts) UpdateHash(ctx context.Context, id, previous, replacement string) error {
	return db.Atomic(ctx, a.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		user, err := orm.For(a.store, func() *User { return &User{} }).Filter(orm.Q("id", id)).SelectForUpdate(false, false).Get(ctx)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(storedHash(user)), []byte(previous)) != 1 {
			return ErrCredentials
		}
		if previous == replacement {
			return nil
		}
		user.PasswordHash = &replacement
		return a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"password_hash", "updated_at"}})
	})
}

type CreateUserOptions struct{ Inactive, Staff, Superuser bool }

func (a *Accounts) CreateUser(ctx context.Context, identifier, password string, options CreateUserOptions) (*User, error) {
	identifier, err := a.identifier(identifier)
	if err != nil {
		return nil, err
	}
	change := AccountChange{Action: "create_user", Name: identifier, Active: !options.Inactive, Staff: options.Staff, Superuser: options.Superuser}
	if err := a.permit(ctx, change); err != nil {
		return nil, err
	}
	id, err := newAccountID()
	if err != nil {
		return nil, err
	}
	hash, err := a.hash(ctx, password, Principal{ID: id, Active: !options.Inactive, Staff: options.Staff, Superuser: options.Superuser, AuthVersion: 1})
	if err != nil {
		return nil, err
	}
	user := &User{ID: id, Identifier: identifier, PasswordHash: &hash, Active: !options.Inactive, Staff: options.Staff, Superuser: options.Superuser, AuthVersion: 1}
	record, err := models.Bind(user)
	if err != nil {
		return nil, err
	}
	if err := record.Set("active", !options.Inactive); err != nil {
		return nil, err
	}
	err = db.Atomic(ctx, a.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		return a.saveUser(ctx, user, orm.SaveOptions{ForceInsert: true})
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (a *Accounts) hash(ctx context.Context, password string, principal Principal) (string, error) {
	if err := ValidatePassword(ctx, password, principal, a.validators...); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", passwordValidationError{cause: err}
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	return hash, ctx.Err()
}

func bumpVersion(user *User) error {
	if user.AuthVersion < 1 || user.AuthVersion == math.MaxInt64 {
		return errors.New("auth: account version cannot advance")
	}
	user.AuthVersion++
	return nil
}

func newAccountID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6], value[8] = value[6]&0x0f|0x40, value[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func validAccountID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := models.UUIDField("id").Clean(context.Background(), id)
	return err == nil
}
