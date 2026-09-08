package health

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	corehttp "github.com/Newton-School/gogo/core/http"
)

// HTTPOptions is explicit route configuration. A query parameter never enables
// diagnostics. Authorize is required for diagnostics and is a read-only current
// authority check, called before evaluation and again before emitting a report.
type HTTPOptions struct {
	Kind        Kind
	Diagnostics bool
	Authorize   func(context.Context) error
	Timeout     time.Duration
}

type probeHandler struct {
	checker Checker
	options HTTPOptions
}

// NewHandler mounts one probe, without opening listeners or installing routes.
// Register separate live/startup/ready handlers in the application URL tree.
// Public handlers return only status and an optional trusted request ID. Detailed
// reports require an explicit separate handler and authorization policy.
func NewHandler(checker *Checker, options HTTPOptions) (http.Handler, error) {
	if checker == nil || checker.state == nil || options.Kind != KindLive && options.Kind != KindStartup && options.Kind != KindReady || options.Diagnostics && options.Authorize == nil || !options.Diagnostics && options.Authorize != nil {
		return nil, ErrConfiguration
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if options.Timeout < time.Millisecond || options.Timeout > time.Minute {
		return nil, ErrConfiguration
	}
	return &probeHandler{checker: *checker, options: options}, nil
}

type probeResponse struct {
	status int
	body   []byte
}

func (h *probeHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	state := *h
	head := request != nil && request.Method == http.MethodHead
	response := state.response(request)
	w.Header().Del("Location")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(response.body)))
	if response.status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "GET, HEAD")
	}
	w.WriteHeader(response.status)
	if !head && len(response.body) != 0 {
		if n, err := w.Write(response.body); err != nil || n != len(response.body) {
			panic(http.ErrAbortHandler)
		}
	}
}

func (h *probeHandler) response(request *http.Request) (response probeResponse) {
	response = probeResponse{status: 503, body: []byte(`{"status":"unavailable"}`)}
	defer func() {
		if recover() != nil {
			response = probeResponse{status: 503, body: []byte(`{"status":"unavailable"}`)}
		}
	}()
	if request == nil {
		return probeResponse{status: 400, body: []byte(`{"status":"invalid_request"}`)}
	}
	// Capture request metadata before any application Context method executes.
	owned := *request
	method := owned.Method
	if method != http.MethodGet && method != http.MethodHead {
		return probeResponse{status: 405, body: []byte(`{"status":"method_not_allowed"}`)}
	}
	if owned.ContentLength != 0 || len(owned.TransferEncoding) != 0 || owned.Body != nil && owned.Body != http.NoBody || owned.Header.Get("Content-Encoding") != "" {
		return probeResponse{status: 400, body: []byte(`{"status":"invalid_request"}`)}
	}
	ctx, cancel := context.WithTimeout(owned.Context(), h.options.Timeout)
	defer cancel()
	if ctx.Err() != nil {
		return response
	}
	requestID := corehttp.RequestID(&owned)
	if !safeProbeRequestID(requestID) {
		requestID = ""
	}
	if h.options.Diagnostics {
		if err := h.authorize(ctx); err != nil {
			return deniedProbeResponse(err)
		}
	}
	report, err := h.checker.Check(ctx, h.options.Kind)
	if err != nil || ctx.Err() != nil {
		return response
	}
	if h.options.Diagnostics {
		if err := h.authorize(ctx); err != nil {
			return deniedProbeResponse(err)
		}
	}
	// A grant callback or cached result cannot hide a newly observed drain.
	// This fence only rechecks in-process state and never starts a DB/Redis probe.
	report, err = h.checker.recheck(ctx, h.options.Kind, report)
	if err != nil || ctx.Err() != nil {
		return response
	}
	var value any = struct {
		Status    Status `json:"status"`
		RequestID string `json:"request_id,omitempty"`
	}{report.Status, requestID}
	if h.options.Diagnostics {
		value = struct {
			Report    Report `json:"report"`
			RequestID string `json:"request_id,omitempty"`
		}{report, requestID}
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) > 64<<10 || ctx.Err() != nil {
		return response
	}
	status := http.StatusServiceUnavailable
	if report.Success() {
		status = http.StatusOK
	}
	return probeResponse{status: status, body: body}
}

func (h *probeHandler) authorize(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := h.options.Authorize(ctx)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	return err
}

func deniedProbeResponse(err error) probeResponse {
	if err == ErrForbidden {
		return probeResponse{status: 403, body: []byte(`{"status":"forbidden"}`)}
	}
	return probeResponse{status: 503, body: []byte(`{"status":"unavailable"}`)}
}

func safeProbeRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range []byte(value) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
