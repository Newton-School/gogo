// Package http adds structured views and lifecycle management to net/http.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

type Error struct {
	Status        int
	Code, Message string
	Fields        map[string][]string
	Cause         error
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Cause }

var ErrNotFound = &Error{404, "NOT_FOUND", "Not found", nil, nil}
var ErrConflict = &Error{409, "CONFLICT", "Resource changed", nil, nil}
var ErrUnavailable = &Error{503, "UNAVAILABLE", "Service unavailable", nil, nil}

func PublicError(err error) *Error {
	var public *Error
	if errors.As(err, &public) {
		if public.Status >= 400 && public.Status <= 599 {
			return public
		}
	}
	if errors.Is(err, auth.ErrUnauthenticated) {
		return &Error{401, "UNAUTHENTICATED", "Authentication required", nil, nil}
	}
	if errors.Is(err, auth.ErrPermissionDenied) {
		return &Error{403, "PERMISSION_DENIED", "Permission denied", nil, nil}
	}
	if errors.Is(err, db.ErrNoRows) || errors.Is(err, orm.ErrNotFound) || errors.Is(err, urls.ErrNotFound) {
		return ErrNotFound
	}
	// A database validation stage can retain field errors alongside a failed
	// provider check. An outage/timeout is not a completed input validation.
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{504, "TIMEOUT", "Request timed out", nil, nil}
	}
	if errors.Is(err, context.Canceled) || db.IsCode(err, db.Unavailable) || db.IsCode(err, db.UnknownCommit) || db.IsCode(err, db.Canceled) {
		return ErrUnavailable
	}
	if db.IsCode(err, db.UniqueViolation) || db.IsCode(err, db.ForeignKeyViolation) || db.IsCode(err, db.CheckViolation) {
		return ErrConflict
	}
	var provider *db.Error
	if errors.As(err, &provider) {
		return &Error{500, "INTERNAL_ERROR", "Internal server error", nil, nil}
	}
	var validation *models.ValidationError
	if errors.As(err, &validation) && models.IsValidationOnly(err) {
		fields := map[string][]string{}
		for name, items := range validation.Fields {
			for _, item := range items {
				fields[name] = append(fields[name], item.Message)
			}
		}
		return &Error{400, "VALIDATION_ERROR", "Invalid input", fields, nil}
	}
	return &Error{500, "INTERNAL_ERROR", "Internal server error", nil, nil}
}

type Response struct {
	Status       int
	Headers      http.Header
	Body         []byte
	Render       func(context.Context) ([]byte, error)
	ETag         string
	LastModified time.Time
}

func Text(status int, value string) Response {
	return Response{Status: status, Headers: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}, Body: []byte(value)}
}
func HTML(status int, value string) Response {
	return Response{Status: status, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte(value)}
}
func JSON(status int, value any) (Response, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return Response{}, e
	}
	return Response{Status: status, Headers: http.Header{"Content-Type": {"application/json"}}, Body: b}, nil
}
func Redirect(target string, status int) (Response, error) {
	if status != 301 && status != 302 && status != 303 && status != 307 && status != 308 {
		return Response{}, errors.New("invalid redirect status")
	}
	if security.SafeNext(target, "") == "" {
		return Response{}, errors.New("unsafe redirect target")
	}
	return Response{Status: status, Headers: http.Header{"Location": {target}}}, nil
}
func Template(engine *templates.Engine, name string, data templates.Context, status int) Response {
	return Response{Status: status, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Render: func(ctx context.Context) ([]byte, error) {
		value, e := engine.Render(ctx, name, data)
		return []byte(value), e
	}}
}
func (r Response) Write(w http.ResponseWriter, req *http.Request) error {
	if r.Status == 0 {
		r.Status = 200
	}
	if r.Status < 200 || r.Status > 599 {
		return errors.New("invalid response status")
	}
	for key, values := range r.Headers {
		if !validHeaderName(key) {
			return errors.New("invalid response header")
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return errors.New("invalid response header")
			}
		}
	}
	if strings.ContainsAny(r.ETag, "\r\n") {
		return errors.New("invalid ETag")
	}
	// Freeze validated metadata and caller-owned bytes before a renderer,
	// context or ResponseWriter callback can change their backing storage.
	r.Headers = r.Headers.Clone()
	body := slices.Clone(r.Body)
	method := req.Method
	// Snapshot the pure conditional decision without cloning arbitrary request
	// header slices: the existing tag parser retains its line/byte bounds.
	// Rendering still runs before any 304 is emitted, preserving error priority.
	notModified := false
	if r.Status == 200 && (method == "GET" || method == "HEAD") {
		etags := req.Header.Values("If-None-Match")
		if len(etags) != 0 {
			notModified = matchesIfNoneMatch(etags, r.ETag)
		} else if !r.LastModified.IsZero() {
			// The tag condition takes precedence even when it is empty or
			// malformed. Dates are not a list: duplicate lines are ignored.
			dates := req.Header.Values("If-Modified-Since")
			if len(dates) == 1 {
				if since, e := http.ParseTime(dates[0]); e == nil && !r.LastModified.Truncate(time.Second).After(since) {
					notModified = true
				}
			}
		}
	}
	if r.Render != nil {
		var err error
		body, err = r.Render(req.Context())
		if err != nil {
			return err
		}
		body = slices.Clone(body)
	}
	for key, values := range r.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if r.ETag != "" {
		w.Header().Set("ETag", r.ETag)
	}
	if !r.LastModified.IsZero() {
		w.Header().Set("Last-Modified", r.LastModified.UTC().Format(http.TimeFormat))
	}
	if notModified {
		w.WriteHeader(304)
		return nil
	}
	w.WriteHeader(r.Status)
	if method == "HEAD" || r.Status == 204 || r.Status == 304 {
		return nil
	}
	n, err := w.Write(body)
	if err == nil && n != len(body) {
		return io.ErrShortWrite
	}
	return err
}

type View func(*http.Request) (Response, error)

func Adapt(view View) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := &trackedWriter{ResponseWriter: w}
		response, err := view(r)
		if err == nil {
			err = response.Write(out, r)
		}
		if err != nil {
			if out.written {
				// net/http closes an HTTP/1 transfer or resets an HTTP/2
				// stream. A second response would corrupt the first one.
				panic(http.ErrAbortHandler)
			}
			WriteError(w, r, err)
		}
	})
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	public := PublicError(err)
	value := struct {
		Code      string              `json:"code"`
		Detail    string              `json:"detail"`
		Fields    map[string][]string `json:"fields,omitempty"`
		RequestID string              `json:"request_id,omitempty"`
	}{public.Code, public.Message, public.Fields, RequestID(r)}
	response, _ := JSON(public.Status, value)
	_ = response.Write(w, r)
}
func File(w http.ResponseWriter, r *http.Request, name string, modified time.Time, content io.ReadSeeker) {
	http.ServeContent(w, r, name, modified, content)
}

type Router = urls.Router

func NewRouter(routes ...urls.Route) (*Router, error) { return urls.New(routes...) }
