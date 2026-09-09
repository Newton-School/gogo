package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/forms"
)

func TestPublicSerializerExcludesInternalFields(t *testing.T) {
	serializer, err := ProductSerializer()
	if err != nil {
		t.Fatal(err)
	}
	output, err := serializer.Representation(context.Background(), api.Values{
		"id": int64(1), "name": "Example", "slug": "example", "description": "", "price": "12.50", "stock": int64(4), "published": false, "internal_note": "do not disclose",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 6 || output["published"] != nil || output["internal_note"] != nil {
		t.Fatalf("unexpected public field allowlist: %v", output)
	}
}

func TestFormValidation(t *testing.T) {
	form, err := EnquiryForm(forms.WithData(url.Values{"name": {"Developer"}, "email": {"developer@example.com"}, "quantity": {"3"}}))
	if err != nil || !form.IsValid() {
		t.Fatalf("valid enquiry rejected: %v", err)
	}
	bad, err := EnquiryForm(forms.WithData(url.Values{"email": {"not-an-address"}}))
	if err != nil {
		t.Fatal(err)
	}
	if bad.IsValid() {
		t.Fatal("invalid enquiry accepted")
	}
}

func TestPagesAndEscaping(t *testing.T) {
	for _, test := range []struct {
		path     string
		handler  http.HandlerFunc
		contains string
	}{
		{"/", Index, "One project."}, {"/fields/", Fields, "Model fields"}, {"/forms/", Form, "Try the form fields"},
	} {
		recorder := httptest.NewRecorder()
		test.handler(recorder, httptest.NewRequest("GET", test.path, nil))
		if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), test.contains) {
			t.Errorf("%s response: %d %s", test.path, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	Index(recorder, httptest.NewRequest("GET", "/missing", nil))
	if recorder.Code != 404 {
		t.Fatal("unknown page did not return 404")
	}
}

func TestEagerTasksAndCanvases(t *testing.T) {
	tasks, err := NewTasks()
	if err != nil {
		t.Fatal(err)
	}
	value, err := tasks.Double.Apply(context.Background(), 21)
	if err != nil || value != 42 {
		t.Fatalf("double: %d %v", value, err)
	}
	for _, kind := range []string{"group", "chain", "chord"} {
		if _, err := tasks.Canvas(kind); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if _, err := tasks.Double.Apply(context.Background(), 1000001); err == nil {
		t.Fatal("unbounded task input accepted")
	}
}
