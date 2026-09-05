package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/templates"
)

var errListMutationUnknown = errors.New("admin: list mutation outcome is unknown")

func invokeListEdit(fn func() (*listEditState, error)) (state *listEditState, err error) {
	defer func() {
		if recover() != nil {
			state, err = nil, errListMutationUnknown
		}
	}()
	return fn()
}

// prepareListEdit returns done when it already wrote an error/redirect. Invalid
// field forms instead return their complete state to the ordinary list renderer.
func (s *Site) prepareListEdit(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore, objects []Object) (result *listEditState, done bool) {
	defer func() {
		if recover() != nil {
			// Preparation is read-only. Mutation panics have a distinct unknown
			// outcome boundary in invokeListEdit, including a panicking Store.
			result, done = nil, true
			s.failure(w, r, errors.New("admin: list preparation unavailable"))
		}
	}()
	if len(options.ListEditable) == 0 {
		return nil, false
	}
	if err := s.allowed(r.Context(), p, "change", options, Object{}); err != nil {
		if r.Method != "POST" && errors.Is(err, auth.ErrPermissionDenied) {
			return nil, false
		}
		s.failure(w, r, err)
		return nil, true
	}
	if len(objects) == 0 {
		if r.Method == "POST" {
			s.failure(w, r, ErrConflict)
			return nil, true
		}
		return nil, false
	}
	var token listEditToken
	var err error
	if r.Method == "POST" {
		token, err = s.checkListToken(r.Context(), p, options, r.URL.Query(), r.PostForm, objects)
		if errors.Is(err, errInvalidListManagement) {
			http.Error(w, "Invalid list management data", 400)
			return nil, true
		}
		if err != nil {
			s.failure(w, r, err)
			return nil, true
		}
	}
	signed, err := s.listToken(p, options, r.URL.Query(), objects)
	if err != nil {
		s.failure(w, r, err)
		return nil, true
	}
	var posted url.Values
	if r.Method == "POST" {
		posted = r.PostForm
	}
	state, err := s.bindListEdit(r.Context(), p, options, objects, posted)
	if err != nil {
		s.failure(w, r, err)
		return nil, true
	}
	state.token = signed
	// Do not render a pre-callback page after a callback changed trusted scope
	// or a persisted sibling. Invalid forms keep submitted widgets, but the
	// identities and DB snapshots still refer to the original authorized page.
	if r.Method != "POST" || !state.valid {
		fresh, err := s.currentListScope(r.Context(), p, options)
		if err != nil {
			s.failure(w, r, err)
			return nil, true
		}
		raw, err := s.config.Signer.Verify(signed, time.Hour)
		var initial listEditToken
		if err != nil || json.Unmarshal(raw, &initial) != nil {
			s.failure(w, r, ErrConflict)
			return nil, true
		}
		for _, row := range initial.Rows {
			current, err := fresh.Get(r.Context(), row.ID, false)
			if err == nil {
				err = sameListObject(current, row)
			}
			if err != nil {
				s.failure(w, r, err)
				return nil, true
			}
		}
	}
	if r.Method != "POST" || !state.valid {
		return state, false
	}
	state, err = invokeListEdit(func() (*listEditState, error) {
		return s.saveListEdit(r, p, options, store, token)
	})
	if errors.Is(err, errInvalidForm) && state != nil {
		state.token = signed
		return state, false
	}
	outcome := "unchanged"
	if state != nil {
		outcome = state.outcome
	} else if errors.Is(err, errListMutationUnknown) {
		outcome = "unknown"
	}
	if err != nil && (outcome == "changed" || outcome == "unknown") {
		w.Header().Set("X-Gogo-List-Change", outcome)
		http.Error(w, "List change outcome requires review. Do not repeat automatically.", 503)
		return nil, true
	}
	if err != nil {
		s.failure(w, r, err)
		return nil, true
	}
	if state.saved > 0 {
		w.Header().Set("X-Gogo-List-Change", "changed")
		s.successMessage(r, "The changed records were saved successfully.")
	}
	target := s.modelURL(options)
	if query := r.URL.Query().Encode(); query != "" {
		target += "?" + query
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return nil, true
}

func listFormProblems(state *listEditState, fields []string) []any {
	if state == nil || state.valid {
		return nil
	}
	problems := []any{}
	for index, form := range state.forms {
		for name, values := range form.Errors() {
			if slices.Contains(fields, name) {
				continue // Displayed next to its cell widget.
			}
			for _, value := range values {
				problems = append(problems, templates.Context{"label": state.objects[index].Label, "message": value.Message})
			}
		}
	}
	return problems
}
