package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/auth"
)

func TestAPIRequestWrapsHTTPMetadata(t *testing.T) {
	raw := httptest.NewRequest("GET", "/api/posts/?page=2", nil)
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 7, IsActive: true, Authenticated: true}}}
	request := NewRequest(raw).
		WithParsedBody(map[string]any{"title": "Gogo"}).
		WithUser(user).
		WithAuth("session").
		WithVersion("v1").
		WithAcceptedRenderer("json")

	if request.Raw() != raw || request.QueryParam("page") != "2" {
		t.Fatalf("raw/query mismatch")
	}
	if request.User().ID != 7 || request.Auth() != "session" || request.Version() != "v1" || request.AcceptedRenderer() != "json" {
		t.Fatalf("request metadata = %#v", request)
	}
	if !reflect.DeepEqual(request.ParsedBody(), map[string]any{"title": "Gogo"}) {
		t.Fatalf("ParsedBody() = %#v", request.ParsedBody())
	}
}

func TestAPIResponseHelpersWriteJSONAndStatus(t *testing.T) {
	tests := []struct {
		name       string
		response   Response
		wantStatus int
		wantBody   string
	}{
		{"json", JSON(http.StatusOK, map[string]string{"ok": "yes"}), http.StatusOK, "{\"ok\":\"yes\"}\n"},
		{"created", Created(map[string]int{"id": 1}), http.StatusCreated, "{\"id\":1}\n"},
		{"accepted", Accepted(map[string]string{"state": "queued"}), http.StatusAccepted, "{\"state\":\"queued\"}\n"},
		{"no content", NoContent(), http.StatusNoContent, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			if err := tc.response.Write(recorder); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if recorder.Code != tc.wantStatus || recorder.Body.String() != tc.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", recorder.Code, recorder.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantBody != "" && recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
			}
		})
	}
}

func TestAPIErrorResponseShapeIncludesRequestIDAndFields(t *testing.T) {
	response := Error(http.StatusBadRequest, APIError{
		Code:      "invalid",
		Message:   "Invalid input",
		Fields:    map[string][]string{"title": {"required"}},
		RequestID: "req-123",
	})
	recorder := httptest.NewRecorder()
	if err := response.Write(recorder); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]APIError
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode error = %v", err)
	}
	if got := body["error"]; got.Code != "invalid" || got.Message != "Invalid input" || got.RequestID != "req-123" || got.Fields["title"][0] != "required" {
		t.Fatalf("error body = %#v", got)
	}
}

func TestAPIFileResponseWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	if err := File(path, "text/plain").Write(recorder); err != nil {
		t.Fatalf("Write(file) error = %v", err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "report" || recorder.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("file response = %d %q %q", recorder.Code, recorder.Body.String(), recorder.Header().Get("Content-Type"))
	}
}
