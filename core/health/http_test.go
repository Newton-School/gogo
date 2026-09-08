package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/app"
	corehttp "github.com/Newton-School/gogo/core/http"
)

func httpChecker(t *testing.T, state func() State, probe func(context.Context) error) *Checker {
	t.Helper()
	config := Config{Role: "web", State: state}
	if probe != nil {
		config.Dependencies = []Dependency{{ID: "database", Probe: probe}}
	}
	checker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}

func httpReadyState() State { return State{Live: true, Started: true, Ready: true} }

func httpProbe(t *testing.T, checker *Checker, options HTTPOptions) http.Handler {
	t.Helper()
	handler, err := NewHandler(checker, options)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveProbe(handler http.Handler, method string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, "http://example.test/health?diagnostics=true&role=worker", nil))
	return response
}

func TestHealthHTTPPublicProjectionAndHeadParity(t *testing.T) {
	for _, kind := range []Kind{KindLive, KindStartup, KindReady} {
		for _, method := range []string{"GET", "HEAD"} {
			calls := 0
			checker := httpChecker(t, httpReadyState, func(context.Context) error { calls++; return nil })
			handler := httpProbe(t, checker, HTTPOptions{Kind: kind})
			response := serveProbe(handler, method)
			if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatal(kind, response.Code, response.Header())
			}
			length, err := strconv.Atoi(response.Header().Get("Content-Length"))
			if err != nil || length <= 0 || method == "HEAD" && response.Body.Len() != 0 || method == "GET" && length != response.Body.Len() {
				t.Fatal(response.Header(), response.Body.String())
			}
			if method == "GET" {
				var body map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body) != 1 || body["status"] == nil {
					t.Fatal("public route disclosed detailed report", body, err)
				}
			}
			wantCalls := 0
			if kind == KindReady {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal(kind, calls)
			}
		}
	}
}

func TestHealthHTTPDependencyFailureIsNotLivenessFailure(t *testing.T) {
	checker := httpChecker(t, httpReadyState, func(context.Context) error { return errors.New("dsn=password@internal.example.test") })
	ready := serveProbe(httpProbe(t, checker, HTTPOptions{Kind: KindReady}), "GET")
	live := serveProbe(httpProbe(t, checker, HTTPOptions{Kind: KindLive}), "GET")
	startup := serveProbe(httpProbe(t, checker, HTTPOptions{Kind: KindStartup}), "GET")
	if ready.Code != 503 || live.Code != 200 || startup.Code != 200 || strings.Contains(ready.Body.String(), "database") || strings.Contains(ready.Body.String(), "password") {
		t.Fatal(ready.Code, ready.Body.String(), live.Code, startup.Code)
	}
}

func TestHealthHTTPDiagnosticsRequireCurrentAuthority(t *testing.T) {
	for _, stage := range []string{"first", "last"} {
		for _, failure := range []string{"deny", "mixed", "panic", "cancel"} {
			t.Run(stage+"/"+failure, func(t *testing.T) {
				checks, grants := 0, 0
				checker := httpChecker(t, httpReadyState, func(context.Context) error { checks++; return nil })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				handler := httpProbe(t, checker, HTTPOptions{Kind: KindReady, Diagnostics: true, Authorize: func(context.Context) error {
					grants++
					if stage == "last" && grants == 1 {
						return nil
					}
					switch failure {
					case "deny":
						return ErrForbidden
					case "mixed":
						return errors.Join(ErrForbidden, errors.New("private policy failure"))
					case "panic":
						panic("private policy failure")
					default:
						cancel()
						return nil
					}
				}})
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("GET", "http://example.test/health", nil).WithContext(ctx))
				want := 503
				if failure == "deny" {
					want = 403
				}
				if response.Code != want || strings.Contains(response.Body.String(), "database") || strings.Contains(response.Body.String(), "private policy") || stage == "first" && checks != 0 {
					t.Fatal(response.Code, response.Body.String(), checks, grants)
				}
			})
		}
	}
}

func TestHealthHTTPDiagnosticsAreSafeAndDoNotMaskDrain(t *testing.T) {
	state := httpReadyState()
	checker := httpChecker(t, func() State { return state }, func(context.Context) error { return nil })
	grants := 0
	handler := httpProbe(t, checker, HTTPOptions{Kind: KindReady, Diagnostics: true, Authorize: func(context.Context) error { grants++; return nil }})
	response := serveProbe(handler, "GET")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "database") || grants != 2 {
		t.Fatal(response.Code, response.Body.String(), grants)
	}
	grants = 0
	handler = httpProbe(t, checker, HTTPOptions{Kind: KindReady, Diagnostics: true, Authorize: func(context.Context) error {
		grants++
		if grants == 2 {
			state.Stopping = true
		}
		return nil
	}})
	response = serveProbe(handler, "GET")
	if response.Code != 503 || !strings.Contains(response.Body.String(), "not_ready") || grants != 2 {
		t.Fatal("cached report hid admission stop", response.Code, response.Body.String(), grants)
	}
}

