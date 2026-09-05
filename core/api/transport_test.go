package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ghttp "github.com/Newton-School/gogo/core/http"
)

func TestBoundedJSONRejectsDuplicatesAndTrailingInput(t *testing.T) {
	for _, body := range []string{`{"id":1,"id":2}`, `{"id":1,"\u0069d":2}`, `{"nested":{"x":1,"x":2}}`, `{"x":1} {}`, `[]`, `null`, "{\"x\":\"\xff\"}"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if _, err := Parse(r, ParseOptions{}); err == nil || ghttp.PublicError(err).Status != 400 {
			t.Fatalf("accepted %q: %v", body, err)
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"id":9007199254740993}`))
	r.Header.Set("Content-Type", "application/json")
	p, err := Parse(r, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Values["id"] != json.Number("9007199254740993") {
		t.Fatal(p.Values)
	}
}
func TestBodyAndMediaLimits(t *testing.T) {
	for _, test := range []struct {
		media, body   string
		limit         int64
		depth, status int
	}{{"text/xml", "<x/>", 100, 4, 415}, {"application/json; charset=latin-1", "{}", 100, 4, 415}, {"application/json", `{"x":123456789}`, 4, 4, 413}, {"application/json", `{"a":{"b":{"c":1}}}`, 100, 1, 400}} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(test.body))
		r.Header.Set("Content-Type", test.media)
		r.ContentLength = -1
		if _, err := Parse(r, ParseOptions{MaxBytes: test.limit, MaxDepth: test.depth}); err == nil || ghttp.PublicError(err).Status != test.status {
			t.Fatal(test, err)
		}
	}
}
func TestMultipartFilesAndRepeatedFormValues(t *testing.T) {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	_ = writer.WriteField("tags", "one")
	_ = writer.WriteField("tags", "two")
	file, err := writer.CreateFormFile("file", "sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("sample"))
	_ = writer.Close()
	r := httptest.NewRequest("POST", "/", &data)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	p, err := Parse(r, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if len(p.Values["tags"].([]any)) != 2 || p.Values["file"].(*multipart.FileHeader).Size != 6 {
		t.Fatal(p.Values)
	}
}
func TestNegotiationSpecificRejectionWinsWildcard(t *testing.T) {
	for _, accept := range []string{"text/html", "application/json;q=0, */*;q=1", "application/json;q=2", "application/json;q=NaN", "application/json;profile=unsupported"} {
		if _, err := Negotiate(accept, "application/json"); err == nil {
			t.Fatal(accept)
		}
	}
	if result, err := Negotiate("text/*;q=0.5, application/json;q=0.9", "text/plain", "application/json"); err != nil || result != "application/json" {
		t.Fatal(result, err)
	}
	if _, err := Negotiate("application/json;profile=private;q=0, application/json;q=1", "application/json;profile=private"); err == nil {
		t.Fatal("profile denial overridden by less specific range")
	}
	if result, err := Negotiate(`application/json;profile="one,two"`, `application/json;profile="one,two"`); err != nil || result == "" {
		t.Fatal("quoted comma rejected", err)
	}
}

func TestDecodedFormTextMustBeUTF8(t *testing.T) {
	for _, body := range []string{"name=%FF", "%FF=name", "name=one&name=%FF"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, err := Parse(r, ParseOptions{}); err == nil || ghttp.PublicError(err).Status != 400 {
			t.Fatal(body, err)
		}
	}
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	_ = writer.WriteField("name", "\xff")
	_ = writer.Close()
	r := httptest.NewRequest("POST", "/", &data)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	if _, err := Parse(r, ParseOptions{}); err == nil || ghttp.PublicError(err).Status != 400 {
		t.Fatal(err)
	}
}
func TestAPIValidationStatusAndErrorRedaction(t *testing.T) {
	s := mustSerializer(t, Definition{Fields: []Field{IntegerField("count")}})
	w := httptest.NewRecorder()
	Adapt(func(r *http.Request) (ghttp.Response, error) {
		_, err := s.Validate(r.Context(), Values{"count": "bad"}, BindOptions{})
		return ghttp.Response{}, err
	}).ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "count") {
		t.Fatal(w.Code, w.Body)
	}
	f := StringField("value")
	f.Validate = func(context.Context, any) (any, error) { return nil, errors.New("private connection DSN") }
	s = mustSerializer(t, Definition{Fields: []Field{f}})
	w = httptest.NewRecorder()
	Adapt(func(r *http.Request) (ghttp.Response, error) {
		_, err := s.Validate(r.Context(), Values{"value": "ok"}, BindOptions{})
		return ghttp.Response{}, err
	}).ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "DSN") {
		t.Fatal(w.Code, w.Body)
	}
}
