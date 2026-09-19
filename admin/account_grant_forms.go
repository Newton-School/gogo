package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
)

func stockGrantField(options ModelAdmin, name string) bool {
	return options.userForms && (name == string(UserGroups) || name == string(UserPermissions)) || options.groupForms && name == string(GroupPermissions)
}

func accountModelFields(options ModelAdmin, names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		if !stockGrantField(options, name) {
			result = append(result, name)
		}
	}
	return result
}

func grantFieldLabel(kind AccountGrantKind) string {
	switch kind {
	case UserGroups:
		return "Groups"
	case UserPermissions:
		return "User permissions"
	default:
		return "Permissions"
	}
}

const grantHelp = "Only visible choices are shown. Existing hidden grants are retained. Account-management authority must approve every change."

type accountGrantForm struct {
	form     *forms.Form
	before   map[AccountGrantKind][]string
	readonly map[string]any
}

func (s *Site) accountGrantAuthority(p auth.Principal) AccountGrantAuthorizer {
	return func(ctx context.Context, kind AccountGrantKind, object Object) error {
		descriptor, err := describeGrant(kind)
		if err != nil {
			return err
		}
		options, registered := s.models[descriptor.target.Key()]
		if !registered {
			// Selector permission does not require a separate browsing page.
			// A registered target may add its stricter object-specific policy.
			options = ModelAdmin{Schema: descriptor.target}
		}
		if object.Record != nil && object.Record.Schema().Key() != descriptor.target.Key() {
			return auth.ErrPermissionDenied
		}
		return s.allowed(ctx, p, "view", options, object)
	}
}

func (s *Site) loadAccountGrantForm(ctx context.Context, p auth.Principal, options ModelAdmin, object Object, store ScopedStore, readonly []string, data url.Values) (*accountGrantForm, error) {
	state := &accountGrantForm{before: map[AccountGrantKind][]string{}, readonly: map[string]any{}}
	var fields []forms.Field
	initial := map[string]any{}
	for _, name := range options.Fields {
		if !stockGrantField(options, name) || slices.Contains(options.Exclude, name) {
			continue
		}
		kind := AccountGrantKind(name)
		authorize := s.accountGrantAuthority(p)
		// Model denial never triggers a target scope or relationship lookup.
		if err := checkGrantModel(ctx, kind, authorize); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				continue
			}
			return nil, err
		}
		reader, ok := store.(AccountGrantReader)
		if !ok {
			return nil, errors.New("admin: scoped account grant reader required")
		}
		choices, err := reader.GrantChoices(ctx, object, kind, authorize)
		if err != nil {
			return nil, err
		}
		if len(choices.Choices) > maxAccountGrantChoices || len(choices.Selected) > maxAccountGrantChoices {
			return nil, errors.New("admin: account selector bound exceeded")
		}
		seen := map[string]bool{}
		for _, choice := range choices.Choices {
			if seen[choice.Value] || choice.Value == "" {
				return nil, errors.New("admin: invalid account selector choices")
			}
			seen[choice.Value] = true
		}
		for _, id := range choices.Selected {
			if !seen[id] {
				return nil, errors.New("admin: account selector includes an undisplayed identity")
			}
		}
		if slices.Contains(readonly, name) {
			labels := []string{}
			for _, choice := range choices.Choices {
				if slices.Contains(choices.Selected, choice.Value) {
					labels = append(labels, choice.Label)
				}
			}
			state.readonly[name] = strings.Join(labels, ", ")
			continue
		}
		if _, ok := store.(AccountGrantEditor); !ok {
			return nil, errors.New("admin: transactional account grant editor required")
		}
		state.before[kind] = slices.Clone(choices.Selected)
		slices.Sort(state.before[kind])
		initial[name] = slices.Clone(state.before[kind])
		fields = append(fields, forms.Field{Name: name, Kind: forms.MultipleChoice, Label: grantFieldLabel(kind), HelpText: grantHelp, Choices: choices.Choices, Widget: forms.InputWidget{Type: "select-multiple", Attrs: map[string]string{"class": "selectfilter", "size": "8", "data-field-name": grantFieldLabel(kind), "data-is-stacked": "0"}}})
	}
	opts := []forms.Option{forms.WithContext(ctx), forms.WithInitial(initial)}
	if data != nil {
		opts = append(opts, forms.WithData(data))
	}
	var err error
	state.form, err = forms.New(fields, opts...)
	return state, err
}

func (f *accountGrantForm) version(object Object) (Object, error) {
	encoded, err := json.Marshal(f.before)
	if err != nil {
		return Object{}, err
	}
	sum := sha256.Sum256(encoded)
	object.Version = hex.EncodeToString(sum[:])
	return object, nil
}

func (f *accountGrantForm) selected() map[AccountGrantKind][]string {
	result := make(map[AccountGrantKind][]string, len(f.before))
	cleaned := f.form.CleanedData()
	for kind := range f.before {
		// Stock MultipleChoice fields are declared here without coercion or
		// application overrides, so their cleaned items are always strings.
		values, _ := cleaned[string(kind)].([]any)
		ids := make([]string, 0, len(values))
		for _, value := range values {
			ids = append(ids, value.(string))
		}
		result[kind] = ids
		slices.Sort(result[kind])
	}
	return result
}

func accountGrantSnapshot(values map[string]any, grants map[AccountGrantKind][]string) {
	for kind, ids := range grants {
		// These IDs are only the visible snapshot, never retained hidden links.
		values[string(kind)] = slices.Clone(ids)
	}
}

func (f *accountGrantForm) changed() bool {
	for kind, ids := range f.selected() {
		if !slices.Equal(ids, f.before[kind]) {
			return true
		}
	}
	return false
}
