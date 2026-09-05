package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
)

// Autocomplete returns a bounded page of choices from a declared source relation.
// No total count is exposed. Eligibility is rechecked with the same resolver that
// validates posted ModelForm IDs; a query parameter never names a target model.
func (s *Site) autocomplete(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	values := r.URL.Query()
	for name, items := range values {
		if !slices.Contains([]string{"app_label", "model_name", "field_name", "term", "page", "object_id"}, name) || len(items) != 1 || len(items[0]) > 4096 {
			http.Error(w, "Invalid related lookup", 400)
			return
		}
	}
	term := values.Get("term")
	if len(term) > 256 {
		http.Error(w, "Search is too long", 400)
		return
	}
	page := 1
	if raw := values.Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			http.Error(w, "Invalid page", 400)
			return
		}
		page = parsed
	}
	var source ModelAdmin
	found := false
	for _, candidate := range s.models {
		if candidate.Schema.AppLabel == values.Get("app_label") && strings.ToLower(candidate.Schema.Name) == values.Get("model_name") {
			source = candidate
			found = true
			break
		}
	}
	fieldName := values.Get("field_name")
	if !found || !slices.Contains(source.AutocompleteFields, fieldName) {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	field, ok := source.Schema.Field(fieldName)
	if !ok || field.Relation == nil || source.ResolveRelation == nil {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	object := Object{}
	action := "add"
	sourceStore, err := s.config.Store.Scope(r.Context(), p, s.config.Name, source.Schema)
	if err != nil || sourceStore == nil {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	if id := values.Get("object_id"); id != "" {
		object, err = sourceStore.Get(r.Context(), id, false)
		if err != nil {
			s.failure(w, r, auth.ErrPermissionDenied)
			return
		}
		action = "change"
	} else {
		if s.allowed(r.Context(), p, action, source, object) != nil {
			s.failure(w, r, auth.ErrPermissionDenied)
			return
		}
		object, err = sourceStore.New(r.Context())
		if err != nil {
			s.failure(w, r, err)
			return
		}
	}
	readonly := append([]string(nil), source.ReadonlyFields...)
	if source.GetReadonlyFields != nil {
		readonly = append(readonly, source.GetReadonlyFields(r.Context(), object)...)
	}
	if slices.Contains(readonly, fieldName) || s.allowed(r.Context(), p, action, source, object) != nil {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	target, ok := s.models[field.Relation.Target]
	if !ok || len(target.SearchFields) == 0 {
		s.failure(w, r, errors.New("admin: relation target search is not configured"))
		return
	}
	if s.allowed(r.Context(), p, "view", target, Object{}) != nil {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	store, err := s.config.Store.Scope(r.Context(), p, s.config.Name, target.Schema)
	if err != nil || store == nil {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	const limit = 20
	ordering := append([]string(nil), target.Ordering...)
	for _, pk := range target.Schema.PKFields() {
		if !slices.Contains(ordering, pk.Name) && !slices.Contains(ordering, "-"+pk.Name) {
			ordering = append(ordering, pk.Name)
		}
	}
	type choice struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	results := []choice{}
	// Pages count eligible objects, not candidates denied by object policy or
	// the source relation resolver. Bound scanning and never expose hidden totals.
	const batch, maxScan = 100, 10000
	eligible, finished := 0, false
	for offset := 0; offset < maxScan; offset += batch {
		rows, err := store.List(r.Context(), ListQuery{Search: term, SearchFields: target.SearchFields, Ordering: ordering, Offset: offset, Limit: batch})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		if len(rows.Objects) > batch {
			s.failure(w, r, errors.New("admin: invalid relation page"))
			return
		}
		for _, candidate := range rows.Objects {
			if candidate.Record == nil || candidate.Record.Schema().Key() != target.Schema.Key() {
				s.failure(w, r, errors.New("admin: invalid scoped relation object"))
				return
			}
			if s.allowed(r.Context(), p, "view", target, candidate) != nil {
				continue
			}
			id, err := relationChoiceID(field, candidate)
			if err != nil {
				s.failure(w, r, err)
				return
			}
			resolved, err := source.ResolveRelation(r.Context(), field, []string{id})
			if err != nil || len(resolved) != 1 {
				continue
			}
			eligible++
			if eligible > (page-1)*limit {
				results = append(results, choice{ID: id, Text: candidate.Label})
			}
			if len(results) > limit {
				finished = true
				break
			}
		}
		if len(rows.Objects) < batch {
			finished = true
		}
		if finished {
			break
		}
	}
	if !finished {
		http.Error(w, "Narrow your related-object search", http.StatusBadRequest)
		return
	}
	more := len(results) > limit
	if more {
		results = results[:limit]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == "HEAD" {
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Results    []choice `json:"results"`
		Pagination struct {
			More bool `json:"more"`
		} `json:"pagination"`
	}{Results: results, Pagination: struct {
		More bool `json:"more"`
	}{More: more}})
}

func relationChoiceID(field models.Field, object Object) (string, error) {
	keys := field.Relation.TargetFields
	if len(keys) == 0 {
		for _, pk := range object.Record.Schema().PKFields() {
			keys = append(keys, pk.Name)
		}
	}
	if len(keys) == 1 {
		value, err := object.Record.Get(keys[0])
		if err != nil {
			return "", err
		}
		return fmt.Sprint(value), nil
	}
	// Composite relation resolvers consume the same opaque, codec-preserving key
	// used by scoped Admin object lookup rather than lossy string concatenation.
	return object.ID, nil
}
