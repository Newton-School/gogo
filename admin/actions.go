package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/templates"
)

func (s *Site) history(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore, id string) {
	if err := s.allowed(r.Context(), p, "history", options, Object{ID: id}); err != nil {
		s.failure(w, r, err)
		return
	}
	entries, err := store.History(r.Context(), id, 0, 100)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	rows := []any{}
	for _, entry := range entries {
		if entry.ObjectID != id || entry.Site != s.config.Name || entry.Model != options.Schema.Key() {
			s.failure(w, r, errors.New("invalid scoped history"))
			return
		}
		rows = append(rows, templates.Context{"at": entry.At.Format(time.RFC3339), "actor": entry.ActorID, "action": entry.Action, "label": entry.ObjectLabel})
	}
	s.render(w, r, p, "history.html", templates.Context{"title": "Object history", "entries": rows}, 200)
}
func (s *Site) delete(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore, id string) {
	object, err := store.Get(r.Context(), id, false)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if err = s.allowed(r.Context(), p, "delete", options, object); err != nil {
		s.failure(w, r, err)
		return
	}
	collector, ok := store.(DeleteCollector)
	if !ok {
		s.failure(w, r, errors.New("deletion collector unavailable"))
		return
	}
	deletion, err := collector.CollectDeletion(r.Context(), object)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	deletion, err = s.scopedDeletion(r.Context(), p, deletion, false)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if r.Method == "POST" {
		if err = s.parsePost(w, r); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if !r.PostForm.Has("_confirm") {
			http.Error(w, "Confirmation required", 400)
			return
		}
		err = store.Atomic(r.Context(), func(ctx context.Context) error {
			current, err := store.Get(ctx, id, true)
			if err != nil {
				return err
			}
			if err = s.checkToken(r.PostForm.Get("_edit_token"), p, options, current, "delete"); err != nil {
				return err
			}
			if err = s.allowed(ctx, p, "delete", options, current); err != nil {
				return err
			}
			graph, err := collector.CollectDeletion(ctx, current)
			if err != nil {
				return err
			}
			if len(graph.Protected) > 0 {
				return ErrConflict
			}
			if _, err = s.scopedDeletion(ctx, p, graph, true); err != nil {
				return err
			}
			if err = store.Delete(ctx, current); err != nil {
				return err
			}
			return store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ObjectID: current.ID, ObjectLabel: current.Label, Action: "delete", At: time.Now().UTC()})
		})
		if err != nil {
			s.failure(w, r, err)
			return
		}
		http.Redirect(w, r, s.modelURL(options), http.StatusSeeOther)
		return
	}
	rows := []any{}
	for _, target := range deletion.Objects {
		if target.Record == nil {
			continue
		}
		schema := target.Record.Schema()
		if s.config.Policy.Authorize(r.Context(), p, "view", auth.Resource{App: schema.AppLabel, Model: schema.Name, ID: target.ID, Object: target.Record}) == nil {
			rows = append(rows, target.Label)
		}
	}
	token, err := s.token(p, options, object, "delete")
	if err != nil {
		s.failure(w, r, err)
		return
	}
	s.render(w, r, p, "delete.html", templates.Context{"title": "Delete " + object.Label, "objects": rows, "blocked": len(deletion.Protected) > 0, "edit_token": token, "cancel_url": s.modelURL(options) + url.PathEscape(id) + "/change/"}, 200)
}

func (s *Site) scopedDeletion(ctx context.Context, p auth.Principal, graph Deletion, lock bool) (Deletion, error) {
	result := Deletion{Protected: append([]string(nil), graph.Protected...)}
	for _, target := range graph.Objects {
		if target.Record == nil {
			return Deletion{}, errors.New("invalid deletion graph")
		}
		schema := target.Record.Schema()
		store, err := s.config.Store.Scope(ctx, p, s.config.Name, schema)
		if err != nil || store == nil {
			return Deletion{}, auth.ErrPermissionDenied
		}
		current, err := store.Get(ctx, target.ID, lock)
		if err != nil {
			return Deletion{}, auth.ErrPermissionDenied
		}
		if err = s.config.Policy.Authorize(ctx, p, "delete", auth.Resource{App: schema.AppLabel, Model: schema.Name, ID: current.ID, Object: current.Record}); err != nil {
			return Deletion{}, err
		}
		result.Objects = append(result.Objects, current)
	}
	return result, nil
}

type actionConfirmation struct {
	Actor, Site, Model, Action string
	Rows                       []editToken
}

func (s *Site) action(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore) {
	if err := s.parsePost(w, r); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	var selected Action
	for _, action := range options.Actions {
		if action.Name == r.PostForm.Get("action") {
			selected = action
			break
		}
	}
	if selected.Run == nil {
		http.Error(w, "Unknown action", 400)
		return
	}
	if err := s.allowed(r.Context(), p, selected.Permission, options, Object{}); err != nil {
		s.failure(w, r, err)
		return
	}
	ids := r.PostForm["_selected"]
	if len(ids) == 0 || len(ids) > 1000 {
		http.Error(w, "Select between 1 and 1000 objects", 400)
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			http.Error(w, "Invalid selection", 400)
			return
		}
		seen[id] = true
	}
	objects := []Object{}
	confirmation := actionConfirmation{Actor: p.ID, Site: s.config.Name, Model: options.Schema.Key(), Action: selected.Name}
	for _, id := range ids {
		object, err := store.Get(r.Context(), id, false)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		if err = s.allowed(r.Context(), p, selected.Permission, options, object); err != nil {
			s.failure(w, r, err)
			return
		}
		objects = append(objects, object)
		confirmation.Rows = append(confirmation.Rows, editToken{Actor: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ID: id, Version: object.Version, Action: selected.Name})
	}
	if selected.Confirm && !r.PostForm.Has("_confirm") {
		payload, _ := json.Marshal(confirmation)
		token, err := s.config.Signer.Sign(payload)
		if err != nil {
			s.failure(w, r, err)
			return
		}
		rows := []any{}
		for _, object := range objects {
			rows = append(rows, templates.Context{"id": object.ID, "label": object.Label})
		}
		s.render(w, r, p, "action.html", templates.Context{"title": "Confirm " + selected.Description, "action": selected.Name, "rows": rows, "confirmation": token}, 200)
		return
	}
	if selected.Confirm {
		payload, err := s.config.Signer.Verify(r.PostForm.Get("_confirmation"), time.Hour)
		var supplied actionConfirmation
		if err != nil || json.Unmarshal(payload, &supplied) != nil || !reflect.DeepEqual(supplied, confirmation) {
			s.failure(w, r, ErrConflict)
			return
		}
	}
	err := store.Atomic(r.Context(), func(ctx context.Context) error {
		locked := []Object{}
		for i, id := range ids {
			object, err := store.Get(ctx, id, true)
			if err != nil {
				return err
			}
			if object.Version != confirmation.Rows[i].Version {
				return ErrConflict
			}
			if err = s.allowed(ctx, p, selected.Permission, options, object); err != nil {
				return err
			}
			locked = append(locked, object)
		}
		if err := selected.Run(ctx, store, locked); err != nil {
			return err
		}
		for _, object := range locked {
			if err := store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ObjectID: object.ID, ObjectLabel: object.Label, Action: selected.Name, At: time.Now().UTC()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.failure(w, r, err)
		return
	}
	http.Redirect(w, r, s.modelURL(options), http.StatusSeeOther)
}
