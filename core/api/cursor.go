package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/security"
)

type ResourceCursorOptions struct {
	// Name distinguishes resources even when they expose the same model.
	// Version must change when ordering/visibility semantics change.
	Name, Version string
	Signer        *security.Signer
	MaxAge        time.Duration
	// ImmutableFields is an explicit application guarantee across ALL writes,
	// including writes outside Gogo. Every allowed/default ordering field and
	// every PK component must be non-null, immutable and directly serialized.
	ImmutableFields []string
	// ScopeIdentity returns the current tenant/visibility-policy version. The
	// framework also binds the verified identity, grants and token ceiling.
	ScopeIdentity func(context.Context, auth.Principal, models.Schema) (string, error)
}
type resourceCursor struct {
	name, version string
	codec         *pagination.CursorCodec
	identity      func(context.Context, auth.Principal, models.Schema) (string, error)
	// Model source name -> direct public serializer field name.
	fields map[string]string
}

func (s *Resource) newCursor(options *ResourceCursorOptions) (*resourceCursor, error) {
	if options == nil || options.Name == "" || len(options.Name) > 128 || !utf8.ValidString(options.Name) || options.Version == "" || len(options.Version) > 128 || !utf8.ValidString(options.Version) || options.ScopeIdentity == nil {
		return nil, errors.New("api: cursor requires resource name, version and scope identity")
	}
	codec, err := pagination.NewCursorCodec(pagination.CursorConfig{Signer: options.Signer, MaxAge: options.MaxAge})
	if err != nil {
		return nil, err
	}
	immutable := map[string]bool{}
	for _, name := range options.ImmutableFields {
		if immutable[name] {
			return nil, errors.New("api: duplicate immutable cursor field")
		}
		field, ok := s.schema.Field(name)
		if !ok || !queryScalar(field) || field.Null || field.Codec != nil {
			return nil, errors.New("api: cursor fields must be non-null scalar fields")
		}
		immutable[name] = true
	}
	used := slices.Clone(s.filters.ordering)
	used = append(used, s.filters.defaults...)
	for _, key := range s.schema.PKFields() {
		used = append(used, key.Name)
	}
	fields := map[string]string{}
	for _, value := range used {
		name := strings.TrimPrefix(value, "-")
		if !immutable[name] {
			return nil, errors.New("api: cursor ordering requires explicit immutable fields")
		}
		if _, exists := fields[name]; exists {
			continue
		}
		model, _ := s.schema.Field(name)
		for _, field := range s.config.Serializer.fields {
			if field.Source == name && !field.Hidden && !field.WriteOnly && field.Compute == nil && field.Represent == nil && field.Nested == nil && field.Element == nil && field.Model.Kind == model.Kind && field.Model.Codec == nil {
				fields[name] = field.Name
				break
			}
		}
		if fields[name] == "" {
			return nil, errors.New("api: cursor keys must be directly exposed by the output serializer")
		}
	}
	if len(fields) > 16 {
		return nil, errors.New("api: at most sixteen cursor ordering fields are supported")
	}
	return &resourceCursor{name: options.Name, version: options.Version, codec: codec, identity: options.ScopeIdentity, fields: fields}, nil
}

