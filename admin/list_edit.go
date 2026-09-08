package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func validateListEditable(options ModelAdmin) error {
	if len(options.ListEditable) == 0 {
		return nil
	}
	if accountModel(options.Schema) {
		return errors.New("admin: account changelist editing requires a separate domain-backed formset")
	}
	seen := map[string]bool{}
	for _, name := range options.ListEditable {
		field, ok := options.Schema.Field(name)
		linked := slices.Contains(options.ListDisplayLinks, name) || options.ListDisplayLinks == nil && len(options.ListDisplay) > 0 && options.ListDisplay[0] == name
		if !ok || !field.IsStored() || !field.IsEditable() || field.PrimaryKey || field.Relation != nil || field.Kind == models.File || field.Kind == models.Image || !slices.Contains(options.Fields, name) || !slices.Contains(options.ListDisplay, name) || linked || seen[name] || slices.Contains(options.ReadonlyFields, name) || slices.Contains(options.Exclude, name) || slices.Contains(options.SensitiveFields, name) {
			return errors.New("admin: ListEditable requires distinct declared editable scalar display fields outside links, readonly, excluded and sensitive fields")
		}
		if slices.ContainsFunc(options.Columns, func(column DisplayColumn) bool { return column.Name == name }) {
			return errors.New("admin: editable list columns cannot use custom display callbacks")
		}
		if override, ok := options.FormOverrides[name]; ok && (override.Kind == forms.File || override.Kind == forms.Image || override.Kind == forms.ModelChoice || override.Kind == forms.ModelMultipleChoice) {
			return errors.New("admin: list form overrides cannot introduce uploads or model relation selectors")
		}
		seen[name] = true
	}
	return nil
}

type listEditRowToken struct {
	ID, Version, Snapshot string
}

// The signature contains only public row identities and digests, not search,
// filter values, submitted fields, labels or raw provider version strings.
type listEditToken struct {
	Actor, Site, Model, Query string
	Fields                    []string
	Rows                      []listEditRowToken
}

func listDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func listRowToken(object Object) (listEditRowToken, error) {
	if object.ID == "" || object.Record == nil || object.Record.State() == nil || !object.Record.State().Persisted {
		return listEditRowToken{}, errors.New("admin: persisted list object required")
	}
	// Snapshot all stored fields for callback/sibling fences, independently of
	// the Store's opaque optimistic version and presentation-only Label.
	canonical, err := objectFromRecord(object.Record)
	if err != nil {
		return listEditRowToken{}, err
	}
	schema, err := object.Record.Schema().Fingerprint()
	if err != nil {
		return listEditRowToken{}, err
	}
	return listEditRowToken{ID: object.ID, Version: listDigest(object.Version), Snapshot: listDigest(canonical.Version + ":" + schema + ":" + object.Record.State().Database)}, nil
}

func (s *Site) listToken(p auth.Principal, options ModelAdmin, query url.Values, objects []Object) (string, error) {
	limit, err := listEditableLimit(options, query)
	if err != nil || len(objects) == 0 || len(objects) > limit || len(objects) > 1000 {
		return "", errors.New("admin: invalid editable page size")
	}
	payload := listEditToken{Actor: p.ID, Site: s.config.Name, Model: options.Schema.Key(), Query: listDigest(query.Encode()), Fields: slices.Clone(options.ListEditable)}
	seen := map[string]bool{}
	for _, object := range objects {
		row, err := listRowToken(object)
		if err != nil || seen[row.ID] {
			return "", errors.New("admin: invalid editable page identity")
		}
		seen[row.ID] = true
		payload.Rows = append(payload.Rows, row)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.config.Signer.Sign(encoded)
}

func (s *Site) checkListToken(ctx context.Context, p auth.Principal, options ModelAdmin, query, posted url.Values, objects []Object) (listEditToken, error) {
	if err := ctx.Err(); err != nil {
		return listEditToken{}, err
	}
	limit, err := listEditableLimit(options, query)
	if err != nil || len(posted["_list_token"]) != 1 || len(posted["_save_list"]) != 1 || posted.Get("_save_list") != "1" || len(posted["action"]) > 1 || posted.Get("action") != "" || len(objects) == 0 || len(objects) > limit || len(objects) > 1000 {
		return listEditToken{}, errInvalidListManagement
	}
	raw, err := s.config.Signer.Verify(posted.Get("_list_token"), time.Hour)
	if err != nil {
		return listEditToken{}, ErrConflict
	}
	var token listEditToken
	if json.Unmarshal(raw, &token) != nil || token.Actor != p.ID || token.Site != s.config.Name || token.Model != options.Schema.Key() || token.Query != listDigest(query.Encode()) || !slices.Equal(token.Fields, options.ListEditable) || len(token.Rows) != len(objects) {
		return listEditToken{}, ErrConflict
	}
	for _, name := range []string{"form-TOTAL_FORMS", "form-INITIAL_FORMS"} {
		if len(posted[name]) != 1 || posted.Get(name) != strconv.Itoa(len(objects)) {
			return listEditToken{}, errInvalidListManagement
		}
	}
	seen := map[string]bool{}
	identities := map[string]bool{}
	for index, row := range token.Rows {
		key := "form-" + strconv.Itoa(index) + "-_id"
		identities[key] = true
		if len(posted[key]) != 1 || posted.Get(key) != row.ID || row.ID == "" || seen[row.ID] {
			return listEditToken{}, errInvalidListManagement
		}
		seen[row.ID] = true
		current, err := listRowToken(objects[index])
		if err != nil || current != row {
			return listEditToken{}, ErrConflict
		}
	}
	for name := range posted {
		if strings.HasPrefix(name, "form-") && strings.HasSuffix(name, "-_id") && !identities[name] {
			return listEditToken{}, errInvalidListManagement
		}
	}
	return token, nil
}

var errInvalidListManagement = errors.New("admin: invalid list management data")
