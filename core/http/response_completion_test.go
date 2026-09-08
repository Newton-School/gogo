package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type completionWriter struct {
	header                  http.Header
	status, headers, writes int
	count                   int
	err                     error
	body                    []byte
	onHeader, onWriteHeader func()
}

func (w *completionWriter) Header() http.Header {
	if w.onHeader != nil {
		w.onHeader()
	}
	return w.header
}
func (w *completionWriter) WriteHeader(status int) {
	w.headers++
	w.status = status
	if w.onWriteHeader != nil {
		w.onWriteHeader()
	}
}
func (w *completionWriter) Write(body []byte) (int, error) {
	w.writes++
	w.body = append(w.body, body...)
	if w.count < 0 {
		return len(body), w.err
	}
	return w.count, w.err
}

func TestResponseCompletionRejectsShortWrites(t *testing.T) {
	failure := errors.New("writer failed")
	for _, test := range []struct {
		name      string
		count     int
		err, want error
	}{
		{name: "zero", count: 0, want: io.ErrShortWrite},
		{name: "partial", count: 3, want: io.ErrShortWrite},
		{name: "invalid_overcount", count: 5, want: io.ErrShortWrite},
		{name: "complete", count: 4},
		{name: "preserve_partial_error", count: 3, err: failure, want: failure},
		{name: "preserve_complete_error", count: 4, err: failure, want: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &completionWriter{header: http.Header{}, count: test.count, err: test.err}
			err := Text(200, "body").Write(writer, httptest.NewRequest("GET", "/", nil))
			if !errors.Is(err, test.want) || writer.headers != 1 || writer.writes != 1 {
				t.Fatalf("completion error=%v, want=%v, writer=%+v", err, test.want, writer)
			}
		})
	}
}

func TestResponseCompletionAdaptAbortsShortWriteWithoutSecondResponse(t *testing.T) {
	writer := &completionWriter{header: http.Header{}, count: 2}
	defer func() {
		if got := recover(); got != http.ErrAbortHandler || writer.headers != 1 || writer.writes != 1 || string(writer.body) != "body" {
			t.Errorf("short response did not abort once: panic=%v writer=%+v", got, writer)
		}
	}()
	Adapt(func(*http.Request) (Response, error) { return Text(200, "body"), nil }).ServeHTTP(writer, httptest.NewRequest("GET", "/", nil))
}

func TestResponseCompletionFreezesHeadersBeforeRendering(t *testing.T) {
	response := Text(200, "unused")
	response.Headers["X-Validated"] = []string{"first", "second"}
	response.ETag = `"original"`
	response.Render = func(context.Context) ([]byte, error) {
		response.Headers["X-Validated"][0] = "bad\r\nInjected: true"
		response.Headers["X-Validated"][1] = "replaced"
		response.Headers.Set("X-Later", "not validated")
		response.ETag = "bad\r\nInjected: true"
		return []byte("rendered"), nil
	}
	writer := httptest.NewRecorder()
	if err := response.Write(writer, httptest.NewRequest("GET", "/", nil)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Header().Values("X-Validated"), []string{"first", "second"}) || writer.Header().Get("X-Later") != "" || writer.Header().Get("ETag") != `"original"` || writer.Body.String() != "rendered" {
		t.Fatal("renderer changed finalized metadata", writer.Header(), writer.Body.String())
	}
}

func TestResponseCompletionFreezesRequestMethodBeforeRendering(t *testing.T) {
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "GET"} {
		t.Run(method, func(t *testing.T) {
			request := httptest.NewRequest(method, "/", nil)
			if method != "HEAD" && method != "GET" {
				request.Header.Set("If-None-Match", "*")
			}
			response := Response{ETag: `"current"`, Render: func(context.Context) ([]byte, error) {
				request.Method = "GET"
				if method == "GET" {
					request.Method = "HEAD"
				}
				return []byte("rendered"), nil
			}}
			writer := httptest.NewRecorder()
			if err := response.Write(writer, request); err != nil || writer.Code != 200 {
				t.Fatalf("method %s changed response status=%d, err=%v", method, writer.Code, err)
			}
			wantBody := "rendered"
			if method == "HEAD" {
				wantBody = ""
			}
			if writer.Body.String() != wantBody {
				t.Fatalf("method %s changed body=%q", method, writer.Body.String())
			}
		})
	}
}