type probeBody struct{ reads int }

func (b *probeBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*probeBody) Close() error               { return nil }

func TestHealthHTTPRejectsInvalidRequestsBeforeCallbacks(t *testing.T) {
	calls := 0
	checker := httpChecker(t, func() State { calls++; return httpReadyState() }, nil)
	handler := httpProbe(t, checker, HTTPOptions{Kind: KindLive})
	for _, method := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		response := serveProbe(handler, method)
		if response.Code != 405 || response.Header().Get("Allow") != "GET, HEAD" || calls != 0 {
			t.Fatal(response.Code, response.Header(), calls)
		}
	}
	body := &probeBody{}
	request := httptest.NewRequest("GET", "http://example.test/health", nil)
	request.Body = body
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 400 || calls != 0 || body.reads != 0 {
		t.Fatal(response.Code, calls, body.reads)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, nil)
	if response.Code != 400 || calls != 0 {
		t.Fatal(response.Code, calls)
	}
}

func TestHealthHTTPConstructorChecksAndCapturesConfiguration(t *testing.T) {
	checker := httpChecker(t, httpReadyState, nil)
	for _, options := range []HTTPOptions{
		{}, {Kind: Kind("unknown")}, {Kind: KindReady, Diagnostics: true},
		{Kind: KindReady, Authorize: func(context.Context) error { return nil }},
		{Kind: KindReady, Timeout: -time.Second}, {Kind: KindReady, Timeout: time.Nanosecond}, {Kind: KindReady, Timeout: time.Minute + time.Nanosecond},
	} {
		if handler, err := NewHandler(checker, options); handler != nil || err != ErrConfiguration {
			t.Fatal(handler, err)
		}
	}
	for _, invalid := range []*Checker{nil, {}} {
		if handler, err := NewHandler(invalid, HTTPOptions{Kind: KindLive}); handler != nil || err != ErrConfiguration {
			t.Fatal(handler, err)
		}
	}
	options := HTTPOptions{Kind: KindReady}
	handler := httpProbe(t, checker, options)
	*checker = Checker{}
	options.Kind = KindLive
	if response := serveProbe(handler, "GET"); response.Code != 200 || response.Body.String() != `{"status":"ready"}` {
		t.Fatal("constructor lost captured checker", response.Code, response.Body.String())
	}
}

type shortProbeWriter struct{ *httptest.ResponseRecorder }

func (w shortProbeWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestHealthHTTPAbortsShortWrite(t *testing.T) {
	handler := httpProbe(t, httpChecker(t, httpReadyState, nil), HTTPOptions{Kind: KindLive})
	defer func() {
		if value := recover(); value != http.ErrAbortHandler {
			t.Fatal("did not abort partial response", value)
		}
	}()
	handler.ServeHTTP(shortProbeWriter{httptest.NewRecorder()}, httptest.NewRequest("GET", "http://example.test/health", nil))
	t.Fatal("short write reported success")
}

func TestHealthHTTPIncludesOnlyTrustedRequestCorrelation(t *testing.T) {
	handler := corehttp.RequestIDs(httpProbe(t, httpChecker(t, httpReadyState, nil), HTTPOptions{Kind: KindLive}))
	response := serveProbe(handler, "GET")
	var body struct {
		Status    string `json:"status"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !safeProbeRequestID(body.RequestID) || body.RequestID != response.Header().Get("X-Request-ID") {
		t.Fatal(body, err)
	}
}

func TestHealthApplicationAdapterKeepsLifecycleQuestionsSeparate(t *testing.T) {
	if FromApplication(nil) != nil {
		t.Fatal("nil application source accepted")
	}
	application, err := app.Prepare(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	checker := httpChecker(t, FromApplication(application), func(context.Context) error { return errors.New("offline") })
	check := func(kind Kind, code int) {
		t.Helper()
		if response := serveProbe(httpProbe(t, checker, HTTPOptions{Kind: kind}), "GET"); response.Code != code {
			t.Fatal(kind, response.Code, response.Body.String())
		}
	}
	check(KindLive, 200)
	check(KindStartup, 503)
	if err := application.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	check(KindStartup, 200)
	check(KindReady, 503)
	check(KindLive, 200)
	application.StopAdmission()
	check(KindStartup, 200)
	check(KindReady, 503)
	check(KindLive, 200)
	if err := application.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	check(KindStartup, 200)
	check(KindReady, 503)
	check(KindLive, 503)
}
