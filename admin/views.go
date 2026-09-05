package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/messages"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

func (s *Site) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if !strings.HasPrefix(r.URL.Path, s.config.Prefix) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, s.config.Prefix)
	if body, version, contentType, legacy, ok := s.asset(r.URL.Path); ok {
		if r.Method != "GET" && r.Method != "HEAD" {
			s.method(w)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, no-cache")
		if !legacy {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		etag := `"` + version + `"`
		w.Header().Set("ETag", etag)
		for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
			candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(candidate), "W/"))
			if candidate == etag || candidate == "*" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		if r.Method != "HEAD" {
			_, _ = w.Write(body)
		}
		return
	}
	p := auth.FromContext(r.Context())
	if !p.Authenticated {
		if s.config.LoginURL != "" {
			login, _ := url.Parse(s.config.LoginURL)
			query := login.Query()
			query.Set("next", r.URL.RequestURI())
			login.RawQuery = query.Encode()
			http.Redirect(w, r, login.String(), http.StatusSeeOther)
		} else {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
		}
		return
	}
	if !p.Active || !p.Staff {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}
	if s.config.Messages {
		if _, err := messages.Peek(r); err != nil {
			s.failure(w, r, err)
			return
		}
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "POST" {
		s.method(w)
		return
	}
	if rest == "" {
		if r.Method == "POST" {
			s.method(w)
			return
		}
		s.index(w, r, p)
		return
	}
	if rest == "autocomplete" || rest == "autocomplete/" {
		if r.Method != "GET" && r.Method != "HEAD" {
			s.method(w)
			return
		}
		s.autocomplete(w, r, p)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || len(parts) > 4 {
		http.NotFound(w, r)
		return
	}
	var options ModelAdmin
	found := false
	for _, candidate := range s.models {
		if candidate.Schema.AppLabel == parts[0] && strings.ToLower(candidate.Schema.Name) == parts[1] {
			options = candidate
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	action := "view"
	if len(parts) == 3 && parts[2] == "add" {
		action = "add"
	}
	if options.userForms && len(parts) == 4 && parts[3] == "password" {
		action = "change"
	}
	if r.Method == "POST" && len(parts) == 4 {
		action = "change"
		if parts[3] == "delete" {
			action = "delete"
		}
	}
	if err := s.allowed(r.Context(), p, action, options, Object{}); err != nil {
		s.failure(w, r, err)
		return
	}
	store, err := s.config.Store.Scope(r.Context(), p, s.config.Name, options.Schema)
	if err != nil || store == nil {
		s.failure(w, r, errors.New("scope unavailable"))
		return
	}
	switch {
	case len(parts) == 2:
		if r.Method == "POST" {
			s.action(w, r, p, options, store)
		} else {
			s.list(w, r, p, options, store)
		}
	case len(parts) == 3 && parts[2] == "add":
		if options.userForms {
			s.userCreateForm(w, r, p, options, store)
		} else {
			s.form(w, r, p, options, store, "")
		}
	case options.userForms && len(parts) == 4 && parts[3] == "password":
		s.userPasswordForm(w, r, p, options, store, parts[2])
	case len(parts) == 4 && parts[3] == "change":
		s.form(w, r, p, options, store, parts[2])
	case len(parts) == 4 && parts[3] == "delete":
		s.delete(w, r, p, options, store, parts[2])
	case len(parts) == 4 && parts[3] == "history":
		if r.Method == "POST" {
			s.method(w)
			return
		}
		s.history(w, r, p, options, store, parts[2])
	default:
		http.NotFound(w, r)
	}
}
func (s *Site) method(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, HEAD, POST")
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}
func (s *Site) failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, ErrConflict):
		http.Error(w, "This object changed. Reload before saving.", http.StatusConflict)
	case errors.Is(err, auth.ErrPermissionDenied):
		http.Error(w, "Permission denied", http.StatusForbidden)
	case errors.Is(err, auth.ErrUnauthenticated):
		http.Error(w, "Authentication required", http.StatusUnauthorized)
	default:
		http.Error(w, "Administration is temporarily unavailable", http.StatusServiceUnavailable)
	}
}
func (s *Site) modelURL(o ModelAdmin) string {
	return s.config.Prefix + url.PathEscape(o.Schema.AppLabel) + "/" + url.PathEscape(strings.ToLower(o.Schema.Name)) + "/"
}
func (s *Site) navigation(r *http.Request, p auth.Principal) []any {
	rows := []any{}
	keys := make([]string, 0, len(s.models))
	for key := range s.models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		options := s.models[key]
		view := s.allowed(r.Context(), p, "view", options, Object{}) == nil
		add := s.allowed(r.Context(), p, "add", options, Object{}) == nil
		if !view && !add {
			continue
		}
		name := options.Schema.LabelPlural
		if name == "" {
			name = options.Schema.Name
		}
		link := s.modelURL(options)
		if !view {
			link += "add/"
		}
		rows = append(rows, templates.Context{"label": name, "app": options.Schema.AppLabel, "url": link, "active": strings.HasPrefix(r.URL.Path, s.modelURL(options)), "add_url": s.modelURL(options) + "add/", "can_add": add, "can_view": view})
	}
	return rows
}
func (s *Site) render(w http.ResponseWriter, r *http.Request, p auth.Principal, name string, data templates.Context, status int) {
	data["header"] = s.config.Header
	data["site_title"] = s.config.Title
	data["prefix"] = s.config.Prefix
	data["css_url"] = s.config.Prefix + "assets/admin." + s.cssVersion + ".css"
	data["js_url"] = s.config.Prefix + "assets/admin." + s.jsVersion + ".js"
	data["is_overview"] = r.URL.Path == s.config.Prefix
	data["navigation"] = s.navigation(r, p)
	data["actor"] = p.ID
	data["csrf_token"] = security.CSRFToken(r)
	data["site_url"] = s.config.SiteURL
	data["logout_url"] = s.config.LogoutURL
	data["password_change_url"] = s.config.PasswordChangeURL
	if s.config.Messages {
		items, err := messages.Consume(r)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		rows := []any{}
		for _, message := range items {
			rows = append(rows, templates.Context{"level": message.LevelTag(), "text": message.Text, "tags": strings.Join(message.Tags, " ")})
		}
		data["messages"] = rows
	}
	body, err := s.engine.Render(r.Context(), name, data)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write([]byte(body))
	}
}
func (s *Site) index(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	s.render(w, r, p, "index.html", templates.Context{"title": s.config.IndexTitle, "models": s.navigation(r, p)}, http.StatusOK)
}

