package sessions

import (
	"bufio"
	"errors"
	"github.com/Newton-School/gogo/core/security"
	"net"
	"net/http"
	"time"
)

type MiddlewareConfig struct {
	Store      Store
	Signer     *security.Signer
	CookieName string
	TTL        time.Duration
	Secure     bool
	Now        func() time.Time
}

func Middleware(config MiddlewareConfig) (func(http.Handler) http.Handler, error) {
	if config.Store == nil || config.Signer == nil {
		return nil, errors.New("session store and signer required")
	}
	if config.CookieName == "" {
		config.CookieName = "gogo_session"
	}
	if config.TTL == 0 {
		config.TTL = 14 * 24 * time.Hour
	}
	if config.TTL < 0 {
		return nil, errors.New("invalid session TTL")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			record := Record{}
			stale := false
			if cookie, err := r.Cookie(config.CookieName); err == nil {
				value, e := config.Signer.Verify(cookie.Value, config.TTL)
				if e != nil {
					stale = true
				} else {
					record, e = config.Store.Load(r.Context(), string(value))
					if errors.Is(e, ErrNotFound) {
						record = Record{}
						stale = true
					} else if e != nil {
						http.Error(w, "session service unavailable", 503)
						return
					} else if !record.ExpiresAt.After(config.Now()) {
						record = Record{}
						stale = true
					}
				}
			}
			session := New(record)
			r = r.WithContext(WithSession(r.Context(), session))
			out := &sessionWriter{ResponseWriter: w, persist: func() error {
				if err := session.Persist(r.Context(), config.Store, config.Now(), config.TTL); err != nil {
					return err
				}
				if session.Accessed() || session.modified || session.flushed || stale {
					w.Header().Add("Vary", "Cookie")
				}
				if session.modified || session.flushed || stale {
					w.Header().Set("Cache-Control", "private, no-store")
				}
				if session.flushed || stale && !session.modified {
					http.SetCookie(w, &http.Cookie{Name: config.CookieName, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
				} else if session.modified {
					token, e := config.Signer.Sign([]byte(session.record.ID))
					if e != nil {
						return e
					}
					cookie := &http.Cookie{Name: config.CookieName, Value: token, Path: "/", Secure: config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode}
					if !session.record.BrowserClose {
						cookie.Expires = session.record.ExpiresAt
						cookie.MaxAge = max(1, int(session.record.ExpiresAt.Sub(config.Now())/time.Second))
					}
					http.SetCookie(w, cookie)
				}
				return nil
			}}
			next.ServeHTTP(out, r)
			if !out.written {
				out.WriteHeader(200)
			}
		})
	}, nil
}

type sessionWriter struct {
	http.ResponseWriter
	persist func() error
	written bool
	err     error
}

func (w *sessionWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.written = true
	w.err = w.persist()
	if w.err != nil {
		w.Header().Del("Set-Cookie")
		w.Header().Del("Content-Length")
		http.Error(w.ResponseWriter, "session service unavailable", 503)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *sessionWriter) Write(value []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	if w.err != nil {
		return 0, w.err
	}
	return w.ResponseWriter.Write(value)
}
func (w *sessionWriter) Flush() {
	if !w.written {
		w.WriteHeader(200)
	}
	if w.err == nil {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
	}
}
func (w *sessionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.written {
		w.written = true
		w.err = w.persist()
	}
	if w.err != nil {
		return nil, nil, w.err
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
