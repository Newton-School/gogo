package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/urls"
)

func allowGenericRead(*http.Request) error { return nil }

func TestGenericReadConstruction(t *testing.T) {
	run := func(*readViewCall) (readViewResult, error) { return readViewResult{response: Text(200, "ok")}, nil }
	for _, options := range []ReadViewOptions{{}, {Authorize: allowGenericRead, Timeout: -1}, {Authorize: allowGenericRead, Timeout: time.Nanosecond}, {Authorize: allowGenericRead, Timeout: time.Minute + 1}} {
		if handler, err := newReadView(options, run); handler != nil || err != ErrGenericConfiguration {
			t.Fatal("invalid construction accepted", err)
		}
	}
	if handler, err := newReadView(ReadViewOptions{Authorize: allowGenericRead}, nil); handler != nil || err != ErrGenericConfiguration {
		t.Fatal("nil read operation accepted")
	}
}

func TestGenericReadMethodsAndHead(t *testing.T) {
	for _, allowOptions := range []bool{false, true} {
		for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE", "TRACE"} {
			t.Run(fmt.Sprintf("%s/options=%v", method, allowOptions), func(t *testing.T) {
				authCalls, reads, finalizers := 0, 0, 0
				handler, err := newReadView(ReadViewOptions{AllowOptions: allowOptions, Authorize: func(*http.Request) error { authCalls++; return nil }}, func(*readViewCall) (readViewResult, error) {
					reads++
					return readViewResult{response: Text(200, "page"), finalize: func(*readViewCall) error { finalizers++; return nil }}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, httptest.NewRequest(method, "/", nil))
				want := 405
				if method == "GET" || method == "HEAD" || allowOptions && method == "OPTIONS" {
					want = 200
				}
				if out.Code != want || out.Header().Get("Cache-Control") != "private, no-store" || out.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("unexpected response", out)
				}
				if want == 405 && (authCalls != 0 || reads != 0 || finalizers != 0 || out.Header().Get("Allow") == "") {
					t.Fatal("unsupported method ran callbacks")
				}
				if want == 200 && authCalls != 2 {
					t.Fatal("missing access fence")
				}
				if method == "OPTIONS" && (reads != 0 || finalizers != 0) {
					t.Fatal("OPTIONS ran operation")
				}
				if method == "HEAD" && (reads != 1 || finalizers != 1 || out.Body.Len() != 0 || out.Header().Get("Content-Length") != "4") {
					t.Fatal("HEAD differs from GET metadata")
				}
			})
		}
	}
}