func hashCursorPart(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
func (s *Resource) cursorBinding(request resourceRequest, values url.Values, options QueryOptions, page pagination.Page) (pagination.CursorBinding, error) {
	p := auth.FromContext(request.ctx)
	scope, err := s.cursor.identity(request.ctx, p, s.schema.Clone())
	if request.ctx.Err() != nil {
		return pagination.CursorBinding{}, request.ctx.Err()
	}
	if err != nil {
		return pagination.CursorBinding{}, err
	}
	if scope == "" || len(scope) > 4096 || !utf8.ValidString(scope) {
		return pagination.CursorBinding{}, errors.New("api: invalid cursor scope identity")
	}
	grants := slices.Clone(p.Permissions)
	slices.Sort(grants)
	ceilings, constrained := p.TokenScopes()
	scopeHash, err := hashCursorPart(struct {
		Scope, ID                                            string
		Version                                              uint64
		Authenticated, Active, Staff, Superuser, Constrained bool
		Grants, Ceilings                                     []string
	}{scope, p.ID, p.AuthVersion, p.Authenticated, p.Active, p.Staff, p.Superuser, constrained, grants, ceilings})
	if err != nil {
		return pagination.CursorBinding{}, err
	}
	filtered := url.Values{}
	for key, items := range values {
		if key != "cursor" && key != "page_size" {
			filtered[key] = slices.Clone(items)
		}
	}
	queryHash, err := hashCursorPart(struct {
		Model, Query string
		Order        []string
		Size         int
	}{s.schema.Key(), filtered.Encode(), options.Ordering, page.Size})
	if err != nil {
		return pagination.CursorBinding{}, err
	}
	return pagination.CursorBinding{Resource: s.cursor.name, Version: s.cursor.version, Scope: scopeHash, Query: queryHash}, nil
}

func (s *Resource) cursorList(request resourceRequest, values url.Values, options QueryOptions, page pagination.Page, query orm.Query[*models.MapRecord]) (Collection, error) {
	binding, err := s.cursorBinding(request, values, options, page)
	if err != nil {
		return Collection{}, err
	}
	filtered := query
	if token := values.Get("cursor"); token != "" {
		position, err := s.cursor.codec.Decode(binding, token)
		if err != nil {
			return Collection{}, invalidCursor()
		}
		predicate, err := s.cursorPredicate(request.ctx, options.Ordering, position)
		if err != nil {
			return Collection{}, err
		}
		filtered = filtered.Filter(predicate)
	}
	result := Collection{Results: []Values{}}
	if s.config.IncludeCount {
		count, err := query.Count(request.ctx)
		if err != nil {
			return Collection{}, err
		}
		result.Count = &count
	}
	rows, err := filtered.Limit(page.Size + 1).All(request.ctx)
	if err != nil {
		return Collection{}, err
	}
	hasMore := len(rows) > page.Size
	if hasMore {
		rows = rows[:page.Size]
	}
	for _, row := range rows {
		value, err := s.represent(request, row, false)
		if err != nil {
			return Collection{}, err
		}
		result.Results = append(result.Results, value)
	}
	if hasMore && len(rows) > 0 {
		// The position is signed, not encrypted. Take only direct, already
		// authorized serializer output; a field policy may disable navigation
		// by denying a key, but must never leak its underlying stored value.
		last := result.Results[len(result.Results)-1]
		position := make([]json.RawMessage, 0, len(options.Ordering))
		for _, order := range options.Ordering {
			name := strings.TrimPrefix(order, "-")
			publicName := s.cursor.fields[name]
			value, ok := last[publicName]
			if !ok || value == nil {
				return Collection{}, auth.ErrPermissionDenied
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return Collection{}, err
			}
			position = append(position, encoded)
		}
		token, err := s.cursor.codec.Encode(binding, position)
		if err != nil {
			return Collection{}, err
		}
		next := url.Values{}
		for key, items := range values {
			next[key] = slices.Clone(items)
		}
		next.Set("cursor", token)
		result.Next = "?" + next.Encode()
		if err := validateNavigation(result.Next, ""); err != nil {
			return Collection{}, err
		}
	}
	return result, request.ctx.Err()
}

func invalidCursor() error {
	return mediaError(400, "INVALID_CURSOR", "Cursor is invalid for this query")
}
func (s *Resource) cursorPredicate(ctx context.Context, ordering []string, position []json.RawMessage) (db.Predicate, error) {
	if len(position) != len(ordering) || len(ordering) == 0 || len(ordering) > 16 {
		return db.Predicate{}, invalidCursor()
	}
	values := make([]any, len(position))
	for i, raw := range position {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil || value == nil {
			return db.Predicate{}, invalidCursor()
		}
		field, _ := s.schema.Field(strings.TrimPrefix(ordering[i], "-"))
		cleaned, err := cleanQueryValue(ctx, field, value)
		if err != nil {
			if ctx.Err() != nil {
				return db.Predicate{}, ctx.Err()
			}
			return db.Predicate{}, invalidCursor()
		}
		values[i] = cleaned
	}
	var alternatives []db.Predicate
	for i, order := range ordering {
		var all []db.Predicate
		for before := 0; before < i; before++ {
			all = append(all, orm.Q(strings.TrimPrefix(ordering[before], "-"), values[before]))
		}
		lookup := "gt"
		if strings.HasPrefix(order, "-") {
			lookup = "lt"
		}
		all = append(all, orm.Q(strings.TrimPrefix(order, "-")+"__"+lookup, values[i]))
		alternatives = append(alternatives, orm.And(all...))
	}
	return orm.Or(alternatives...), nil
}
