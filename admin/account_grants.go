package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// AccountGrantKind names stock form fields without adding virtual model fields
// or changing installed auth migration state.
type AccountGrantKind string

const (
	UserGroups       AccountGrantKind = "groups"
	UserPermissions  AccountGrantKind = "user_permissions"
	GroupPermissions AccountGrantKind = "permissions"
)

// AccountGrantChoices contains only currently visible target identities. A
// hidden existing relationship never contributes a label, ID or count here.
// The bounded stock selector requires a narrower configured scope above 1000
// choices; it is not an unbounded account enumeration endpoint.
type AccountGrantChoices struct {
	Choices  []forms.Choice
	Selected []string
}

// AccountGrantAuthorizer is a read-only target-object check supplied by the
// Site. It runs first with an empty Object for target-model permission before
// lookup, then with scoped target objects. It can only narrow row scope.
// Non-denial provider failures must propagate, not silently hide choices.
type AccountGrantAuthorizer func(context.Context, AccountGrantKind, Object) error

type AccountGrantReader interface {
	GrantChoices(context.Context, Object, AccountGrantKind, AccountGrantAuthorizer) (AccountGrantChoices, error)
}

func checkGrantModel(ctx context.Context, kind AccountGrantKind, authorize AccountGrantAuthorizer) error {
	if authorize == nil {
		return auth.ErrPermissionDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, kind, Object{}); err != nil {
		return err
	}
	return ctx.Err()
}

const maxAccountGrantChoices = 1000

type grantDescriptor struct {
	source, target           models.Schema
	link                     func() models.Model
	sourceField, targetField string
}

func describeGrant(kind AccountGrantKind) (grantDescriptor, error) {
	switch kind {
	case UserGroups:
		return grantDescriptor{(&auth.User{}).Schema(), (&auth.Group{}).Schema(), func() models.Model { return &auth.UserGroup{} }, "user_id", "group_id"}, nil
	case UserPermissions:
		return grantDescriptor{(&auth.User{}).Schema(), (&auth.Permission{}).Schema(), func() models.Model { return &auth.UserPermission{} }, "user_id", "permission_id"}, nil
	case GroupPermissions:
		return grantDescriptor{(&auth.Group{}).Schema(), (&auth.Permission{}).Schema(), func() models.Model { return &auth.GroupPermission{} }, "group_id", "permission_id"}, nil
	default:
		return grantDescriptor{}, auth.ErrPermissionDenied
	}
}

func grantID(schema models.Schema, value any) (string, error) {
	if schema.Key() == (&auth.Group{}).Schema().Key() {
		text, ok := value.(string)
		if !ok {
			return "", auth.ErrPermissionDenied
		}
		cleaned, err := models.UUIDField("id").Clean(context.Background(), text)
		if err != nil || cleaned != text {
			return "", auth.ErrPermissionDenied
		}
		return text, nil
	}
	text := fmt.Sprint(value)
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != text {
		return "", auth.ErrPermissionDenied
	}
	return text, nil
}