func (s *Site) list(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore) {
	query := ListQuery{Search: r.URL.Query().Get("q"), SearchFields: options.SearchFields, Filters: map[string]string{}, Ordering: options.Ordering, Limit: options.ListPerPage}
	if len(query.Search) > 256 || query.Search != "" && len(options.SearchFields) == 0 {
		http.Error(w, "Search is too long", 400)
		return
	}
	page := 1
	if text := r.URL.Query().Get("p"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > 1000000 {
			http.Error(w, "Invalid page", 400)
			return
		}
		page = value
	}
	query.Offset = (page - 1) * query.Limit
	if order := r.URL.Query().Get("o"); order != "" {
		field := strings.TrimPrefix(order, "-")
		if !slices.Contains(options.ListDisplay, field) {
			http.Error(w, "Invalid ordering", 400)
			return
		}
		if metadata, ok := options.Schema.Field(field); !ok || !metadata.IsStored() {
			http.Error(w, "Invalid ordering", 400)
			return
		}
		query.Ordering = []string{order}
	}
	for name, values := range r.URL.Query() {
		if name == "q" || name == "p" || name == "o" {
			continue
		}
		if !slices.Contains(options.ListFilter, name) || len(values) != 1 || len(values[0]) > 256 {
			http.Error(w, "Invalid filter", 400)
			return
		}
		if values[0] == "" {
			continue
		}
		query.Filters[name] = values[0]
	}
	for _, pk := range options.Schema.PKFields() {
		if !slices.Contains(query.Ordering, pk.Name) && !slices.Contains(query.Ordering, "-"+pk.Name) {
			query.Ordering = append(query.Ordering, pk.Name)
		}
	}
	result, err := store.List(r.Context(), query)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if len(result.Objects) > query.Limit || result.Count < 0 {
		s.failure(w, r, errors.New("invalid store page"))
		return
	}
	rows := []any{}
	for _, object := range result.Objects {
		if err := s.allowed(r.Context(), p, "view", options, object); err != nil {
			s.failure(w, r, err)
			return
		}
		cells := []any{}
		for index, name := range options.ListDisplay {
			var value any
			found := false
			for _, column := range options.Columns {
				if column.Name == name {
					value, err = column.Value(r.Context(), object)
					found = true
					break
				}
			}
			if !found {
				value, err = object.Record.Get(name)
			}
			if err != nil {
				s.failure(w, r, err)
				return
			}
			link := ""
			if options.ListDisplayLinks == nil && index == 0 || slices.Contains(options.ListDisplayLinks, name) {
				link = s.modelURL(options) + url.PathEscape(object.ID) + "/change/"
			}
			cells = append(cells, templates.Context{"value": value, "url": link})
		}
		rows = append(rows, templates.Context{"id": object.ID, "url": s.modelURL(options) + url.PathEscape(object.ID) + "/change/", "cells": cells})
	}
	columns := []any{}
	for _, name := range options.ListDisplay {
		label := name
		if field, ok := options.Schema.Field(name); ok && field.Label != "" {
			label = field.Label
		}
		for _, column := range options.Columns {
			if column.Name == name && column.Label != "" {
				label = column.Label
			}
		}
		sortURL := ""
		if field, ok := options.Schema.Field(name); ok && field.IsStored() {
			values := r.URL.Query()
			order := name
			if len(query.Ordering) > 0 && query.Ordering[0] == name {
				order = "-" + name
			}
			values.Set("o", order)
			values.Del("p")
			sortURL = "?" + values.Encode()
		}
		direction := "none"
		if len(query.Ordering) > 0 {
			if query.Ordering[0] == name {
				direction = "ascending"
			} else if query.Ordering[0] == "-"+name {
				direction = "descending"
			}
		}
		columns = append(columns, templates.Context{"label": label, "url": sortURL, "direction": direction})
	}
	filters := []any{}
	for _, name := range options.ListFilter {
		field, _ := options.Schema.Field(name)
		label := field.Label
		if label == "" {
			label = name
		}
		choices := []any{}
		for _, choice := range field.Choices {
			value := fmt.Sprint(choice.Value)
			choices = append(choices, templates.Context{"value": value, "label": choice.Label, "selected": query.Filters[name] == value})
		}
		if field.Kind == models.Boolean {
			for _, choice := range []struct{ value, label string }{{"true", "Yes"}, {"false", "No"}} {
				choices = append(choices, templates.Context{"value": choice.value, "label": choice.label, "selected": query.Filters[name] == choice.value})
			}
		}
		filters = append(filters, templates.Context{"name": name, "label": label, "value": query.Filters[name], "choices": choices})
	}
	actions := []any{}
	for _, action := range options.Actions {
		if s.allowed(r.Context(), p, action.Permission, options, Object{}) == nil {
			actions = append(actions, templates.Context{"name": action.Name, "label": action.Description})
		}
	}
	pageURL := func(next int) string {
		values := r.URL.Query()
		values.Set("p", strconv.Itoa(next))
		return "?" + values.Encode()
	}
	s.render(w, r, p, "list.html", templates.Context{"title": options.Schema.Name, "columns": columns, "rows": rows, "count": result.Count, "search": query.Search, "has_search": len(options.SearchFields) > 0, "filters": filters, "order": r.URL.Query().Get("o"), "page": page, "previous": pageURL(max(1, page-1)), "next": pageURL(page + 1), "has_previous": page > 1, "has_next": int64(query.Offset+len(rows)) < result.Count, "can_add": s.allowed(r.Context(), p, "add", options, Object{}) == nil, "add_url": s.modelURL(options) + "add/", "actions": actions}, 200)
}

