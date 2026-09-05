// Package messages stores bounded, plain-text, consume-once user notifications.
// Install its middleware inside session middleware when using session/fallback.
package messages

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type Level int

const (
	Debug   Level = 10
	Info    Level = 20
	Success Level = 25
	Warning Level = 30
	Error   Level = 40
)

type Message struct {
	Level  Level    `json:"level"`
	Text   string   `json:"text"`
	Tags   []string `json:"tags,omitempty"`
	Public bool     `json:"public,omitempty"`
}

func (m Message) LevelTag() string {
	switch m.Level {
	case Debug:
		return "debug"
	case Info:
		return "info"
	case Success:
		return "success"
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return "message"
}

type Mode string

const (
	Session  Mode = "session"
	Cookie   Mode = "cookie"
	Fallback Mode = "fallback"
)

type Config struct {
	Mode                               Mode
	Signer                             *security.Signer
	CookieName                         string
	Secure                             bool
	MaxAge                             time.Duration
	MaxMessages, MaxBytes, CookieBytes int
	Minimum                            Level
}

var ErrUnavailable = errors.New("messages: storage unavailable")
var ErrLimit = errors.New("messages: configured limit exceeded")
var ErrPrivateCookie = errors.New("messages: private messages require session storage")
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const sessionKey = "_gogo_messages"

type contextKey struct{}
type queue struct {
	config                       Config
	messages                     []Message
	consumed                     int
	changed, committed, accessed bool
	minimum                      Level
	session                      *sessions.Session
	generation                   uint64
}

func clone(messages []Message) []Message {
	out := slices.Clone(messages)
	for i := range out {
		out[i].Tags = slices.Clone(out[i].Tags)
	}
	return out
}
func current(r *http.Request) (*queue, error) {
	q, ok := r.Context().Value(contextKey{}).(*queue)
	if !ok {
		return nil, ErrUnavailable
	}
	q.syncIdentity()
	return q, nil
}

func (q *queue) syncIdentity() {
	if q.session != nil && q.session.Generation() != q.generation {
		q.generation = q.session.Generation()
		q.messages = nil
		q.consumed = 0
		q.changed = true
	}
}

// Add keeps content in the configured session; it never puts private text into
// a readable signed cookie. Tokens, credentials and secrets must not be messages.
func Add(r *http.Request, level Level, text string, tags ...string) error {
	return add(r, Message{Level: level, Text: text, Tags: tags})
}

// AddPublic explicitly permits this plain text to reside in a signed cookie.
func AddPublic(r *http.Request, level Level, text string, tags ...string) error {
	return add(r, Message{Level: level, Text: text, Tags: tags, Public: true})
}
func add(r *http.Request, message Message) error {
	q, err := current(r)
	if err != nil {
		return err
	}
	if q.committed {
		return sessions.ErrCommitted
	}
	if message.Level < q.minimum {
		return nil
	}
	if q.config.Mode == Cookie && !message.Public {
		return ErrPrivateCookie
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	remaining := clone(q.messages[q.consumed:])
	remaining = append(remaining, message)
	if err := q.check(remaining); err != nil {
		return err
	}
	if q.config.Mode == Cookie {
		raw, err := json.Marshal(remaining)
		if err != nil {
			return err
		}
		token, err := q.config.Signer.Sign(raw)
		if err != nil {
			return err
		}
		cookie := &http.Cookie{Name: q.config.CookieName, Value: token, Path: "/", Secure: q.config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(q.config.MaxAge / time.Second)}
		if len(cookie.String()) > q.config.CookieBytes {
			return ErrLimit
		}
	}
	q.messages = clone(remaining)
	q.consumed = 0
	q.changed = true
	return nil
}
func validateMessage(m Message) error {
	if m.Level < 0 || m.Level > 1000 || !utf8.ValidString(m.Text) || len(m.Tags) > 10 {
		return errors.New("messages: invalid plain-text message")
	}
	for _, tag := range m.Tags {
		if !tagPattern.MatchString(tag) {
			return errors.New("messages: invalid tag")
		}
	}
	return nil
}
func (q *queue) check(messages []Message) error {
	if len(messages) > q.config.MaxMessages {
		return ErrLimit
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	if len(raw) > q.config.MaxBytes {
		return ErrLimit
	}
	return nil
}
func SetLevel(r *http.Request, minimum Level) error {
	q, err := current(r)
	if err != nil {
		return err
	}
	if q.committed {
		return sessions.ErrCommitted
	}
	if minimum < 0 || minimum > 1000 {
		return errors.New("messages: invalid level")
	}
	q.minimum = minimum
	return nil
}
func Peek(r *http.Request) ([]Message, error) {
	q, err := current(r)
	if err != nil {
		return nil, err
	}
	if q.committed {
		return nil, sessions.ErrCommitted
	}
	q.accessed = true
	return clone(q.messages[q.consumed:]), nil
}
func Consume(r *http.Request) ([]Message, error) {
	q, err := current(r)
	if err != nil {
		return nil, err
	}
	if q.committed {
		return nil, sessions.ErrCommitted
	}
	out := clone(q.messages[q.consumed:])
	q.accessed = true
	if len(out) > 0 {
		q.changed = true
	}
	q.consumed = len(q.messages)
	return out, nil
}

func Middleware(config Config) (func(http.Handler) http.Handler, error) {
	if config.Mode == "" {
		config.Mode = Session
	}
	if config.Mode != Session && config.Mode != Cookie && config.Mode != Fallback {
		return nil, errors.New("messages: invalid storage mode")
	}
	if config.Mode != Session && config.Signer == nil {
		return nil, errors.New("messages: cookie signer required")
	}
	if config.CookieName == "" {
		config.CookieName = "gogo_messages"
	}
	if config.MaxAge == 0 {
		config.MaxAge = 24 * time.Hour
	}
	if config.MaxMessages == 0 {
		config.MaxMessages = 20
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 8192
	}
	if config.CookieBytes == 0 {
		config.CookieBytes = 3500
	}
	if config.Minimum == 0 {
		config.Minimum = Info
	}
	if config.MaxAge < time.Second || config.MaxMessages < 1 || config.MaxBytes < 1 || config.CookieBytes < 256 || config.CookieBytes > 3800 || config.Minimum < 0 || config.Minimum > 1000 {
		return nil, errors.New("messages: invalid storage limits")
	}
	if err := (&http.Cookie{Name: config.CookieName, Value: "value"}).Valid(); err != nil {
		return nil, errors.New("messages: invalid cookie name")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := &queue{config: config, minimum: config.Minimum}
			var session *sessions.Session
			if config.Mode != Cookie {
				var ok bool
				session, ok = sessions.FromContext(r.Context())
				if !ok {
					http.Error(w, "message storage unavailable", 503)
					return
				}
				if _, err := session.Get(sessionKey, &q.messages); err != nil {
					http.Error(w, "message storage unavailable", 503)
					return
				}
				q.session = session
				q.generation = session.Generation()
			}
			stale := false
			if config.Mode != Session {
				if cookie, err := r.Cookie(config.CookieName); err == nil {
					raw, err := config.Signer.Verify(cookie.Value, config.MaxAge)
					var values []Message
					if err != nil || len(raw) > config.MaxBytes || json.Unmarshal(raw, &values) != nil {
						stale = true
					} else {
						q.messages = append(q.messages, values...)
					}
				}
			}
			if err := q.check(q.messages); err != nil {
				http.Error(w, "message storage unavailable", 503)
				return
			}
			for _, m := range q.messages {
				if err := validateMessage(m); err != nil {
					http.Error(w, "message storage unavailable", 503)
					return
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), contextKey{}, q))
			out := &writer{ResponseWriter: w, persist: func(status int) error {
				q.syncIdentity()
				q.committed = true
				if q.accessed || q.changed || stale {
					w.Header().Add("Vary", "Cookie")
					w.Header().Set("Cache-Control", "private, no-store")
				}
				// A failed response neither consumes existing messages nor persists
				// newly queued success messages. Client may retry the request.
				if status >= 500 {
					return nil
				}
				if !q.changed && !stale {
					return nil
				}
				remaining := q.messages[q.consumed:]
				if config.Mode == Session {
					return session.Set(sessionKey, remaining)
				}
				cookie := &http.Cookie{Name: config.CookieName, Path: "/", Secure: config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode}
				private := false
				for _, message := range remaining {
					private = private || !message.Public
				}
				raw, err := json.Marshal(remaining)
				if err != nil {
					return err
				}
				token := ""
				if len(remaining) > 0 && !private {
					token, err = config.Signer.Sign(raw)
					if err != nil {
						return err
					}
				}
				cookie.Value = token
				cookie.MaxAge = int(config.MaxAge / time.Second)
				tooLarge := len(cookie.String()) > config.CookieBytes
				if private || tooLarge {
					if config.Mode != Fallback {
						return ErrLimit
					}
					if err := session.Set(sessionKey, remaining); err != nil {
						return err
					}
					token = ""
				} else if session != nil {
					if err := session.Set(sessionKey, []Message{}); err != nil {
						return err
					}
				}
				if token == "" {
					cookie.Value = ""
					cookie.MaxAge = -1
					cookie.Expires = time.Unix(1, 0)
				} else {
					cookie.Value = token
					cookie.MaxAge = int(config.MaxAge / time.Second)
				}
				http.SetCookie(w, cookie)
				return nil
			}}
			next.ServeHTTP(out, r)
			if !out.written {
				out.WriteHeader(200)
			}
		})
	}, nil
}

type writer struct {
	http.ResponseWriter
	persist func(int) error
	written bool
	err     error
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *writer) WriteHeader(status int) {
	if w.written {
		return
	}
	if status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.written = true
	w.err = w.persist(status)
	if w.err != nil {
		w.Header().Del("Content-Length")
		w.Header().Del("Set-Cookie")
		http.Error(w.ResponseWriter, "message storage unavailable", 503)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *writer) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	if w.err != nil {
		return 0, w.err
	}
	return w.ResponseWriter.Write(b)
}
func (w *writer) Flush() {
	if !w.written {
		w.WriteHeader(200)
	}
	if w.err == nil {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
	}
}
func (w *writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.written {
		w.written = true
		w.err = w.persist(101)
	}
	if w.err != nil {
		return nil, nil, w.err
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