func TestGenericReadErrorsAreExactAndPrivateAtEveryStage(t *testing.T) {
	for _, stage := range []string{"initial", "run", "final", "finalizer"} {
		for _, failure := range []struct {
			err    error
			status int
		}{
			{auth.ErrUnauthenticated, 401}, {auth.ErrPermissionDenied, 403}, {ErrNotFound, 404},
			{errors.New("private provider detail"), 503}, {fmt.Errorf("private wrapper: %w", auth.ErrPermissionDenied), 503},
			{errors.Join(auth.ErrPermissionDenied, errors.New("private outage")), 503},
		} {
			t.Run(fmt.Sprintf("%s/%d/%T", stage, failure.status, failure.err), func(t *testing.T) {
				calls, reads, finals := 0, 0, 0
				handler, err := newReadView(ReadViewOptions{Authorize: func(*http.Request) error {
					calls++
					if stage == "initial" && calls == 1 || stage == "final" && calls == 2 {
						return failure.err
					}
					return nil
				}}, func(*readViewCall) (readViewResult, error) {
					reads++
					if stage == "run" {
						return readViewResult{}, failure.err
					}
					return readViewResult{response: Text(200, "private page"), finalize: func(*readViewCall) error {
						finals++
						if stage == "finalizer" {
							return failure.err
						}
						return nil
					}}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
				if out.Code != failure.status || strings.Contains(out.Body.String(), "private") {
					t.Fatal("error disclosed data or was reclassified", out)
				}
				if stage == "initial" && reads != 0 || stage == "final" && finals != 0 {
					t.Fatal("work continued after denial")
				}
			})
		}
	}
}

func TestGenericReadCancellationAndPanicNeverReturnPartialData(t *testing.T) {
	for _, stage := range []string{"initial", "run", "final", "finalizer"} {
		for _, mode := range []string{"cancel", "panic", "cancel_denial"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fail := func() error {
					if mode == "panic" {
						panic("private panic")
					}
					cancel()
					if mode == "cancel_denial" {
						return auth.ErrPermissionDenied
					}
					return nil
				}
				calls := 0
				handler, err := newReadView(ReadViewOptions{Authorize: func(*http.Request) error {
					calls++
					if stage == "initial" && calls == 1 || stage == "final" && calls == 2 {
						return fail()
					}
					return nil
				}}, func(*readViewCall) (readViewResult, error) {
					if stage == "run" {
						if err := fail(); err != nil {
							return readViewResult{}, err
						}
					}
					return readViewResult{response: Text(200, "private page"), finalize: func(*readViewCall) error {
						if stage == "finalizer" {
							return fail()
						}
						return nil
					}}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
				if out.Code != 503 || strings.Contains(out.Body.String(), "private") {
					t.Fatal("partial data or denial masked cancellation", out)
				}
			})
		}
	}
}

func TestGenericReadRejectsBodyAndOversizedMetadataBeforeCallbacks(t *testing.T) {
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = http.NoBody; r.ContentLength = 1 },
		func(r *http.Request) { r.Body = httptest.NewRequest("POST", "/", strings.NewReader("x")).Body },
		func(r *http.Request) { r.ContentLength = -1 },
		func(r *http.Request) { r.TransferEncoding = []string{"chunked"} },
		func(r *http.Request) { r.Trailer = http.Header{"X-Trailer": {"value"}} },
		func(r *http.Request) { r.URL = nil },
		func(r *http.Request) { r.URL.User = url.User("private") },
		func(r *http.Request) { r.URL.Host = strings.Repeat("h", 16<<10) },
		func(r *http.Request) { r.URL.Scheme = strings.Repeat("s", 16<<10) },
		func(r *http.Request) { r.URL.RawFragment = "hidden" },
		func(r *http.Request) { r.URL.Path = "relative" },
		func(r *http.Request) { r.URL.Path = "/bad\x00path" },
		func(r *http.Request) { r.URL.RawPath = "/different" },
		func(r *http.Request) { r.URL.RawPath = "/%xx" },
		func(r *http.Request) { r.Proto = strings.Repeat("p", 65) },
		func(r *http.Request) { r.URL.RawQuery = strings.Repeat("q", 16<<10) },
		func(r *http.Request) { r.Header.Set("Bad Header", "value") },
		func(r *http.Request) { r.Header.Set("X-Bad", "private\r\nheader") },
		func(r *http.Request) { r.Header["X-Many"] = make([]string, 1025) },
		func(r *http.Request) { r.Header.Set("X-Large", strings.Repeat("h", 64<<10)) },
	} {
		handler, err := newReadView(ReadViewOptions{Authorize: func(*http.Request) error { t.Error("invalid request reached grant"); return nil }}, func(*readViewCall) (readViewResult, error) {
			t.Error("invalid request reached read")
			return readViewResult{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("HEAD", "/", nil)
		mutate(r)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != 400 || out.Body.Len() != 0 {
			t.Fatal("malformed HEAD response", out)
		}
	}
}

func TestGenericReadFreezesRequestAndResponseAcrossCallbacks(t *testing.T) {
	type scopeKey struct{}
	r := httptest.NewRequest("HEAD", "/original?q=one&q=two", nil).WithContext(context.WithValue(context.Background(), scopeKey{}, "scope"))
	r.Header.Set("X-Original", "first")
	r.SetPathValue("id", "original")
	body, headers := []byte("page"), http.Header{"X-Result": {"original"}}
	var order []string
	check := func(req *http.Request) {
		if req.Method != "HEAD" || req.URL.Path != "/original" || req.URL.RawQuery != "q=one&q=two" || req.Header.Get("X-Original") != "first" || req.PathValue("id") != "original" || req.Context().Value(scopeKey{}) != "scope" {
			t.Error("request snapshot changed", req)
		}
		req.Method = "POST"
		req.URL.Path = "/changed"
		req.Header.Set("X-Original", "changed")
		req.SetPathValue("id", "changed")
	}
	handler, err := newReadView(ReadViewOptions{Authorize: func(req *http.Request) error {
		order = append(order, "grant")
		check(req)
		r.Method = "GET"
		r.URL.Path = "/retarget"
		r.URL.RawQuery = "wrong"
		r.Header.Set("X-Original", "wrong")
		r.SetPathValue("id", "wrong")
		if len(order) > 1 {
			body[0] = 'X'
			headers["X-Result"][0] = "wrong"
		}
		return nil
	}}, func(call *readViewCall) (readViewResult, error) {
		order = append(order, "read")
		check(call.request())
		if call.rawQuery() != "q=one&q=two" {
			t.Error("raw query changed")
		}
		return readViewResult{response: Response{Status: 200, Body: body, Headers: headers}, finalize: func(call *readViewCall) error { order = append(order, "finalizer"); check(call.request()); return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	if out.Code != 200 || out.Body.Len() != 0 || out.Header().Get("Content-Length") != "4" || out.Header().Get("X-Result") != "original" || !reflect.DeepEqual(order, []string{"grant", "read", "grant", "finalizer"}) {
		t.Fatal("mutable response or wrong callback order", out, order)
	}
}

func TestGenericReadRejectsMutableCustomParametersWithoutInvokingMethods(t *testing.T) {
	for _, value := range []any{map[string]string{"id": "private"}, []int{1}, new(int), nil, func() {}} {
		handler, err := newReadView(ReadViewOptions{Authorize: func(*http.Request) error { t.Error("mutable parameter reached grant"); return nil }}, func(*readViewCall) (readViewResult, error) {
			t.Error("mutable parameter reached operation")
			return readViewResult{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		router, err := urls.NewWithConverters(map[string]urls.Converter{"custom": {Pattern: "[a-z]+", Decode: func(string) (any, error) { return value, nil }, Encode: func(any) (string, error) { t.Error("unexpected encode"); return "", nil }}}, urls.Path("/<custom:id>/", handler, "item"))
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", "/item/", nil))
		if out.Code != 503 {
			t.Fatal("mutable custom parameter accepted", out.Code)
		}
	}
}

func TestGenericReadMaterializesBeforeFinalGrantAndAbortsShortWrites(t *testing.T) {
	for _, response := range []Response{
		{Status: 200, Render: func(context.Context) ([]byte, error) { t.Error("deferred render invoked"); return nil, nil }},
		{Status: 200, Headers: http.Header{"X-Bad": {"private\nvalue"}}},
		{Status: 200, ETag: `"private"`}, {Status: 199},
	} {
		grants := 0
		handler, err := newReadView(ReadViewOptions{Authorize: func(*http.Request) error { grants++; return nil }}, func(*readViewCall) (readViewResult, error) { return readViewResult{response: response}, nil })
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
		if out.Code != 503 || grants != 1 || strings.Contains(out.Body.String(), "private") {
			t.Fatal("invalid representation crossed final fence", out, grants)
		}
	}
	handler, err := newReadView(ReadViewOptions{Authorize: allowGenericRead}, func(*readViewCall) (readViewResult, error) { return readViewResult{response: Text(200, "page")}, nil })
	if err != nil {
		t.Fatal(err)
	}
	writer := &completionWriter{header: http.Header{}, count: 1}
	defer func() {
		if got := recover(); got != http.ErrAbortHandler || writer.headers != 1 || writer.writes != 1 {
			t.Errorf("partial response did not abort once: %v", got)
		}
	}()
	handler.ServeHTTP(writer, httptest.NewRequest("GET", "/", nil))
}

func TestGenericReadCooperativeDeadlineAndCanceledEntry(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if alreadyCanceled {
			cancel()
		}
		calls := 0
		handler, err := newReadView(ReadViewOptions{Authorize: allowGenericRead, Timeout: time.Millisecond}, func(call *readViewCall) (readViewResult, error) {
			calls++
			<-call.base.Context().Done()
			return readViewResult{response: Text(200, "private")}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
		cancel()
		if out.Code != 503 || strings.Contains(out.Body.String(), "private") || alreadyCanceled && calls != 0 {
			t.Fatal("canceled read emitted data", out)
		}
	}
}