type editToken struct{ Actor, Site, Model, ID, Version, Action string }

func (s *Site) token(p auth.Principal, o ModelAdmin, object Object, action string) (string, error) {
	payload, err := json.Marshal(editToken{p.ID, s.config.Name, o.Schema.Key(), object.ID, object.Version, action})
	if err != nil {
		return "", err
	}
	return s.config.Signer.Sign(payload)
}
func (s *Site) checkToken(token string, p auth.Principal, o ModelAdmin, object Object, action string) error {
	raw, err := s.config.Signer.Verify(token, time.Hour)
	if err != nil {
		return ErrConflict
	}
	var value editToken
	if json.Unmarshal(raw, &value) != nil || value != (editToken{p.ID, s.config.Name, o.Schema.Key(), object.ID, object.Version, action}) {
		return ErrConflict
	}
	return nil
}
func (s *Site) parsePost(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.ParseMultipartForm(2 << 20)
	}
	return r.ParseForm()
}
func (s *Site) form(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore, id string) {
	var object Object
	var err error
	if id == "" {
		object, err = store.New(r.Context())
	} else {
		object, err = store.Get(r.Context(), id, false)
	}
	if err != nil {
		s.failure(w, r, err)
		return
	}
	action := "change"
	originalVersion := object.Version
	selfAccount := false
	if options.userForms && id != "" {
		accountID, err := object.Record.Get("id")
		if err != nil {
			s.failure(w, r, err)
			return
		}
		selfAccount = accountID == p.ID
	}
	if id == "" {
		action = "add"
	}
	canChange := s.allowed(r.Context(), p, action, options, object) == nil
	if !canChange && s.allowed(r.Context(), p, "view", options, object) != nil {
		s.failure(w, r, auth.ErrPermissionDenied)
		return
	}
	readonly := append([]string(nil), options.ReadonlyFields...)
	if options.GetReadonlyFields != nil {
		readonly = append(readonly, options.GetReadonlyFields(r.Context(), object)...)
	}
	if !canChange {
		readonly = append(readonly, options.Fields...)
	}
	relationInitial, err := s.formRelations(r.Context(), p, options, object, store, readonly)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	makeForm := func(ctx context.Context, obj Object, bound bool) (*forms.ModelForm, error) {
		currentReadonly := append([]string(nil), options.ReadonlyFields...)
		if options.GetReadonlyFields != nil {
			currentReadonly = append(currentReadonly, options.GetReadonlyFields(ctx, obj)...)
		}
		if !canChange {
			currentReadonly = append(currentReadonly, options.Fields...)
		}
		opts := []forms.Option{}
		initial := map[string]any{}
		for name, values := range relationInitial {
			initial[name] = values
		}
		opts = append(opts, forms.WithInitial(initial))
		if bound {
			opts = append(opts, forms.WithData(r.PostForm))
			if r.MultipartForm != nil {
				opts = append(opts, forms.WithFiles(r.MultipartForm.File))
			}
		}
		overrides, err := s.relationOverrides(ctx, options, obj)
		if err != nil {
			return nil, err
		}
		if overrides == nil {
			overrides = map[string]forms.Field{}
		}
		for name, values := range relationInitial {
			field, ok := overrides[name]
			if !ok {
				metadata, _ := options.Schema.Field(name)
				field, err = forms.FieldFromModel(metadata)
				if err != nil {
					return nil, err
				}
			}
			if !slices.Contains(options.AutocompleteFields, name) && len(field.Choices) == 0 {
				field.Choices, err = s.relationChoices(ctx, p, options, name)
				if err != nil {
					return nil, err
				}
			}
			for _, value := range values {
				id := fmt.Sprint(value)
				if !slices.ContainsFunc(field.Choices, func(choice forms.Choice) bool { return choice.Value == id }) {
					field.Choices = append(field.Choices, forms.Choice{Value: id, Label: id})
				}
			}
			overrides[name] = field
		}
		return forms.NewModelForm(ctx, obj.Record, forms.ModelFormOptions{Fields: options.Fields, Exclude: options.Exclude, Readonly: currentReadonly, Overrides: overrides, ResolveRelation: options.ResolveRelation, Checker: options.ConstraintChecker}, opts...)
	}
	var modelForm *forms.ModelForm
	var inlines []*inlineState
	invalidForm := false
	if r.Method == "POST" {
		if options.userForms || options.groupForms {
			w.Header().Set("X-Gogo-Account-Change", string(auth.PasswordUnchanged))
		}
		if !canChange {
			s.failure(w, r, auth.ErrPermissionDenied)
			return
		}
		if err = s.parsePost(w, r); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if r.PostForm.Has("_addanother") && s.allowed(r.Context(), p, "add", options, Object{}) != nil {
			s.failure(w, r, auth.ErrPermissionDenied)
			return
		}
		save := func() error {
			return store.Atomic(r.Context(), func(ctx context.Context) error {
				if id != "" {
					var err error
					object, err = store.Get(ctx, id, true)
					if err != nil {
						return err
					}
				}
				if err := s.allowed(ctx, p, action, options, object); err != nil {
					return err
				}
				if err := s.checkToken(r.PostForm.Get("_edit_token"), p, options, object, action); err != nil {
					return err
				}
				currentReadonly := append([]string(nil), options.ReadonlyFields...)
				if options.GetReadonlyFields != nil {
					currentReadonly = append(currentReadonly, options.GetReadonlyFields(ctx, object)...)
				}
				relationInitial, err = s.formRelations(ctx, p, options, object, store, currentReadonly)
				if err != nil {
					return err
				}
				if len(relationInitial) > 0 {
					relationObject, err := relationVersion(object, relationInitial)
					if err != nil {
						return err
					}
					if err := s.checkToken(r.PostForm.Get("_relation_token"), p, options, relationObject, "relations:"+action); err != nil {
						return err
					}
				}
				before, snapshotErr := snapshot(options, object)
				if snapshotErr != nil {
					return snapshotErr
				}
				if err := addRelationSnapshot(before, options, relationInitial); err != nil {
					return err
				}
				var err error
				modelForm, err = makeForm(ctx, object, true)
				if err != nil {
					return err
				}
				parentValid := modelForm.IsValid()
				inlines, err = s.loadInlines(ctx, r, p, options, object, true)
				if err != nil {
					return err
				}
				if !parentValid || !validInlines(inlines) {
					return errInvalidForm
				}
				if options.SaveModel != nil {
					object, err = options.SaveModel(ctx, store, object)
				} else {
					object, err = store.Save(ctx, object)
				}
				if err != nil {
					return err
				}
				if err = s.saveFormRelations(ctx, p, options, store, object, action, modelForm); err != nil {
					return err
				}
				if err = s.saveInlines(ctx, p, object, inlines); err != nil {
					return err
				}
				if options.SaveRelated != nil {
					if err = options.SaveRelated(ctx, store, object, r); err != nil {
						return err
					}
				}
				after, snapshotErr := snapshot(options, object)
				if snapshotErr != nil {
					return snapshotErr
				}
				relationAfter, err := s.formRelations(ctx, p, options, object, store, currentReadonly)
				if err != nil {
					return err
				}
				if err = addRelationSnapshot(after, options, relationAfter); err != nil {
					return err
				}
				return store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ObjectID: object.ID, ObjectLabel: object.Label, Action: action, Changes: diff(before, after), At: time.Now().UTC()})
			})
		}
		if options.userForms || options.groupForms {
			err = invokeAccountMutation(save)
			state := accountOutcome(err)
			if err == nil && object.Version == originalVersion {
				state = auth.PasswordUnchanged
			}
			w.Header().Set("X-Gogo-Account-Change", string(state))
			if selfAccount && state != auth.PasswordUnchanged {
				logoutErr := s.invalidateEditedIdentity(w, r)
				if err != nil || logoutErr != nil {
					http.Error(w, "Account change outcome requires a fresh login or account review. Do not repeat automatically.", http.StatusServiceUnavailable)
					return
				}
				s.successMessage(r, "The account was changed. Sign in again to continue.")
				target := s.config.LoginURL
				if target == "" {
					target = s.config.Prefix
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
			if err != nil && state != auth.PasswordUnchanged {
				http.Error(w, "Account change outcome requires review. Do not repeat automatically.", http.StatusServiceUnavailable)
				return
			}
		} else {
			err = save()
		}
		if err == nil {
			s.successMessage(r, "The record was saved successfully.")
			target := s.modelURL(options)
			if r.PostForm.Has("_continue") {
				target += url.PathEscape(object.ID) + "/change/"
			} else if r.PostForm.Has("_addanother") {
				if s.allowed(r.Context(), p, "add", options, Object{}) != nil {
					s.failure(w, r, auth.ErrPermissionDenied)
					return
				}
				target += "add/"
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if !errors.Is(err, errInvalidForm) {
			s.failure(w, r, err)
			return
		}
		invalidForm = true
	}
	if modelForm == nil {
		modelForm, err = makeForm(r.Context(), object, false)
		if err != nil {
			s.failure(w, r, err)
			return
		}
	}
	readonlyDisplay, err := s.readonlyValues(r.Context(), p, options, object, store, readonly)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	formHTML, err := s.renderModelForm(r.Context(), options, object, modelForm, readonly, readonlyDisplay)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if inlines == nil {
		inlines, err = s.loadInlines(r.Context(), r, p, options, object, false)
		if err != nil {
			s.failure(w, r, err)
			return
		}
	}
	inlineHTML, err := s.renderInlines(r.Context(), p, object, inlines)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	token, err := s.token(p, options, object, action)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	relationToken := ""
	if len(relationInitial) > 0 {
		relationObject, err := relationVersion(object, relationInitial)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		relationToken, err = s.token(p, options, relationObject, "relations:"+action)
		if err != nil {
			s.failure(w, r, err)
			return
		}
	}
	readonlyValues := []any{}
	for _, name := range readonly {
		if len(options.Fieldsets) > 0 {
			continue
		}
		value, ok := readonlyDisplay[name]
		if !ok {
			continue
		}
		readonlyValues = append(readonlyValues, templates.Context{"label": name, "value": value})
	}
	title := object.Label
	if id == "" {
		title = "Add " + options.Schema.Name
	}
	status := 200
	if invalidForm {
		status = 400
	}
	s.render(w, r, p, "form.html", templates.Context{"title": title, "form": formHTML, "inlines": inlineHTML, "readonly": readonlyValues, "edit_token": token, "relation_token": relationToken, "can_change": canChange, "can_add": s.allowed(r.Context(), p, "add", options, Object{}) == nil, "can_delete": id != "" && s.allowed(r.Context(), p, "delete", options, object) == nil, "delete_url": s.modelURL(options) + url.PathEscape(id) + "/delete/", "history_url": s.modelURL(options) + url.PathEscape(id) + "/history/", "has_object": id != "", "can_manage_password": options.userForms && id != "" && canChange, "user_password_url": s.modelURL(options) + url.PathEscape(id) + "/password/"}, status)
}

var errInvalidForm = errors.New("invalid form")

func (s *Site) successMessage(r *http.Request, text string) {
	if s.config.Messages {
		// The database transaction is already committed. A bounded/full message
		// queue must not turn a durable write into a retryable HTTP failure.
		_ = messages.AddPublic(r, messages.Success, text)
	}
}

func snapshot(options ModelAdmin, object Object) (map[string]any, error) {
	values := map[string]any{}
	for _, field := range options.Schema.Fields {
		if !field.IsStored() {
			continue
		}
		if sensitiveField(options, field.Name) {
			continue
		}
		value, err := object.Record.Get(field.Name)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		var copy any
		if err = decoder.Decode(&copy); err != nil {
			return nil, err
		}
		values[field.Name] = copy
	}
	return values, nil
}

func sensitiveField(options ModelAdmin, field string) bool {
	name := strings.ToLower(field)
	return slices.Contains(options.SensitiveFields, field) || strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "token")
}
func diff(before, after map[string]any) map[string]Change {
	result := map[string]Change{}
	for name, value := range after {
		if !reflect.DeepEqual(before[name], value) {
			result[name] = Change{before[name], value}
		}
	}
	return result
}