func TestResponseCompletionFreezesConditionalHeaderValues(t *testing.T) {
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, key string
		before    []string
		after     string
		remove    bool
		status    int
	}{
		{"tag_miss_stays_miss", "If-None-Match", []string{`"other"`}, `"current"`, false, 200},
		{"tag_match_stays_match", "If-None-Match", []string{`"current"`}, `"other"`, false, 304},
		{"tag_absence_stays_absent", "If-None-Match", nil, `"current"`, false, 200},
		{"empty_tag_still_suppresses_date", "If-None-Match", []string{""}, "", true, 200},
		{"date_match_stays_match", "If-Modified-Since", []string{modified.Format(http.TimeFormat)}, modified.Add(-time.Hour).Format(http.TimeFormat), false, 304},
		{"date_miss_stays_miss", "If-Modified-Since", []string{modified.Add(-time.Hour).Format(http.TimeFormat)}, modified.Format(http.TimeFormat), false, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			request.Header[test.key] = test.before
			response := Response{ETag: `"current"`}
			if test.key == "If-Modified-Since" || test.remove {
				response.LastModified = modified
			}
			if test.remove {
				request.Header.Set("If-Modified-Since", modified.Format(http.TimeFormat))
			}
			response.Render = func(context.Context) ([]byte, error) {
				if test.remove {
					request.Header.Del(test.key)
				} else if len(request.Header[test.key]) != 0 {
					request.Header[test.key][0] = test.after
				} else {
					request.Header.Set(test.key, test.after)
				}
				return []byte("rendered"), nil
			}
			writer := httptest.NewRecorder()
			if err := response.Write(writer, request); err != nil || writer.Code != test.status {
				t.Fatalf("conditional snapshot status=%d want=%d err=%v", writer.Code, test.status, err)
			}
		})
	}
}

func TestResponseCompletionFreezesInitialAndRenderedBytesBeforeWriterCallbacks(t *testing.T) {
	for _, rendered := range []bool{false, true} {
		body := []byte("safe")
		response := Response{Body: body, Headers: http.Header{"X-Validated": {"one", "two"}}}
		if rendered {
			response.Render = func(context.Context) ([]byte, error) { return body, nil }
		}
		writer := &completionWriter{header: http.Header{}, count: -1}
		writer.onHeader = func() {
			copy(body, "evil")
			response.Headers["X-Validated"][1] = "unvalidated"
		}
		if err := response.Write(writer, httptest.NewRequest("GET", "/", nil)); err != nil || string(writer.body) != "safe" || !reflect.DeepEqual(writer.header.Values("X-Validated"), []string{"one", "two"}) {
			t.Fatalf("rendered=%t err=%v body=%q headers=%v", rendered, err, writer.body, writer.header)
		}
	}
}

type completionContext struct {
	context.Context
	callback func()
}

func (ctx completionContext) Err() error { ctx.callback(); return nil }

func TestResponseCompletionFreezesInputsBeforeContextCallbacks(t *testing.T) {
	response := Text(200, "unused")
	request := httptest.NewRequest("HEAD", "/", nil)
	request = request.WithContext(completionContext{Context: context.Background(), callback: func() {
		request.Method = "GET"
		response.Headers.Set("Content-Type", "unvalidated")
	}})
	response.Render = func(ctx context.Context) ([]byte, error) { return []byte("rendered"), ctx.Err() }
	writer := httptest.NewRecorder()
	if err := response.Write(writer, request); err != nil || writer.Code != 200 || writer.Body.Len() != 0 || writer.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal("context changed response inputs", err, writer.Code, writer.Body.String(), writer.Header())
	}
}

func TestResponseCompletionPreservesRenderFailureBeforeConditionalHeaders(t *testing.T) {
	failure := errors.New("render failed")
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("If-None-Match", "*")
	writer := &completionWriter{header: http.Header{}}
	response := Response{ETag: `"current"`, Headers: http.Header{"X-Validated": {"value"}}, Render: func(context.Context) ([]byte, error) { return nil, failure }}
	if err := response.Write(writer, request); err != failure || writer.headers != 0 || writer.writes != 0 || len(writer.header) != 0 {
		t.Fatal("conditional response bypassed rendering failure", err, writer)
	}
}