func grantKey(schema models.Schema, value string) (string, error) {
	id, err := grantID(schema, value)
	if err != nil {
		return "", err
	}
	var primary any = id
	if schema.Key() == (&auth.Permission{}).Schema().Key() {
		primary, _ = strconv.ParseInt(id, 10, 64)
	}
	encoded, err := json.Marshal([]any{primary})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func (s *accountScoped) grantTarget(ctx context.Context, descriptor grantDescriptor) (ScopedStore, error) {
	// Evaluate the target scope for this operation, never reuse the source
	// predicate or infer cross-tenant access from group/user change authority.
	return s.owner.Scope(ctx, s.principal, s.site, descriptor.target)
}

func checkGrantTarget(ctx context.Context, kind AccountGrantKind, object Object, authorize AccountGrantAuthorizer) error {
	if authorize == nil || object.Record == nil {
		return auth.ErrPermissionDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := objectFromRecord(object.Record)
	if err != nil {
		return err
	}
	if err := authorize(ctx, kind, object); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := objectFromRecord(object.Record)
	if err != nil {
		return err
	}
	if before.ID != object.ID || before.ID != after.ID || before.Version != after.Version {
		return auth.ErrPermissionDenied
	}
	return nil
}

func (s *accountScoped) currentGrantIDs(ctx context.Context, object Object, descriptor grantDescriptor) ([]string, error) {
	if object.Record == nil || object.Record.Schema().Key() != descriptor.source.Key() || s.schema.Key() != descriptor.source.Key() {
		return nil, auth.ErrPermissionDenied
	}
	if !object.Record.State().Persisted {
		return nil, nil
	}
	current, err := s.Get(ctx, object.ID, false)
	if err != nil {
		return nil, err
	}
	parentID, err := current.Record.Get("id")
	if err != nil {
		return nil, err
	}
	links, err := orm.For(s.owner.config.Store, descriptor.link).Filter(orm.Q(descriptor.sourceField, parentID)).Limit(maxAccountGrantChoices + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(links) > maxAccountGrantChoices {
		return nil, errors.New("admin: account grant maintenance bound exceeded")
	}
	ids := make([]string, 0, len(links))
	for _, link := range links {
		record, err := models.Bind(link)
		if err != nil {
			return nil, err
		}
		value, err := record.Get(descriptor.targetField)
		if err != nil {
			return nil, err
		}
		id, err := grantID(descriptor.target, value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		return nil, errors.New("admin: invalid account grant state")
	}
	return ids, nil
}

func (s *accountScoped) GrantChoices(ctx context.Context, object Object, kind AccountGrantKind, authorize AccountGrantAuthorizer) (AccountGrantChoices, error) {
	descriptor, err := describeGrant(kind)
	if err != nil || s.schema.Key() != descriptor.source.Key() || authorize == nil {
		return AccountGrantChoices{}, auth.ErrPermissionDenied
	}
	ctx = auth.WithPrincipal(ctx, s.principal)
	if err := checkGrantModel(ctx, kind, authorize); err != nil {
		return AccountGrantChoices{}, err
	}
	ids, err := s.currentGrantIDs(ctx, object, descriptor)
	if err != nil {
		return AccountGrantChoices{}, err
	}
	target, err := s.grantTarget(ctx, descriptor)
	if err != nil {
		return AccountGrantChoices{}, err
	}
	page, err := target.List(ctx, ListQuery{Limit: maxAccountGrantChoices + 1, Ordering: []string{"name", "id"}})
	if err != nil {
		return AccountGrantChoices{}, err
	}
	if page.Count > maxAccountGrantChoices || len(page.Objects) > maxAccountGrantChoices {
		return AccountGrantChoices{}, errors.New("admin: narrow the account grant selector scope")
	}
	candidates := make([]Object, 0, len(page.Objects))
	for _, object := range page.Objects {
		if err := checkGrantTarget(ctx, kind, object, authorize); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				continue
			}
			return AccountGrantChoices{}, err
		}
		candidates = append(candidates, object)
	}
	// Build output only after all policy callbacks. A later callback must not
	// make an earlier, now-out-of-scope label escape the response.
	result := AccountGrantChoices{Choices: []forms.Choice{}, Selected: []string{}}
	for _, object := range candidates {
		object, err = s.recheckGrantTarget(ctx, kind, object)
		if errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrPermissionDenied) {
			continue
		}
		if err != nil {
			return AccountGrantChoices{}, err
		}
		value, err := object.Record.Get("id")
		if err != nil {
			return AccountGrantChoices{}, err
		}
		id, err := grantID(descriptor.target, value)
		if err != nil {
			return AccountGrantChoices{}, err
		}
		name, err := object.Record.Get("name")
		if err != nil {
			return AccountGrantChoices{}, err
		}
		label, ok := name.(string)
		if !ok {
			return AccountGrantChoices{}, auth.ErrPermissionDenied
		}
		if descriptor.target.Key() == (&auth.Permission{}).Schema().Key() {
			code, err := object.Record.Get("codename")
			if err != nil {
				return AccountGrantChoices{}, err
			}
			label += " (" + fmt.Sprint(code) + ")"
		}
		result.Choices = append(result.Choices, forms.Choice{Value: id, Label: label})
		if slices.Contains(ids, id) {
			result.Selected = append(result.Selected, id)
		}
	}
	slices.Sort(result.Selected)
	return result, ctx.Err()
}

var _ AccountGrantReader = (*accountScoped)(nil)
