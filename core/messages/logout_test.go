package messages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/sessions"
)

type lifecycleStore struct {
	mu                      sync.Mutex
	rows                    map[string]sessions.Record
	created, deleted, saved int
}

func (s *lifecycleStore) Load(_ context.Context, id string) (sessions.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return row, sessions.ErrNotFound
	}
	return sessions.Clone(row), nil
}
func (s *lifecycleStore) Create(_ context.Context, row sessions.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[row.ID]; ok {
		return sessions.ErrConflict
	}
	s.rows[row.ID] = sessions.Clone(row)
	s.created++
	return nil
}
func (s *lifecycleStore) Save(_ context.Context, row sessions.Record, version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.rows[row.ID]
	if !ok || old.Version != version {
		return sessions.ErrConflict
	}
	s.rows[row.ID] = sessions.Clone(row)
	s.saved++
	return nil
}
func (s *lifecycleStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	s.deleted++
	return nil
}

func TestFlushAndEmptyMessagesExpireWithoutCreatingAnonymousSession(t *testing.T) {
	for _, mode := range []Mode{Session, Fallback} {
		for _, pending := range []string{"none", "private", "public"} {
			t.Run(string(mode)+"/"+pending, func(t *testing.T) {
				id, err := sessions.NewID()
				if err != nil {
					t.Fatal(err)
				}
				prior, _ := json.Marshal([]Message{{Level: Info, Text: "prior account notice"}})
				store := &lifecycleStore{rows: map[string]sessions.Record{id: {ID: id, Version: 1, ExpiresAt: time.Now().Add(time.Hour), Data: map[string]json.RawMessage{sessionKey: prior, "identity": json.RawMessage(`"prior-account"`)}}}}
				signing := signer(t)
				sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: store, Signer: signing, TTL: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				handler := sessionMiddleware(middleware(t, Config{Mode: mode, Signer: signing})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					session, _ := sessions.FromContext(r.Context())
					if err := session.Flush(); err != nil {
						t.Fatal(err)
					}
					if pending == "private" {
						if err := Add(r, Info, "new notice"); err != nil {
							t.Fatal(err)
						}
					}
					if pending == "public" {
						if err := AddPublic(r, Info, "new notice"); err != nil {
							t.Fatal(err)
						}
					}
					http.Redirect(w, r, "/signed-out", http.StatusSeeOther)
				})))
				token, _ := signing.Sign([]byte(id))
				request := httptest.NewRequest("POST", "http://example.test/logout", nil)
				request.AddCookie(&http.Cookie{Name: "gogo_session", Value: token})
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != 303 || store.deleted != 1 {
					t.Fatal("flush not persisted", response.Code)
				}
				wantSession := pending == "private" || pending == "public" && mode == Session
				if (store.created == 1) != wantSession || len(store.rows) != store.created {
					t.Fatal("empty maintenance created anonymous state", store.created)
				}
				var sessionCookie *http.Cookie
				for _, cookie := range response.Result().Cookies() {
					if cookie.Name == "gogo_session" {
						sessionCookie = cookie
					}
				}
				if sessionCookie == nil || (sessionCookie.MaxAge > 0) != wantSession {
					t.Fatal("incorrect post-flush cookie disposition")
				}
				for _, row := range store.rows {
					if row.ID == id || len(row.Data) != 1 || row.Data["identity"] != nil {
						t.Fatal("prior account state crossed flush")
					}
					var remaining []Message
					if json.Unmarshal(row.Data[sessionKey], &remaining) != nil || len(remaining) != 1 || remaining[0].Text != "new notice" {
						t.Fatal("new logout notice lost")
					}
				}
				if mode == Fallback && pending == "public" {
					next := httptest.NewRequest("GET", "http://example.test/", nil)
					for _, cookie := range response.Result().Cookies() {
						if cookie.MaxAge >= 0 {
							next.AddCookie(cookie)
						}
					}
					next = next.WithContext(sessions.WithSession(next.Context(), sessions.New(sessions.Record{})))
					middleware(t, Config{Mode: Fallback, Signer: signing})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						items, err := Consume(r)
						if err != nil || len(items) != 1 || items[0].Text != "new notice" {
							t.Fatal("public logout notice lost")
						}
					})).ServeHTTP(httptest.NewRecorder(), next)
				}
			})
		}
	}
}

func TestConsumingFinalSessionMessageDeletesOnlyItsKey(t *testing.T) {
	for _, mode := range []Mode{Session, Fallback} {
		for _, status := range []int{200, 500} {
			session := sessions.New(sessions.Record{})
			_ = session.Set("identity", "kept-account")
			_ = session.Set(sessionKey, []Message{{Level: Info, Text: "one notice"}})
			request := httptest.NewRequest("GET", "http://example.test/", nil)
			request = request.WithContext(sessions.WithSession(request.Context(), session))
			middleware(t, Config{Mode: mode, Signer: signer(t)})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := Consume(r); err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(status)
			})).ServeHTTP(httptest.NewRecorder(), request)
			var items []Message
			found, err := session.Get(sessionKey, &items)
			if err != nil || found != (status == 500) {
				t.Fatal("empty key retained or failed response consumed queue", mode, status, found, err)
			}
			var identity string
			if found, err := session.Get("identity", &identity); err != nil || !found || identity != "kept-account" {
				t.Fatal("unrelated session data changed")
			}
		}
	}
}
